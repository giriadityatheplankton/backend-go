//go:build examples

// Package domain holds the order aggregate and its invariants. It imports only
// the standard library and kernel; frameworks never reach this layer.
package domain

import (
	"strings"
	"time"
)

// Status is the order lifecycle state.
type Status string

const (
	StatusPaymentPending Status = "PAYMENT_PENDING"
	StatusPaid           Status = "PAID"
	StatusCancelled      Status = "CANCELLED"
)

// Order is the aggregate root. Fields are unexported so invariants cannot be bypassed.
type Order struct {
	id             string
	customerID     string
	amount         Money
	status         Status
	outboxEnqueued bool
	createdAt      time.Time
	events         []Event
}

// NewOrder places a new order and records OrderCreated.
func NewOrder(id, customerID string, amount Money, now time.Time) (*Order, error) {
	customerID = strings.TrimSpace(customerID)
	if id == "" || customerID == "" || !amount.IsPositive() {
		return nil, ErrInvalidOrder
	}
	o := &Order{id: id, customerID: customerID, amount: amount, status: StatusPaymentPending, createdAt: now.UTC()}
	o.record(OrderCreated{OrderID: id, CustomerID: customerID, Amount: amount, Status: o.status, OccurredAt: now.UTC()})
	return o, nil
}

// Rehydrate rebuilds an order from storage without raising events.
func Rehydrate(id, customerID string, amount Money, status Status, outboxEnqueued bool, createdAt time.Time) *Order {
	return &Order{id: id, customerID: customerID, amount: amount, status: status, outboxEnqueued: outboxEnqueued, createdAt: createdAt}
}

// MarkPaid moves a pending order to PAID. Calling it on a PAID order is a no-op,
// which makes payment events safe to process more than once.
func (o *Order) MarkPaid(now time.Time) error {
	switch o.status {
	case StatusPaid:
		return nil
	case StatusPaymentPending:
		o.status = StatusPaid
		o.record(OrderPaid{OrderID: o.id, OccurredAt: now.UTC()})
		return nil
	default:
		return ErrInvalidStatus
	}
}

// CreatedEvent rebuilds the OrderCreated event, used when reconciliation must
// restore a missing outbox record.
func (o *Order) CreatedEvent() OrderCreated {
	return OrderCreated{OrderID: o.id, CustomerID: o.customerID, Amount: o.amount, Status: StatusPaymentPending, OccurredAt: o.createdAt}
}

// PullEvents returns and clears the recorded domain events.
func (o *Order) PullEvents() []Event { e := o.events; o.events = nil; return e }

func (o *Order) record(e Event) { o.events = append(o.events, e) }

func (o *Order) ID() string           { return o.id }
func (o *Order) CustomerID() string   { return o.customerID }
func (o *Order) Amount() Money        { return o.amount }
func (o *Order) Status() Status       { return o.status }
func (o *Order) OutboxEnqueued() bool { return o.outboxEnqueued }
func (o *Order) CreatedAt() time.Time { return o.createdAt }
