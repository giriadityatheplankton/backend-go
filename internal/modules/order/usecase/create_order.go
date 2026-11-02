//go:build examples

// Package usecase contains the order application logic: one file per use case.
// It depends on domain and on ports only.
package usecase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"backend-go/internal/kernel/clock"
	"backend-go/internal/modules/order/domain"
	"backend-go/internal/modules/order/usecase/port"
)

// CreateOrderInput crosses the boundary as plain data: no gin, proto or JSON tags.
type CreateOrderInput struct {
	IdempotencyKey string
	CustomerID     string
	AmountMinor    int64
	Currency       string
}

// OrderOutput is the use-case result shared by create and read.
type OrderOutput struct {
	ID          string
	CustomerID  string
	AmountMinor int64
	Currency    string
	Status      string
	CreatedAt   int64 // unix seconds
	Replayed    bool
}

func toOutput(o *domain.Order, replayed bool) OrderOutput {
	return OrderOutput{
		ID: o.ID(), CustomerID: o.CustomerID(), AmountMinor: o.Amount().Minor(), Currency: o.Amount().Currency(),
		Status: string(o.Status()), CreatedAt: o.CreatedAt().Unix(), Replayed: replayed,
	}
}

// CreateOrder places an order exactly once per idempotency key.
type CreateOrder struct {
	tx     port.TxManager
	repo   port.OrderRepository
	outbox port.EventOutbox
	ids    port.IDGenerator
	clock  clock.Clock
}

func NewCreateOrder(tx port.TxManager, repo port.OrderRepository, outbox port.EventOutbox, ids port.IDGenerator, clk clock.Clock) *CreateOrder {
	return &CreateOrder{tx: tx, repo: repo, outbox: outbox, ids: ids, clock: clk}
}

// Execute is idempotent (layer 2 of the idempotency design): the database UNIQUE
// key is the source of truth, so it stays correct even if the Redis edge cache
// was flushed or bypassed.
func (uc *CreateOrder) Execute(ctx context.Context, in CreateOrderInput) (OrderOutput, error) {
	key := strings.TrimSpace(in.IdempotencyKey)
	if key == "" || len(key) > 128 {
		return OrderOutput{}, domain.ErrIdempotencyKeyNeeded
	}
	amount, err := domain.NewMoney(in.AmountMinor, in.Currency)
	if err != nil {
		return OrderOutput{}, err
	}
	customerID := strings.TrimSpace(in.CustomerID)
	fp := fingerprint(customerID, amount)

	// Fast path: a retry of a request that already succeeded.
	if out, found, err := uc.replay(ctx, key, fp); err != nil || found {
		return out, err
	}

	order, err := domain.NewOrder(uc.ids.NewID(), customerID, amount, uc.clock.Now())
	if err != nil {
		return OrderOutput{}, err
	}
	events := order.PullEvents() // taken once, outside the closure: the closure may be retried

	err = uc.tx.WithinTx(ctx, func(ctx context.Context) error {
		if err := uc.repo.Create(ctx, order, key, fp); err != nil {
			return err
		}
		if err := uc.outbox.Enqueue(ctx, events...); err != nil {
			return fmt.Errorf("enqueue order events: %w", err)
		}
		return uc.repo.MarkOutboxEnqueued(ctx, order.ID())
	})
	if errors.Is(err, port.ErrDuplicateKey) {
		// Lost a race with a concurrent request carrying the same key.
		out, found, rerr := uc.replay(ctx, key, fp)
		if rerr != nil {
			return OrderOutput{}, rerr
		}
		if !found {
			return OrderOutput{}, fmt.Errorf("idempotency key vanished after conflict: %w", err)
		}
		return out, nil
	}
	if err != nil {
		return OrderOutput{}, fmt.Errorf("create order: %w", err)
	}
	return toOutput(order, false), nil
}

func (uc *CreateOrder) replay(ctx context.Context, key, fp string) (OrderOutput, bool, error) {
	existing, storedFP, err := uc.repo.FindByIdempotencyKey(ctx, key)
	if errors.Is(err, domain.ErrOrderNotFound) {
		return OrderOutput{}, false, nil
	}
	if err != nil {
		return OrderOutput{}, false, fmt.Errorf("lookup idempotency key: %w", err)
	}
	if storedFP != fp {
		return OrderOutput{}, false, domain.ErrIdempotencyKeyReused
	}
	return toOutput(existing, true), true, nil
}

func fingerprint(customerID string, amount domain.Money) string {
	sum := sha256.Sum256([]byte(customerID + "\n" + strconv.FormatInt(amount.Minor(), 10) + "\n" + amount.Currency()))
	return hex.EncodeToString(sum[:])
}
