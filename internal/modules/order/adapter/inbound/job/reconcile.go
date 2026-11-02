//go:build examples

// Package job adapts the reconciliation engine to order use cases.
package job

import (
	"context"
	"fmt"
	"time"

	"backend-go/internal/modules/order/usecase"
	"backend-go/internal/platform/reconciliation"
)

type reconciler interface {
	FindStale(ctx context.Context, threshold time.Duration, limit int) ([]usecase.StaleOrder, error)
	Repair(ctx context.Context, orderID string) error
}

type OutboxReconciliation struct{ uc reconciler }

var _ reconciliation.Job = (*OutboxReconciliation)(nil)

func NewOutboxReconciliation(uc reconciler) *OutboxReconciliation {
	return &OutboxReconciliation{uc: uc}
}

func (j *OutboxReconciliation) Name() string { return "orders-missing-outbox-event" }

func (j *OutboxReconciliation) FetchStaleRecords(ctx context.Context, threshold time.Duration, limit int) ([]reconciliation.StaleRecord, error) {
	orders, err := j.uc.FindStale(ctx, threshold, limit)
	if err != nil {
		return nil, err
	}
	out := make([]reconciliation.StaleRecord, len(orders))
	for i, o := range orders {
		out[i] = reconciliation.StaleRecord{ID: o.ID, Reference: o.ID, Status: o.Status, CreatedAt: o.CreatedAt}
	}
	return out, nil
}

func (j *OutboxReconciliation) Reconcile(ctx context.Context, r reconciliation.StaleRecord) (reconciliation.Outcome, error) {
	if r.ID == "" {
		return reconciliation.OutcomeFailed, fmt.Errorf("order ID is required")
	}
	if err := j.uc.Repair(ctx, r.ID); err != nil {
		return reconciliation.OutcomeRetryNeeded, fmt.Errorf("repair order %q: %w", r.ID, err)
	}
	return reconciliation.OutcomeResolved, nil
}
