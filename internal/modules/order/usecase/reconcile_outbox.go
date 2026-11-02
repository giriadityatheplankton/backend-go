//go:build examples

package usecase

import (
	"context"
	"fmt"
	"time"

	"backend-go/internal/kernel/clock"
	"backend-go/internal/modules/order/usecase/port"
)

// ReconcileOutbox is a self-healing job: it restores the order-created event for
// any order whose event was never enqueued (for example after a bug or a manual
// import). Re-enqueueing is idempotent because the event ID is deterministic and
// the outbox ignores duplicates.
type ReconcileOutbox struct {
	tx     port.TxManager
	repo   port.OrderRepository
	outbox port.EventOutbox
	clock  clock.Clock
}

func NewReconcileOutbox(tx port.TxManager, repo port.OrderRepository, outbox port.EventOutbox, clk clock.Clock) *ReconcileOutbox {
	return &ReconcileOutbox{tx: tx, repo: repo, outbox: outbox, clock: clk}
}

// StaleOrder identifies an order needing repair.
type StaleOrder struct {
	ID        string
	Status    string
	CreatedAt time.Time
}

// FindStale returns orders older than threshold with no enqueued event.
func (uc *ReconcileOutbox) FindStale(ctx context.Context, threshold time.Duration, limit int) ([]StaleOrder, error) {
	orders, err := uc.repo.FindWithoutOutbox(ctx, uc.clock.Now().Add(-threshold), limit)
	if err != nil {
		return nil, err
	}
	out := make([]StaleOrder, 0, len(orders))
	for _, o := range orders {
		out = append(out, StaleOrder{ID: o.ID(), Status: string(o.Status()), CreatedAt: o.CreatedAt()})
	}
	return out, nil
}

// Repair restores the missing event for one order.
func (uc *ReconcileOutbox) Repair(ctx context.Context, orderID string) error {
	return uc.tx.WithinTx(ctx, func(ctx context.Context) error {
		o, err := uc.repo.GetByID(ctx, orderID, port.ForUpdate)
		if err != nil {
			return err
		}
		if o.OutboxEnqueued() {
			return nil
		}
		if err := uc.outbox.Enqueue(ctx, o.CreatedEvent()); err != nil {
			return fmt.Errorf("restore order-created event: %w", err)
		}
		return uc.repo.MarkOutboxEnqueued(ctx, o.ID())
	})
}
