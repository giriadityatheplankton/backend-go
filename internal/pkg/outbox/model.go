package outbox

import (
	"hash/crc32"
	"time"
)

type EventStatus string

const (
	StatusPending    EventStatus = "PENDING"
	StatusProcessing EventStatus = "PROCESSING"
	StatusPublished  EventStatus = "PUBLISHED"
	StatusFailed     EventStatus = "FAILED"
	StatusDLQ        EventStatus = "DLQ"
)

// OutboxEvent represents a transactional outbox record.
type OutboxEvent struct {
	// ID must be deterministic from the aggregate (e.g. "order-created-<orderID>")
	// so re-enqueueing (reconciliation) and consumer de-duplication are safe.
	ID            string            `json:"id"`
	AggregateType string            `json:"aggregate_type"`
	AggregateID   string            `json:"aggregate_id"`
	EventType     string            `json:"event_type"`
	Payload       []byte            `json:"payload"`
	Headers       map[string]string `json:"headers"`
	Partition     int               `json:"partition"`
	Status        EventStatus       `json:"status"`
	RetryCount    int               `json:"retry_count"`
	MaxRetries    int               `json:"max_retries"`
	NextRetryAt   time.Time         `json:"next_retry_at"`
	LastError     string            `json:"last_error,omitempty"`
	LockedBy      string            `json:"locked_by,omitempty"`
	LockedUntil   time.Time         `json:"locked_until,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
}

// DLQEvent represents an event permanently diverted to the Dead Letter Queue.
type DLQEvent struct {
	ID              string            `json:"id"`
	OriginalEventID string            `json:"original_event_id"`
	EventType       string            `json:"event_type"`
	Payload         []byte            `json:"payload"`
	Headers         map[string]string `json:"headers"`
	FailureReason   string            `json:"failure_reason"`
	RetryAttempts   int               `json:"retry_attempts"`
	FailedAt        time.Time         `json:"failed_at"`
}

// PartitionFor maps an aggregate ID to a stable partition in [0, n).
func PartitionFor(aggregateID string, n int) int {
	if n <= 1 {
		return 0
	}
	return int(crc32.ChecksumIEEE([]byte(aggregateID)) % uint32(n)) //nolint:gosec // n > 1 and result < n
}

// Stats summarises the backlog for alerting.
type Stats struct {
	Unpublished      int64
	OldestPendingAge time.Duration
}
