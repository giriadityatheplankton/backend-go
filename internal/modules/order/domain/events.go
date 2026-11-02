//go:build examples

package domain

import "time"

// Event is a fact that happened to an aggregate. Plain structs: no JSON tags, no
// broker types. Serialisation and schema versioning belong to the outbox adapter.
type Event interface {
	EventName() string
	AggregateID() string
	OccurredAtTime() time.Time
}

// OrderCreated is raised when an order is placed.
type OrderCreated struct {
	OrderID    string
	CustomerID string
	Amount     Money
	Status     Status
	OccurredAt time.Time
}

func (e OrderCreated) EventName() string         { return "order.created" }
func (e OrderCreated) AggregateID() string       { return e.OrderID }
func (e OrderCreated) OccurredAtTime() time.Time { return e.OccurredAt }

// OrderPaid is raised when payment succeeded.
type OrderPaid struct {
	OrderID    string
	OccurredAt time.Time
}

func (e OrderPaid) EventName() string         { return "order.paid" }
func (e OrderPaid) AggregateID() string       { return e.OrderID }
func (e OrderPaid) OccurredAtTime() time.Time { return e.OccurredAt }
