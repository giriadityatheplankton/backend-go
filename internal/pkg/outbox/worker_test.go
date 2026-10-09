package outbox_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"backend-go/internal/pkg/outbox"

	"github.com/stretchr/testify/assert"
)

type mockDistributedLock struct {
	mu     sync.Mutex
	locked bool
}

func (m *mockDistributedLock) Acquire(ctx context.Context, key string, ttl time.Duration) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.locked {
		return "", outbox.ErrLockFailed
	}
	m.locked = true
	return "token-123", nil
}

func (m *mockDistributedLock) Release(ctx context.Context, key string, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.locked = false
	return nil
}

func (m *mockDistributedLock) Extend(ctx context.Context, key string, token string, ttl time.Duration) error {
	return nil
}

type mockPublisher struct {
	mu        sync.Mutex
	published []string
	shouldFail bool
}

func (p *mockPublisher) Publish(ctx context.Context, topic string, payload []byte, headers map[string]string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.shouldFail {
		return errors.New("connection broken")
	}
	p.published = append(p.published, topic)
	return nil
}

func TestOutboxWorkerSuccess(t *testing.T) {
	repo := outbox.NewMemoryRepository()
	lock := &mockDistributedLock{}
	pub := &mockPublisher{}

	event := &outbox.OutboxEvent{
		ID:            "evt-1",
		AggregateType: "User",
		AggregateID:   "101",
		EventType:     "user.created",
		Payload:       []byte(`{"id":101}`),
		Partition:     0,
		Status:        outbox.StatusPending,
		CreatedAt:     time.Now(),
	}
	_ = repo.Save(context.Background(), event)

	cfg := outbox.WorkerConfig{
		PartitionCount: 1,
		BatchSize:      10,
		PollInterval:   20 * time.Millisecond,
		LockTTL:        1 * time.Second,
		MaxRetries:     3,
		BaseBackoff:    10 * time.Millisecond,
	}

	worker := outbox.NewWorker(cfg, repo, lock, pub)
	ctx, cancel := context.WithCancel(context.Background())

	worker.Start(ctx)
	time.Sleep(100 * time.Millisecond)
	cancel()
	worker.Stop()

	pub.mu.Lock()
	assert.Len(t, pub.published, 1)
	assert.Equal(t, "user.created", pub.published[0])
	pub.mu.Unlock()
}

func TestOutboxWorkerDLQ(t *testing.T) {
	repo := outbox.NewMemoryRepository()
	lock := &mockDistributedLock{}
	pub := &mockPublisher{shouldFail: true}

	event := &outbox.OutboxEvent{
		ID:            "evt-fail",
		AggregateType: "User",
		AggregateID:   "102",
		EventType:     "user.failed",
		Payload:       []byte(`{"id":102}`),
		Partition:     0,
		Status:        outbox.StatusPending,
		RetryCount:    2,
		MaxRetries:     3,
		CreatedAt:     time.Now(),
	}
	_ = repo.Save(context.Background(), event)

	cfg := outbox.WorkerConfig{
		PartitionCount: 1,
		BatchSize:      10,
		PollInterval:   20 * time.Millisecond,
		LockTTL:        1 * time.Second,
		MaxRetries:     3,
		BaseBackoff:    5 * time.Millisecond,
	}

	worker := outbox.NewWorker(cfg, repo, lock, pub)
	ctx, cancel := context.WithCancel(context.Background())

	worker.Start(ctx)
	time.Sleep(100 * time.Millisecond)
	cancel()
	worker.Stop()

	// Should be moved to DLQ
	events, _ := repo.FetchPendingBatch(context.Background(), 0, 10)
	assert.Empty(t, events)
}
