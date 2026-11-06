//go:build examples

package domain

import "backend-go/internal/kernel/apperr"

// Domain errors carry their classification, so every transport maps them the same way.
var (
	ErrOrderNotFound        = apperr.New(apperr.KindNotFound, "ORDER_NOT_FOUND", "Order not found")
	ErrInvalidOrder         = apperr.New(apperr.KindInvalid, "INVALID_ORDER", "A valid customer_id, amount_minor and currency are required")
	ErrInvalidStatus        = apperr.New(apperr.KindFailedPrecondition, "INVALID_ORDER_STATUS", "The order cannot move to the requested status")
	ErrIdempotencyKeyReused = apperr.New(apperr.KindConflict, "IDEMPOTENCY_KEY_REUSED", "This idempotency key was already used with a different request")
	ErrIdempotencyKeyNeeded = apperr.New(apperr.KindInvalid, "IDEMPOTENCY_KEY_REQUIRED", "An idempotency key of 1-128 characters is required")
)
