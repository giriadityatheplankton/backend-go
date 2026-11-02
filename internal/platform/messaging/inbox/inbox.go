// Package inbox is layer 3 of the idempotency design: consumer-side
// de-duplication. A message ID is recorded in the same database transaction as
// the business effect, so a redelivered message is acknowledged without being
// applied twice, and a crash before commit leaves nothing behind.
package inbox

import (
	"context"
	"fmt"
	"time"

	"backend-go/internal/platform/database"
	"backend-go/internal/platform/messaging"
)

// TxRunner runs fn inside a transaction carried by ctx (database.TxManager).
type TxRunner interface {
	WithinTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// Inbox de-duplicates messages for one logical consumer.
type Inbox struct {
	tx       TxRunner
	db       database.Querier
	consumer string
	obs      messaging.Observer
}

// New creates an Inbox. consumer must be unique per subscriber (e.g. "payment-service/order-events").
// obs may be nil.
func New(tx TxRunner, db database.Querier, consumer string, obs messaging.Observer) *Inbox {
	return &Inbox{tx: tx, db: db, consumer: consumer, obs: obs}
}

// Wrap returns a Handler that runs next at most once per message ID:
//
//	BEGIN; INSERT inbox ON CONFLICT DO NOTHING; (if new) next(ctx, m); COMMIT
//
// next must perform its writes through the transaction in ctx (repositories use
// database.Executor). The broker acknowledgement happens after this returns nil,
// i.e. after COMMIT.
func (i *Inbox) Wrap(next messaging.Handler) messaging.Handler {
	return func(ctx context.Context, m messaging.Message) error {
		duplicate := false
		err := i.tx.WithinTx(ctx, func(ctx context.Context) error {
			duplicate = false
			res, err := database.Executor(ctx, i.db).ExecContext(ctx,
				`INSERT INTO inbox_messages (consumer, message_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
				i.consumer, m.ID)
			if err != nil {
				return fmt.Errorf("inbox insert: %w", err)
			}
			if n, _ := res.RowsAffected(); n == 0 {
				duplicate = true
				return nil
			}
			return next(ctx, m)
		})
		if err == nil && duplicate && i.obs != nil {
			i.obs.IncConsumer(m.Stream, messaging.ResultDuplicate)
		}
		return err
	}
}

// Purge deletes entries older than before. Keep retention longer than the
// stream's retention and the maximum redelivery window.
func (i *Inbox) Purge(ctx context.Context, before time.Time, limit int) (int64, error) {
	if limit <= 0 {
		limit = 1000
	}
	res, err := i.db.ExecContext(ctx, `
		DELETE FROM inbox_messages WHERE (consumer, message_id) IN (
			SELECT consumer, message_id FROM inbox_messages WHERE received_at < $1 LIMIT $2)`, before, limit)
	if err != nil {
		return 0, fmt.Errorf("inbox purge: %w", err)
	}
	return res.RowsAffected()
}
