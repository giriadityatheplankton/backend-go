//go:build examples

// Package consumer adapts broker messages to order use cases.
package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"backend-go/internal/modules/order/domain"
	"backend-go/internal/platform/messaging"
)

type orderPayer interface {
	Execute(ctx context.Context, orderID string) error
}

// PaymentEvents handles events published by the payment service.
type PaymentEvents struct{ uc orderPayer }

func NewPaymentEvents(uc orderPayer) *PaymentEvents { return &PaymentEvents{uc: uc} }

type paymentSucceeded struct {
	OrderID string `json:"order_id"`
}

// Handle is a thin humble object: decode, call the use case, classify the error.
// De-duplication (inbox) wraps it in app/, so it only needs to be correct once.
func (p *PaymentEvents) Handle(ctx context.Context, m messaging.Message) error {
	if m.Type != "payment.succeeded" {
		return nil // not ours; acknowledge so it does not block the stream
	}
	var ev paymentSucceeded
	if err := json.Unmarshal(m.Payload, &ev); err != nil {
		return messaging.Permanent(fmt.Errorf("invalid payment.succeeded payload: %w", err))
	}
	if ev.OrderID == "" {
		return messaging.Permanent(errors.New("payment.succeeded payload has no order_id"))
	}
	err := p.uc.Execute(ctx, ev.OrderID)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, domain.ErrOrderNotFound), errors.Is(err, domain.ErrInvalidStatus):
		return messaging.Permanent(err) // retrying cannot change the outcome
	default:
		return err // transient: redeliver
	}
}
