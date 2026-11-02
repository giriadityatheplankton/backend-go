package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// Querier is the subset of *sql.DB / *sql.Tx used by repositories.
type Querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type txKey struct{}

// Executor returns the transaction carried by ctx if there is one, otherwise
// fallback. Repositories call this so they join the use case's transaction
// without importing database/sql in the use case layer.
func Executor(ctx context.Context, fallback Querier) Querier {
	if tx, ok := ctx.Value(txKey{}).(*sql.Tx); ok && tx != nil {
		return tx
	}
	return fallback
}

// InTx reports whether ctx carries an open transaction.
func InTx(ctx context.Context) bool {
	tx, ok := ctx.Value(txKey{}).(*sql.Tx)
	return ok && tx != nil
}

// TxManager runs functions inside a database transaction and retries
// serialization failures (40001) and deadlocks (40P01).
type TxManager struct {
	db         *sql.DB
	maxRetries int
	opts       *sql.TxOptions
}

// NewTxManager creates a TxManager on the primary pool.
func NewTxManager(db *sql.DB) *TxManager { return &TxManager{db: db, maxRetries: 3} }

// WithRetries overrides the retry budget for retryable transaction failures.
func (m *TxManager) WithRetries(n int) *TxManager { cp := *m; cp.maxRetries = n; return &cp }

// WithOptions sets the isolation level / read-only flag for transactions.
func (m *TxManager) WithOptions(o *sql.TxOptions) *TxManager { cp := *m; cp.opts = o; return &cp }

// WithinTx runs fn in a transaction. If ctx already carries a transaction, fn
// joins it (no nested BEGIN, no retry: the outermost caller owns the boundary).
// fn may be invoked more than once, so it must be free of external side effects.
func (m *TxManager) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if InTx(ctx) {
		return fn(ctx)
	}
	if m == nil || m.db == nil {
		return errors.New("database: TxManager has no database")
	}
	var err error
	for attempt := 0; attempt <= m.maxRetries; attempt++ {
		err = m.runOnce(ctx, fn)
		if err == nil || !Retryable(err) || attempt == m.maxRetries {
			return err
		}
		backoff := time.Duration(10<<attempt) * time.Millisecond
		backoff += time.Duration(rand.Int64N(int64(backoff)/2 + 1))
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return errors.Join(err, ctx.Err())
		}
	}
	return err
}

func (m *TxManager) runOnce(ctx context.Context, fn func(ctx context.Context) error) (err error) {
	tx, err := m.db.BeginTx(ctx, m.opts)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
		if err != nil {
			if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
				err = errors.Join(err, fmt.Errorf("rollback: %w", rbErr))
			}
			return
		}
		if cErr := tx.Commit(); cErr != nil {
			err = fmt.Errorf("commit transaction: %w", cErr)
		}
	}()
	return fn(context.WithValue(ctx, txKey{}, tx))
}

// Retryable reports whether err is a serialization failure or deadlock.
func Retryable(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "40001" || pgErr.Code == "40P01"
	}
	return false
}

// IsUniqueViolation reports whether err is a unique_violation (23505).
func IsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
