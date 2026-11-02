//go:build examples

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"backend-go/internal/modules/order/domain"
	"backend-go/internal/modules/order/usecase/port"
	"backend-go/internal/platform/database"
	"backend-go/internal/platform/outbox"
)

var _ port.EventOutbox = (*EventOutbox)(nil)

// EventOutbox serialises domain events (event schema + version live here, not in
// the use case) and writes them to the outbox in the caller's transaction.
type EventOutbox struct {
	db         *database.DBGroup
	partitions int
}

func NewEventOutbox(db *database.DBGroup, partitions int) *EventOutbox {
	if partitions <= 0 {
		partitions = 1
	}
	return &EventOutbox{db: db, partitions: partitions}
}

// orderEventV1 is the published contract: see api/events/order.v1.schema.json.
type orderEventV1 struct {
	EventID     string `json:"event_id"`
	EventType   string `json:"event_type"`
	Version     int    `json:"version"`
	OccurredAt  string `json:"occurred_at"`
	OrderID     string `json:"order_id"`
	CustomerID  string `json:"customer_id,omitempty"`
	AmountMinor int64  `json:"amount_minor,omitempty"`
	Currency    string `json:"currency,omitempty"`
	Status      string `json:"status,omitempty"`
}

// EventID is deterministic ("order-created-<orderID>"): re-enqueueing is a no-op
// and consumers can de-duplicate on it.
func EventID(e domain.Event) string {
	return strings.ReplaceAll(e.EventName(), ".", "-") + "-" + e.AggregateID()
}

func (o *EventOutbox) Enqueue(ctx context.Context, events ...domain.Event) error {
	repo := outbox.NewPostgresRepository(database.Executor(ctx, o.db.Write()))
	for _, e := range events {
		payload, err := encode(e)
		if err != nil {
			return err
		}
		if err := repo.Save(ctx, &outbox.OutboxEvent{
			ID:            EventID(e),
			AggregateType: "order",
			AggregateID:   e.AggregateID(),
			EventType:     e.EventName(),
			Payload:       payload,
			Partition:     outbox.PartitionFor(e.AggregateID(), o.partitions),
		}); err != nil {
			return fmt.Errorf("enqueue %s: %w", e.EventName(), err)
		}
	}
	return nil
}

func encode(e domain.Event) ([]byte, error) {
	base := orderEventV1{EventID: EventID(e), EventType: e.EventName(), Version: 1,
		OccurredAt: e.OccurredAtTime().UTC().Format("2006-01-02T15:04:05.000Z07:00"), OrderID: e.AggregateID()}
	switch ev := e.(type) {
	case domain.OrderCreated:
		base.CustomerID, base.AmountMinor, base.Currency, base.Status =
			ev.CustomerID, ev.Amount.Minor(), ev.Amount.Currency(), string(ev.Status)
	case domain.OrderPaid:
		base.Status = string(domain.StatusPaid)
	default:
		return nil, fmt.Errorf("unsupported event type %T", e)
	}
	return json.Marshal(base)
}
