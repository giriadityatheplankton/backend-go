//go:build examples

package usecase

import (
	"context"
	"fmt"

	"backend-go/internal/kernel/clock"
	"backend-go/internal/modules/order/domain"
	"backend-go/internal/modules/order/usecase/port"
)

// MarkOrderPaid reacts to a payment-succeeded event. It is safe to run twice
// (MarkPaid on a PAID order is a no-op); the inbox additionally stops the second
// delivery from reaching it at all.
type MarkOrderPaid struct {
	tx     port.TxManager
	repo   port.OrderRepository
	outbox port.EventOutbox
	clock  clock.Clock
}

func NewMarkOrderPaid(tx port.TxManager, repo port.OrderRepository, outbox port.EventOutbox, clk clock.Clock) *MarkOrderPaid {
	return &MarkOrderPaid{tx: tx, repo: repo, outbox: outbox, clock: clk}
}

func (uc *MarkOrderPaid) Execute(ctx context.Context, orderID string) error {
	return uc.tx.WithinTx(ctx, func(ctx context.Context) error {
		o, err := uc.repo.GetByID(ctx, orderID, port.ForUpdate)
		if err != nil {
			return err
		}
		if err := o.MarkPaid(uc.clock.Now()); err != nil {
			return err
		}
		events := o.PullEvents()
		if len(events) == 0 {
			return nil // already paid
		}
		if err := uc.repo.Update(ctx, o); err != nil {
			return err
		}
		if err := uc.outbox.Enqueue(ctx, events...); err != nil {
			return fmt.Errorf("enqueue %s events: %w", domain.StatusPaid, err)
		}
		return nil
	})
}
