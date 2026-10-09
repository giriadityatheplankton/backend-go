package outbox

import (
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
