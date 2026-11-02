package outbox

import (
	"context"
	"errors"
	"time"
)

// ErrLeaseLost means the event is no longer leased to this worker (expired and
// reclaimed). The worker must not touch the event again.
var ErrLeaseLost = errors.New("outbox lease lost")

// The repository is split by consumer (interface segregation): each user depends
// only on the methods it calls. PostgresRepository and MemoryRepository
// implement all of them.

// Writer is used by modules to enqueue events in their business transaction.
type Writer interface {
	// Save inserts the event. If an event with the same ID exists it is left
	// untouched (ON CONFLICT DO NOTHING), so replays and reconciliation can never
	// resurrect an already published event.
	Save(ctx context.Context, event *OutboxEvent) error
}

// Claimer is used by the Worker. Every mutation is fenced by owner.
type Claimer interface {
	// Claim leases up to limit due events of a partition to owner for leaseFor,
	// reclaiming PROCESSING events whose lease expired (crashed worker).
	Claim(ctx context.Context, partition, limit int, owner string, leaseFor time.Duration, preserveOrder bool) ([]*OutboxEvent, error)
	ExtendLease(ctx context.Context, owner string, ids []string, leaseFor time.Duration) error
	// ReleaseLease returns unprocessed events to PENDING (used on shutdown).
	ReleaseLease(ctx context.Context, owner string, ids []string) error
	MarkPublished(ctx context.Context, id, owner string) error
	MarkFailed(ctx context.Context, id, owner, errStr string, nextRetry time.Time) error
	MoveToDLQ(ctx context.Context, id, owner, reason string) error
}

// Purger is used by the Cleaner.
type Purger interface {
	DeletePublishedBefore(ctx context.Context, before time.Time, limit int) (int64, error)
}

// StatsReader is used by the Worker to export backlog metrics.
type StatsReader interface {
	Stats(ctx context.Context) (Stats, error)
}

// Repository is the full surface, for wiring convenience and tests.
type Repository interface {
	Writer
	Claimer
	Purger
	StatsReader
}
