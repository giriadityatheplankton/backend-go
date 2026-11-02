package outbox

import (
	"context"
	"sort"
	"sync"
	"time"
)

// MemoryRepository is a thread-safe in-memory Repository for tests. It mirrors
// the PostgresRepository semantics (idempotent Save, fenced updates, lease reclaim).
type MemoryRepository struct {
	mu     sync.Mutex
	events map[string]*OutboxEvent
	dlq    map[string]*DLQEvent
	now    func() time.Time
}

var _ Repository = (*MemoryRepository)(nil)

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{events: map[string]*OutboxEvent{}, dlq: map[string]*DLQEvent{}, now: time.Now}
}

func (m *MemoryRepository) Save(_ context.Context, ev *OutboxEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.events[ev.ID]; exists {
		return nil // ON CONFLICT DO NOTHING
	}
	cp := *ev
	if cp.Status == "" {
		cp.Status = StatusPending
	}
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = m.now()
	}
	if cp.UpdatedAt.IsZero() {
		cp.UpdatedAt = cp.CreatedAt
	}
	m.events[cp.ID] = &cp
	return nil
}

// Get returns a copy of an event (test helper).
func (m *MemoryRepository) Get(id string) (OutboxEvent, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ev, ok := m.events[id]
	if !ok {
		return OutboxEvent{}, false
	}
	return *ev, true
}

// DLQ returns the DLQ entries (test helper).
func (m *MemoryRepository) DLQ() []DLQEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]DLQEvent, 0, len(m.dlq))
	for _, d := range m.dlq {
		out = append(out, *d)
	}
	return out
}

func (m *MemoryRepository) Claim(_ context.Context, partition, limit int, owner string, leaseFor time.Duration, preserveOrder bool) ([]*OutboxEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	var due []*OutboxEvent
	for _, ev := range m.events {
		if ev.Partition != partition {
			continue
		}
		ready := (ev.Status == StatusPending || ev.Status == StatusFailed) && !ev.NextRetryAt.After(now)
		reclaim := ev.Status == StatusProcessing && ev.LockedUntil.Before(now)
		if ready || reclaim {
			due = append(due, ev)
		}
	}
	sort.Slice(due, func(i, j int) bool {
		if due[i].CreatedAt.Equal(due[j].CreatedAt) {
			return due[i].ID < due[j].ID
		}
		return due[i].CreatedAt.Before(due[j].CreatedAt)
	})
	var out []*OutboxEvent
	for _, ev := range due {
		if len(out) >= limit {
			break
		}
		if preserveOrder && m.hasEarlierOpen(ev) {
			continue
		}
		ev.Status, ev.LockedBy, ev.LockedUntil, ev.UpdatedAt = StatusProcessing, owner, now.Add(leaseFor), now
		cp := *ev
		out = append(out, &cp)
	}
	return out, nil
}

func (m *MemoryRepository) hasEarlierOpen(ev *OutboxEvent) bool {
	for _, o := range m.events {
		if o.ID == ev.ID || o.AggregateID != ev.AggregateID || o.Partition != ev.Partition {
			continue
		}
		open := o.Status == StatusPending || o.Status == StatusFailed || o.Status == StatusProcessing
		earlier := o.CreatedAt.Before(ev.CreatedAt) || (o.CreatedAt.Equal(ev.CreatedAt) && o.ID < ev.ID)
		if open && earlier {
			return true
		}
	}
	return false
}

func (m *MemoryRepository) owned(id, owner string) *OutboxEvent {
	ev := m.events[id]
	if ev == nil || ev.Status != StatusProcessing || ev.LockedBy != owner {
		return nil
	}
	return ev
}

func (m *MemoryRepository) ExtendLease(_ context.Context, owner string, ids []string, leaseFor time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range ids {
		if ev := m.owned(id, owner); ev != nil {
			ev.LockedUntil = m.now().Add(leaseFor)
		}
	}
	return nil
}

func (m *MemoryRepository) ReleaseLease(_ context.Context, owner string, ids []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range ids {
		if ev := m.owned(id, owner); ev != nil {
			ev.Status, ev.LockedBy = StatusPending, ""
		}
	}
	return nil
}

func (m *MemoryRepository) MarkPublished(_ context.Context, id, owner string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	ev := m.owned(id, owner)
	if ev == nil {
		return ErrLeaseLost
	}
	ev.Status, ev.LockedBy, ev.UpdatedAt = StatusPublished, "", m.now()
	return nil
}

func (m *MemoryRepository) MarkFailed(_ context.Context, id, owner, errStr string, nextRetry time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	ev := m.owned(id, owner)
	if ev == nil {
		return ErrLeaseLost
	}
	ev.Status, ev.LockedBy, ev.LastError, ev.NextRetryAt, ev.UpdatedAt = StatusFailed, "", errStr, nextRetry, m.now()
	ev.RetryCount++
	return nil
}

func (m *MemoryRepository) MoveToDLQ(_ context.Context, id, owner, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	ev := m.owned(id, owner)
	if ev == nil {
		return ErrLeaseLost
	}
	ev.Status, ev.LockedBy, ev.LastError, ev.UpdatedAt = StatusDLQ, "", reason, m.now()
	ev.RetryCount++
	m.dlq["dlq_"+ev.ID] = &DLQEvent{ID: "dlq_" + ev.ID, OriginalEventID: ev.ID, EventType: ev.EventType,
		Payload: ev.Payload, Headers: ev.Headers, FailureReason: reason, RetryAttempts: ev.RetryCount, FailedAt: m.now()}
	return nil
}

func (m *MemoryRepository) DeletePublishedBefore(_ context.Context, before time.Time, limit int) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for id, ev := range m.events {
		if ev.Status == StatusPublished && ev.UpdatedAt.Before(before) {
			delete(m.events, id)
			n++
			if limit > 0 && int(n) >= limit {
				break
			}
		}
	}
	return n, nil
}

func (m *MemoryRepository) Stats(_ context.Context) (Stats, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var s Stats
	var oldest time.Time
	for _, ev := range m.events {
		if ev.Status == StatusPending || ev.Status == StatusFailed || ev.Status == StatusProcessing {
			s.Unpublished++
			if oldest.IsZero() || ev.CreatedAt.Before(oldest) {
				oldest = ev.CreatedAt
			}
		}
	}
	if !oldest.IsZero() {
		s.OldestPendingAge = m.now().Sub(oldest)
	}
	return s, nil
}
