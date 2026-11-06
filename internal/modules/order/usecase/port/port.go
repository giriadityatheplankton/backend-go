//go:build examples

// Package port declares what the order use cases need from the outside world.
// The use-case layer owns these interfaces (dependency inversion); adapters implement them.
package port

import (
	"context"
	"errors"
	"time"

	"backend-go/internal/modules/order/domain"
)

// ErrDuplicateKey is returned by OrderRepository.Create when the idempotency key
// already exists. It is the database-level idempotency guard (layer 2).
var ErrDuplicateKey = errors.New("duplicate idempotency key")

// Consistency states how fresh a read must be.
type Consistency int

const (
	// EventuallyConsistent may be served from cache or a read replica.
	EventuallyConsistent Consistency = iota
	// Strong reads the primary and bypasses cache: use it for read-after-write and decisions.
	Strong
	// ForUpdate reads the primary and locks the row; requires an open transaction.
	ForUpdate
)

// OrderRepository persists orders.
type OrderRepository interface {
	// Create inserts the order with its idempotency key and request fingerprint.
	// It returns ErrDuplicateKey (and writes nothing) if the key already exists.
	Create(ctx context.Context, o *domain.Order, idempotencyKey, fingerprint string) error
	// FindByIdempotencyKey returns the order and its stored fingerprint, or domain.ErrOrderNotFound.
	FindByIdempotencyKey(ctx context.Context, key string) (*domain.Order, string, error)
	GetByID(ctx context.Context, id string, c Consistency) (*domain.Order, error)
	Update(ctx context.Context, o *domain.Order) error
	// FindWithoutOutbox lists orders older than cutoff whose events were never enqueued.
	FindWithoutOutbox(ctx context.Context, cutoff time.Time, limit int) ([]*domain.Order, error)
	MarkOutboxEnqueued(ctx context.Context, id string) error
}

// TxManager lets a use case define the transaction boundary without importing database/sql.
type TxManager interface {
	WithinTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// EventOutbox writes domain events to the outbox inside the caller's transaction.
type EventOutbox interface {
	Enqueue(ctx context.Context, events ...domain.Event) error
}

// IDGenerator creates identifiers.
type IDGenerator interface{ NewID() string }
