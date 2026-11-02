package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"sync"
	"time"

	"backend-go/internal/pkg/messaging"
	"backend-go/internal/pkg/observability/requestid"
	"backend-go/internal/pkg/observability/telemetry"
)

// TopicMapper derives the broker topic/stream from an event.
type TopicMapper func(*OutboxEvent) string

// ByAggregateType routes every event of an aggregate type to one topic, which
// keeps per-aggregate ordering and bounds the number of streams.
func ByAggregateType(prefix string) TopicMapper {
	return func(ev *OutboxEvent) string { return prefix + ev.AggregateType }
}

// Observer receives relay metrics; nil is allowed.
type Observer interface {
	IncOutbox(result string)
	SetOutboxOldestPending(seconds float64)
}

// WorkerConfig holds tuning options for the outbox relay.
type WorkerConfig struct {
	PartitionCount int
	BatchSize      int
	PollInterval   time.Duration
	// LeaseDuration is how long a claimed event stays reserved. The relay extends
	// it while a batch is in progress; if the pod dies, another pod reclaims it.
	LeaseDuration  time.Duration
	PublishTimeout time.Duration
	MaxRetries     int
	BaseBackoff    time.Duration
	MaxBackoff     time.Duration
	// PreserveOrder blocks an event while an earlier event of the same aggregate
	// is unpublished, so consumers see per-aggregate order. Disable for throughput
	// when ordering does not matter.
	PreserveOrder bool
	// Owner identifies this relay instance for lease fencing. Defaults to hostname+random.
	Owner         string
	StatsInterval time.Duration
}

// DefaultConfig provides sensible production defaults.
func DefaultConfig() WorkerConfig {
	return WorkerConfig{
		PartitionCount: 8,
		BatchSize:      100,
		PollInterval:   200 * time.Millisecond,
		LeaseDuration:  30 * time.Second,
		PublishTimeout: 5 * time.Second,
		MaxRetries:     5,
		BaseBackoff:    time.Second,
		MaxBackoff:     5 * time.Minute,
		PreserveOrder:  true,
		StatsInterval:  15 * time.Second,
	}
}

func (c *WorkerConfig) normalise() {
	d := DefaultConfig()
	if c.PartitionCount <= 0 {
		c.PartitionCount = d.PartitionCount
	}
	if c.BatchSize <= 0 {
		c.BatchSize = d.BatchSize
	}
	if c.PollInterval <= 0 {
		c.PollInterval = d.PollInterval
	}
	if c.LeaseDuration <= 0 {
		c.LeaseDuration = d.LeaseDuration
	}
	if c.PublishTimeout <= 0 {
		c.PublishTimeout = d.PublishTimeout
	}
	if c.MaxRetries <= 0 {
		c.MaxRetries = d.MaxRetries
	}
	if c.BaseBackoff <= 0 {
		c.BaseBackoff = d.BaseBackoff
	}
	if c.MaxBackoff <= 0 {
		c.MaxBackoff = d.MaxBackoff
	}
	if c.StatsInterval <= 0 {
		c.StatsInterval = d.StatsInterval
	}
	if c.Owner == "" {
		host, _ := os.Hostname()
		c.Owner = fmt.Sprintf("%s-%08x", host, rand.Uint32())
	}
}

// Worker relays outbox events to a messaging.Publisher. Exclusivity comes from
// Postgres row leases (FOR UPDATE SKIP LOCKED), not from Redis, so a Redis
// outage cannot stall the outbox.
type Worker struct {
	cfg       WorkerConfig
	repo      Claimer
	stats     StatsReader
	publisher messaging.Publisher
	topic     TopicMapper
	obs       Observer
}

// NewWorker creates a relay. topic may be nil (defaults to the aggregate type); obs may be nil.
func NewWorker(cfg WorkerConfig, repo Claimer, publisher messaging.Publisher, topic TopicMapper, obs Observer) *Worker {
	cfg.normalise()
	if topic == nil {
		topic = ByAggregateType("")
	}
	w := &Worker{cfg: cfg, repo: repo, publisher: publisher, topic: topic, obs: obs}
	if sr, ok := repo.(StatsReader); ok {
		w.stats = sr
	}
	return w
}

// Run blocks until ctx is cancelled and all partition loops have drained. It
// never returns an error for transient failures; those are logged and retried.
func (w *Worker) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	for p := 0; p < w.cfg.PartitionCount; p++ {
		wg.Add(1)
		go func(p int) { defer wg.Done(); w.partitionLoop(ctx, p) }(p)
	}
	if w.stats != nil && w.obs != nil {
		wg.Add(1)
		go func() { defer wg.Done(); w.statsLoop(ctx) }()
	}
	wg.Wait()
	return nil
}

func (w *Worker) partitionLoop(ctx context.Context, partition int) {
	// Spread pollers so N pods do not hit the database in lockstep.
	jitter := time.Duration(rand.Int64N(int64(w.cfg.PollInterval)))
	select {
	case <-time.After(jitter):
	case <-ctx.Done():
		return
	}
	for ctx.Err() == nil {
		n := w.processPartition(ctx, partition)
		if n >= w.cfg.BatchSize {
			continue // backlog: drain without sleeping
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(w.cfg.PollInterval):
		}
	}
}

func (w *Worker) processPartition(ctx context.Context, partition int) int {
	events, err := w.repo.Claim(ctx, partition, w.cfg.BatchSize, w.cfg.Owner, w.cfg.LeaseDuration, w.cfg.PreserveOrder)
	if err != nil {
		if ctx.Err() == nil {
			slog.ErrorContext(ctx, "Failed to claim outbox events", "partition", partition, "error", err)
		}
		return 0
	}
	claimedAt := time.Now()
	for i, ev := range events {
		if ctx.Err() != nil {
			w.release(events[i:]) //nolint:contextcheck // runs after ctx is cancelled, on purpose
			break
		}
		if time.Since(claimedAt) > w.cfg.LeaseDuration/2 {
			w.extend(ctx, events[i:])
			claimedAt = time.Now()
		}
		w.processEvent(ctx, ev)
	}
	return len(events)
}

func (w *Worker) extend(ctx context.Context, events []*OutboxEvent) {
	if err := w.repo.ExtendLease(ctx, w.cfg.Owner, ids(events), w.cfg.LeaseDuration); err != nil {
		slog.WarnContext(ctx, "Failed to extend outbox lease", "error", err)
	}
}

func (w *Worker) release(events []*OutboxEvent) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := w.repo.ReleaseLease(ctx, w.cfg.Owner, ids(events)); err != nil {
		slog.Warn("Failed to release outbox lease on shutdown", "error", err)
	}
}

func ids(events []*OutboxEvent) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.ID
	}
	return out
}

func (w *Worker) processEvent(ctx context.Context, ev *OutboxEvent) {
	// Rebuild correlation from the headers stored at enqueue time.
	evCtx := ctx
	if id := ev.Headers[requestid.Header]; requestid.Valid(id) {
		evCtx = requestid.With(evCtx, id)
	}
	if len(ev.Headers) > 0 {
		evCtx = telemetry.ContextWithSpan(evCtx, telemetry.ExtractMap(ev.Headers))
	}

	// A publish that already started should finish even during shutdown, but never hang forever.
	pubCtx, cancel := context.WithTimeout(context.WithoutCancel(evCtx), w.cfg.PublishTimeout)
	defer cancel()

	var err error
	if w.publisher == nil {
		err = errors.New("outbox message publisher is unavailable")
	} else {
		err = w.publisher.Publish(pubCtx, w.topic(ev), messaging.Message{
			ID: ev.ID, Type: ev.EventType, Key: ev.AggregateID, Payload: ev.Payload, Headers: ev.Headers,
		})
	}
	// State updates use a detached, bounded context for the same reason.
	dbCtx, dbCancel := context.WithTimeout(context.WithoutCancel(evCtx), 5*time.Second)
	defer dbCancel()

	if err == nil {
		switch mErr := w.repo.MarkPublished(dbCtx, ev.ID, w.cfg.Owner); {
		case mErr == nil:
			w.inc("ok")
			slog.InfoContext(evCtx, "Outbox event published", "event_id", ev.ID, "type", ev.EventType)
		case errors.Is(mErr, ErrLeaseLost):
			// Published but the lease expired first. Another relay may publish again:
			// that is the at-least-once case consumers de-duplicate with the inbox.
			slog.WarnContext(evCtx, "Outbox lease lost after publish; duplicate delivery possible", "event_id", ev.ID)
		default:
			slog.ErrorContext(evCtx, "Event published but could not be marked published", "event_id", ev.ID, "error", mErr)
		}
		return
	}

	attempt := ev.RetryCount + 1
	maxRetries := ev.MaxRetries
	if maxRetries <= 0 {
		maxRetries = w.cfg.MaxRetries
	}
	if attempt >= maxRetries || messaging.IsPermanent(err) {
		reason := fmt.Sprintf("after %d attempt(s): %v", attempt, err)
		if dlqErr := w.repo.MoveToDLQ(dbCtx, ev.ID, w.cfg.Owner, reason); dlqErr != nil {
			slog.ErrorContext(evCtx, "Failed to move outbox event to DLQ", "event_id", ev.ID, "error", dlqErr)
			return
		}
		w.inc("dlq")
		slog.ErrorContext(evCtx, "Outbox event moved to DLQ", "event_id", ev.ID, "attempts", attempt, "error", err)
		return
	}

	next := time.Now().Add(Backoff(w.cfg.BaseBackoff, w.cfg.MaxBackoff, attempt))
	if mErr := w.repo.MarkFailed(dbCtx, ev.ID, w.cfg.Owner, err.Error(), next); mErr != nil {
		slog.ErrorContext(evCtx, "Failed to schedule outbox retry", "event_id", ev.ID, "error", mErr)
		return
	}
	w.inc("retry")
	slog.WarnContext(evCtx, "Outbox publish failed; retry scheduled", "event_id", ev.ID, "attempt", attempt, "next_retry", next, "error", err)
}

func (w *Worker) inc(result string) {
	if w.obs != nil {
		w.obs.IncOutbox(result)
	}
}

func (w *Worker) statsLoop(ctx context.Context) {
	t := time.NewTicker(w.cfg.StatsInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			qctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			s, err := w.stats.Stats(qctx)
			cancel()
			if err != nil {
				slog.WarnContext(ctx, "Failed to read outbox stats", "error", err)
				continue
			}
			w.obs.SetOutboxOldestPending(s.OldestPendingAge.Seconds())
		}
	}
}

// Backoff returns an exponential delay (base * 2^(attempt-1)) capped at max,
// with +/-20% jitter so retries from many events do not synchronise.
func Backoff(base, max time.Duration, attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := base
	for i := 1; i < attempt && d < max; i++ {
		d *= 2
	}
	if d > max {
		d = max
	}
	jitter := 0.8 + 0.4*rand.Float64()
	return time.Duration(float64(d) * jitter)
}
