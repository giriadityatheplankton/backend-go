package idempotency

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

type memEntry struct {
	token     string
	rec       *Record
	expiresAt time.Time
}

// MemoryStore is an in-process Store for tests and single-instance development.
// It follows the same contract as RedisStore (see storetest).
type MemoryStore struct {
	mu      sync.Mutex
	entries map[string]*memEntry
	now     func() time.Time
}

// NewMemoryStore creates an empty MemoryStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{entries: make(map[string]*memEntry), now: time.Now}
}

func (m *MemoryStore) live(key string) *memEntry {
	e, ok := m.entries[key]
	if !ok {
		return nil
	}
	if !m.now().Before(e.expiresAt) {
		delete(m.entries, key)
		return nil
	}
	return e
}

func (m *MemoryStore) sweepLocked() {
	if len(m.entries) < 1024 {
		return
	}
	now := m.now()
	for k, e := range m.entries {
		if !now.Before(e.expiresAt) {
			delete(m.entries, k)
		}
	}
}

func (m *MemoryStore) Acquire(_ context.Context, key, fingerprint string, ttl time.Duration) (Lease, *Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sweepLocked()
	if e := m.live(key); e != nil {
		return Lease{}, e.rec.Clone(), nil
	}
	tok := newToken()
	m.entries[key] = &memEntry{
		token:     tok,
		rec:       &Record{State: StateStarted, Fingerprint: fingerprint},
		expiresAt: m.now().Add(ttl),
	}
	return Lease{Key: key, Token: tok}, nil, nil
}

func (m *MemoryStore) owned(l Lease) *memEntry {
	e := m.live(l.Key)
	if e == nil || e.token != l.Token || e.rec.State != StateStarted {
		return nil
	}
	return e
}

func (m *MemoryStore) Complete(_ context.Context, l Lease, rec *Record, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.owned(l)
	if e == nil {
		return ErrLeaseLost
	}
	cp := rec.Clone()
	cp.State = StateCompleted
	if cp.Fingerprint == "" {
		cp.Fingerprint = e.rec.Fingerprint
	}
	e.rec = cp
	e.expiresAt = m.now().Add(ttl)
	return nil
}

func (m *MemoryStore) Release(_ context.Context, l Lease) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.owned(l) == nil {
		return ErrLeaseLost
	}
	delete(m.entries, l.Key)
	return nil
}

func (m *MemoryStore) Extend(_ context.Context, l Lease, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.owned(l)
	if e == nil {
		return ErrLeaseLost
	}
	e.expiresAt = m.now().Add(ttl)
	return nil
}

func newToken() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
