//go:build examples

// Package postgres implements the order ports on PostgreSQL.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"backend-go/internal/modules/order/domain"
	"backend-go/internal/modules/order/usecase/port"
	"backend-go/internal/platform/database"
)

var _ port.OrderRepository = (*OrderRepository)(nil)

// OrderRepository stores orders. Writes join the transaction carried by ctx.
type OrderRepository struct{ db *database.DBGroup }

func NewOrderRepository(db *database.DBGroup) *OrderRepository { return &OrderRepository{db: db} }

const selectCols = `id, customer_id, amount_minor, currency, status, outbox_enqueued, created_at`

type rowScanner interface{ Scan(dest ...any) error }

func scan(r rowScanner) (*domain.Order, error) {
	var (
		id, customer, currency, status string
		minor                          int64
		enqueued                       bool
		createdAt                      time.Time
	)
	if err := r.Scan(&id, &customer, &minor, &currency, &status, &enqueued, &createdAt); err != nil {
		return nil, err
	}
	amount, err := domain.NewMoney(minor, currency)
	if err != nil {
		return nil, fmt.Errorf("corrupt order %q in database: %w", id, err)
	}
	return domain.Rehydrate(id, customer, amount, domain.Status(status), enqueued, createdAt), nil
}

func (r *OrderRepository) Create(ctx context.Context, o *domain.Order, key, fingerprint string) error {
	res, err := database.Executor(ctx, r.db.Write()).ExecContext(ctx, `
		INSERT INTO example_orders (id, customer_id, amount_minor, currency, status, outbox_enqueued,
		                            idempotency_key, request_fingerprint, created_at)
		VALUES ($1,$2,$3,$4,$5,FALSE,$6,$7,$8)
		ON CONFLICT (idempotency_key) DO NOTHING`,
		o.ID(), o.CustomerID(), o.Amount().Minor(), o.Amount().Currency(), string(o.Status()), key, fingerprint, o.CreatedAt())
	if err != nil {
		return fmt.Errorf("insert order %q: %w", o.ID(), err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return port.ErrDuplicateKey
	}
	return nil
}

func (r *OrderRepository) FindByIdempotencyKey(ctx context.Context, key string) (*domain.Order, string, error) {
	// Primary, never a replica: a retry must see the original write.
	row := database.Executor(ctx, r.db.ReadPrimary()).QueryRowContext(ctx,
		`SELECT `+selectCols+`, request_fingerprint FROM example_orders WHERE idempotency_key = $1`, key)
	var fp string
	o, err := scanWith(row, &fp)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", domain.ErrOrderNotFound
	}
	if err != nil {
		return nil, "", fmt.Errorf("find order by idempotency key: %w", err)
	}
	return o, fp, nil
}

func scanWith(r rowScanner, extra ...any) (*domain.Order, error) {
	var (
		id, customer, currency, status string
		minor                          int64
		enqueued                       bool
		createdAt                      time.Time
	)
	dest := append([]any{&id, &customer, &minor, &currency, &status, &enqueued, &createdAt}, extra...)
	if err := r.Scan(dest...); err != nil {
		return nil, err
	}
	amount, err := domain.NewMoney(minor, currency)
	if err != nil {
		return nil, fmt.Errorf("corrupt order %q in database: %w", id, err)
	}
	return domain.Rehydrate(id, customer, amount, domain.Status(status), enqueued, createdAt), nil
}

func (r *OrderRepository) GetByID(ctx context.Context, id string, c port.Consistency) (*domain.Order, error) {
	var q database.Querier
	suffix := ""
	switch c {
	case port.ForUpdate:
		if !database.InTx(ctx) {
			return nil, errors.New("order repository: ForUpdate requires a transaction")
		}
		q, suffix = database.Executor(ctx, r.db.Write()), " FOR UPDATE"
	case port.Strong:
		q = database.Executor(ctx, r.db.ReadPrimary())
	default:
		q = r.db.Read() // replica (healthy ones only), may lag
	}
	o, err := scan(q.QueryRowContext(ctx, `SELECT `+selectCols+` FROM example_orders WHERE id = $1`+suffix, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrOrderNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get order %q: %w", id, err)
	}
	return o, nil
}

func (r *OrderRepository) Update(ctx context.Context, o *domain.Order) error {
	res, err := database.Executor(ctx, r.db.Write()).ExecContext(ctx,
		`UPDATE example_orders SET status = $2 WHERE id = $1`, o.ID(), string(o.Status()))
	if err != nil {
		return fmt.Errorf("update order %q: %w", o.ID(), err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrOrderNotFound
	}
	return nil
}

func (r *OrderRepository) FindWithoutOutbox(ctx context.Context, cutoff time.Time, limit int) (orders []*domain.Order, retErr error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := r.db.Write().QueryContext(ctx, `SELECT `+selectCols+`
		FROM example_orders WHERE outbox_enqueued = FALSE AND created_at < $1
		ORDER BY created_at, id LIMIT $2`, cutoff, limit)
	if err != nil {
		return nil, fmt.Errorf("find orders without outbox: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			retErr = errors.Join(retErr, cerr)
		}
	}()
	for rows.Next() {
		o, err := scan(rows)
		if err != nil {
			return nil, err
		}
		orders = append(orders, o)
	}
	return orders, rows.Err()
}

func (r *OrderRepository) MarkOutboxEnqueued(ctx context.Context, id string) error {
	_, err := database.Executor(ctx, r.db.Write()).ExecContext(ctx,
		`UPDATE example_orders SET outbox_enqueued = TRUE WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("mark order %q outbox enqueued: %w", id, err)
	}
	return nil
}
