// Package messaging defines the broker-agnostic contracts used by the outbox
// relay (producer side) and consumers. Adapters live in sub-packages
// (redisstream, ...). Delivery is at-least-once: consumers MUST be idempotent,
// which is what the inbox sub-package provides.
package messaging

import (
	"context"
	"errors"
)

// Message is a broker-neutral envelope.
type Message struct {
	// ID is the deterministic business event ID assigned at the outbox. It is the
	// de-duplication key for consumers; do NOT use the broker's own message ID.
	ID string
	// Type is the event type, e.g. "order.created".
	Type string
	// Key is the aggregate ID; messages with the same key must be handled in order.
	Key     string
	Payload []byte
	Headers map[string]string

	// Populated by consumers.
	Stream  string
	Attempt int // 1 on first delivery
}

// Handler processes one message. Return nil to acknowledge. Return an error to
// request redelivery; wrap with Permanent for errors that retrying cannot fix
// (undecodable payload, failed validation): those go straight to the DLQ.
type Handler func(ctx context.Context, m Message) error

// Publisher sends a message to a topic. Implementations must be safe for concurrent use.
type Publisher interface {
	Publish(ctx context.Context, topic string, m Message) error
}

// Consumer blocks in Run until ctx is cancelled, then drains in-flight work.
type Consumer interface {
	Run(ctx context.Context) error
}

// Guard executes fn under a resilience policy (circuit breaker, bulkhead, ...).
// resilience.CircuitBreaker implements it.
type Guard interface {
	Do(ctx context.Context, fn func(ctx context.Context) error) error
}

// Observer receives consumer outcomes for metrics; nil is allowed.
type Observer interface {
	IncConsumer(stream, result string)
}

var errPermanent = errors.New("permanent message error")

// Permanent marks err as non-retryable.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &permanentError{err}
}

type permanentError struct{ err error }

func (p *permanentError) Error() string { return p.err.Error() }
func (p *permanentError) Unwrap() []error {
	return []error{p.err, errPermanent}
}

// IsPermanent reports whether err (or a wrapped error) was marked Permanent.
func IsPermanent(err error) bool { return errors.Is(err, errPermanent) }

// GuardedPublisher wraps a Publisher with a Guard (e.g. circuit breaker).
func GuardedPublisher(p Publisher, g Guard) Publisher {
	if g == nil {
		return p
	}
	return guarded{p: p, g: g}
}

type guarded struct {
	p Publisher
	g Guard
}

func (g guarded) Publish(ctx context.Context, topic string, m Message) error {
	return g.g.Do(ctx, func(ctx context.Context) error { return g.p.Publish(ctx, topic, m) })
}

// Result labels reported to Observer.
const (
	ResultOK        = "ok"
	ResultDuplicate = "duplicate"
	ResultRetry     = "retry"
	ResultDLQ       = "dlq"
	ResultPoison    = "poison"
)

// Subscription binds a handler to a stream. Modules return these; app/ wraps them
// with the inbox and runs them as consumers.
type Subscription struct {
	// Stream is the topic (without the service-wide stream prefix).
	Stream string
	// Name identifies the subscriber for inbox de-duplication; keep it stable.
	Name    string
	Handler Handler
}
