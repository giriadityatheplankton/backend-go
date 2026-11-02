package outbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"backend-go/internal/pkg/observability/requestid"
	"backend-go/internal/pkg/observability/telemetry"
)

// SQLExecutor is implemented by *sql.DB and *sql.Tx.
type SQLExecutor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// PostgresRepository persists outbox events and DLQ records in PostgreSQL.
// For Save, pass the business transaction (or use database.Executor(ctx, db)) so
// the event commits atomically with the state change.
type PostgresRepository struct {
	db SQLExecutor
}

// NewPostgresRepository creates a PostgreSQL-backed repository.
func NewPostgresRepository(db SQLExecutor) *PostgresRepository {
	return &PostgresRepository{db: db}
}

var _ Repository = (*PostgresRepository)(nil)

func (p *PostgresRepository) checkDB() error {
	if p == nil || p.db == nil {
		return errors.New("outbox PostgreSQL repository has no database executor")
	}
	return nil
}

// Enrich copies request and trace correlation from ctx into event headers.
func Enrich(ctx context.Context, ev *OutboxEvent) {
	if ev.Headers == nil {
		ev.Headers = map[string]string{}
	}
	if id := requestid.From(ctx); id != "" {
		if _, ok := ev.Headers[requestid.Header]; !ok {
			ev.Headers[requestid.Header] = id
		}
	}
	if _, ok := ev.Headers[telemetry.HeaderTraceParent]; !ok {
		telemetry.InjectMap(ctx, ev.Headers)
	}
}

func (p *PostgresRepository) Save(ctx context.Context, event *OutboxEvent) error {
	if err := p.checkDB(); err != nil {
		return err
	}
	if event == nil || event.ID == "" {
		return errors.New("outbox event and event ID are required")
	}
	Enrich(ctx, event)

	now := time.Now().UTC()
	status := event.Status
	if status == "" {
		status = StatusPending
	}
	nextRetryAt := event.NextRetryAt
	if nextRetryAt.IsZero() {
		nextRetryAt = now
	}
	createdAt := event.CreatedAt
	if createdAt.IsZero() {
		createdAt = now
	}
	maxRetries := event.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 5
	}
	payload := event.Payload
	if payload == nil {
		payload = []byte{}
	}
	headers, err := json.Marshal(event.Headers)
	if err != nil {
		return fmt.Errorf("marshal outbox headers: %w", err)
	}

	_, err = p.db.ExecContext(ctx, `
		INSERT INTO outbox_events (
			id, aggregate_type, aggregate_id, event_type, payload, headers,
			partition, status, retry_count, max_retries, next_retry_at, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7,$8,0,$9,$10,$11,$11)
		ON CONFLICT (id) DO NOTHING`,
		event.ID, event.AggregateType, event.AggregateID, event.EventType,
		payload, string(headers), event.Partition, status, maxRetries, nextRetryAt, createdAt,
	)
	if err != nil {
		return fmt.Errorf("save outbox event %q: %w", event.ID, err)
	}
	return nil
}

const claimQuery = `
WITH claimable AS (
	SELECT o.id FROM outbox_events o
	WHERE o.partition = $1
	  AND (
	        (o.status IN ('PENDING','FAILED') AND o.next_retry_at <= now())
	     OR (o.status = 'PROCESSING' AND o.locked_until < now())
	  )
	  AND ($5::boolean = FALSE OR NOT EXISTS (
	        SELECT 1 FROM outbox_events prev
	        WHERE prev.aggregate_id = o.aggregate_id
	          AND prev.partition = o.partition
	          AND prev.id <> o.id
	          AND prev.status IN ('PENDING','FAILED','PROCESSING')
	          AND (prev.created_at, prev.id) < (o.created_at, o.id)))
	ORDER BY o.created_at, o.id
	LIMIT $2
	FOR UPDATE SKIP LOCKED
)
UPDATE outbox_events e
SET status = 'PROCESSING', locked_by = $3,
    locked_until = now() + make_interval(secs => $4), updated_at = now()
FROM claimable c WHERE e.id = c.id
RETURNING e.id, e.aggregate_type, e.aggregate_id, e.event_type, e.payload, e.headers,
          e.partition, e.status, e.retry_count, e.max_retries, e.next_retry_at,
          e.last_error, e.created_at, e.updated_at`

func (p *PostgresRepository) Claim(ctx context.Context, partition, limit int, owner string, leaseFor time.Duration, preserveOrder bool) (events []*OutboxEvent, retErr error) {
	if err := p.checkDB(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		return nil, nil
	}
	rows, err := p.db.QueryContext(ctx, claimQuery, partition, limit, owner, leaseFor.Seconds(), preserveOrder)
	if err != nil {
		return nil, fmt.Errorf("claim outbox events for partition %d: %w", partition, err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			retErr = errors.Join(retErr, fmt.Errorf("close claimed outbox rows: %w", closeErr))
		}
	}()
	for rows.Next() {
		ev := new(OutboxEvent)
		var headers []byte
		var lastError sql.NullString
		if err := rows.Scan(&ev.ID, &ev.AggregateType, &ev.AggregateID, &ev.EventType, &ev.Payload, &headers,
			&ev.Partition, &ev.Status, &ev.RetryCount, &ev.MaxRetries, &ev.NextRetryAt,
			&lastError, &ev.CreatedAt, &ev.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan claimed outbox event: %w", err)
		}
		if err := json.Unmarshal(headers, &ev.Headers); err != nil {
			return nil, fmt.Errorf("decode headers for outbox event %q: %w", ev.ID, err)
		}
		ev.LastError = lastError.String
		ev.LockedBy = owner
		events = append(events, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate claimed outbox events: %w", err)
	}
	return events, nil
}

func (p *PostgresRepository) ExtendLease(ctx context.Context, owner string, ids []string, leaseFor time.Duration) error {
	if err := p.checkDB(); err != nil || len(ids) == 0 {
		return err
	}
	_, err := p.db.ExecContext(ctx, `
		UPDATE outbox_events SET locked_until = now() + make_interval(secs => $3)
		WHERE locked_by = $1 AND status = 'PROCESSING' AND id = ANY($2::text[])`,
		owner, ids, leaseFor.Seconds())
	if err != nil {
		return fmt.Errorf("extend outbox lease: %w", err)
	}
	return nil
}

func (p *PostgresRepository) ReleaseLease(ctx context.Context, owner string, ids []string) error {
	if err := p.checkDB(); err != nil || len(ids) == 0 {
		return err
	}
	_, err := p.db.ExecContext(ctx, `
		UPDATE outbox_events SET status = 'PENDING', locked_by = NULL, locked_until = NULL, updated_at = now()
		WHERE locked_by = $1 AND status = 'PROCESSING' AND id = ANY($2::text[])`, owner, ids)
	if err != nil {
		return fmt.Errorf("release outbox lease: %w", err)
	}
	return nil
}

func (p *PostgresRepository) fenced(ctx context.Context, what, id string, query string, args ...any) error {
	if err := p.checkDB(); err != nil {
		return err
	}
	res, err := p.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("%s outbox event %q: %w", what, id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrLeaseLost
	}
	return nil
}

func (p *PostgresRepository) MarkPublished(ctx context.Context, id, owner string) error {
	return p.fenced(ctx, "mark published", id, `
		UPDATE outbox_events SET status = 'PUBLISHED', locked_by = NULL, locked_until = NULL, updated_at = now()
		WHERE id = $1 AND locked_by = $2 AND status = 'PROCESSING'`, id, owner)
}

func (p *PostgresRepository) MarkFailed(ctx context.Context, id, owner, errStr string, nextRetry time.Time) error {
	return p.fenced(ctx, "mark failed", id, `
		UPDATE outbox_events SET status = 'FAILED', retry_count = retry_count + 1, last_error = $3,
			next_retry_at = $4, locked_by = NULL, locked_until = NULL, updated_at = now()
		WHERE id = $1 AND locked_by = $2 AND status = 'PROCESSING'`, id, owner, errStr, nextRetry)
}

func (p *PostgresRepository) MoveToDLQ(ctx context.Context, id, owner, reason string) error {
	// One statement: the status change and the DLQ insert succeed or fail together.
	return p.fenced(ctx, "move to DLQ", id, `
		WITH moved AS (
			UPDATE outbox_events
			SET status = 'DLQ', last_error = $3, retry_count = retry_count + 1,
			    locked_by = NULL, locked_until = NULL, updated_at = now()
			WHERE id = $1 AND locked_by = $2 AND status = 'PROCESSING'
			RETURNING id, event_type, payload, headers, retry_count
		), ins AS (
			INSERT INTO dlq_events (id, original_event_id, event_type, payload, headers, failure_reason, retry_attempts, failed_at)
			SELECT 'dlq_' || id, id, event_type, payload, headers, $3, retry_count, now() FROM moved
			ON CONFLICT (id) DO UPDATE SET failure_reason = EXCLUDED.failure_reason,
				retry_attempts = EXCLUDED.retry_attempts, failed_at = EXCLUDED.failed_at
		)
		SELECT 1 FROM moved`, id, owner, reason)
}

func (p *PostgresRepository) DeletePublishedBefore(ctx context.Context, before time.Time, limit int) (int64, error) {
	if err := p.checkDB(); err != nil {
		return 0, err
	}
	if limit <= 0 {
		limit = 1000
	}
	res, err := p.db.ExecContext(ctx, `
		DELETE FROM outbox_events WHERE id IN (
			SELECT id FROM outbox_events WHERE status = 'PUBLISHED' AND updated_at < $1
			ORDER BY updated_at LIMIT $2 FOR UPDATE SKIP LOCKED)`, before, limit)
	if err != nil {
		return 0, fmt.Errorf("delete published outbox events before %s: %w", before, err)
	}
	return res.RowsAffected()
}

func (p *PostgresRepository) Stats(ctx context.Context) (Stats, error) {
	if err := p.checkDB(); err != nil {
		return Stats{}, err
	}
	var count int64
	var ageSec float64
	err := p.db.QueryRowContext(ctx, `
		SELECT count(*), COALESCE(EXTRACT(EPOCH FROM now() - min(created_at)), 0)
		FROM outbox_events WHERE status IN ('PENDING','FAILED','PROCESSING')`).Scan(&count, &ageSec)
	if err != nil {
		return Stats{}, fmt.Errorf("outbox stats: %w", err)
	}
	return Stats{Unpublished: count, OldestPendingAge: time.Duration(ageSec * float64(time.Second))}, nil
}
