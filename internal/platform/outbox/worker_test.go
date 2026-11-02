package outbox_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"backend-go/internal/platform/messaging"
	"backend-go/internal/platform/outbox"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakePublisher struct {
	mu     sync.Mutex
	msgs   []messaging.Message
	topics []string
	err    error
}

func (p *fakePublisher) Publish(_ context.Context, topic string, m messaging.Message) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	p.msgs = append(p.msgs, m)
	p.topics = append(p.topics, topic)
	return nil
}

func cfg() outbox.WorkerConfig {
	return outbox.WorkerConfig{
		PartitionCount: 1, BatchSize: 10, PollInterval: 5 * time.Millisecond,
		LeaseDuration: time.Second, MaxRetries: 3, BaseBackoff: time.Millisecond, MaxBackoff: 5 * time.Millisecond,
		PreserveOrder: true, Owner: "w1",
	}
}

func runFor(w *outbox.Worker, d time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	_ = w.Run(ctx)
}

func ev(id, agg string, at time.Time) *outbox.OutboxEvent {
	return &outbox.OutboxEvent{ID: id, AggregateType: "order", AggregateID: agg, EventType: "order.created",
		Payload: []byte(`{}`), Status: outbox.StatusPending, CreatedAt: at}
}

func TestWorker_PublishesWithDeterministicIDAndKey(t *testing.T) {
	repo := outbox.NewMemoryRepository()
	pub := &fakePublisher{}
	require.NoError(t, repo.Save(context.Background(), ev("order-created-1", "1", time.Now())))

	runFor(outbox.NewWorker(cfg(), repo, pub, outbox.ByAggregateType("events."), nil), 100*time.Millisecond)

	require.Len(t, pub.msgs, 1)
	assert.Equal(t, "order-created-1", pub.msgs[0].ID)
	assert.Equal(t, "1", pub.msgs[0].Key)
	assert.Equal(t, "order.created", pub.msgs[0].Type)
	assert.Equal(t, "events.order", pub.topics[0])
	got, _ := repo.Get("order-created-1")
	assert.Equal(t, outbox.StatusPublished, got.Status)
}

func TestWorker_PreservesPerAggregateOrderDespiteRetry(t *testing.T) {
	repo := outbox.NewMemoryRepository()
	now := time.Now()
	_ = repo.Save(context.Background(), ev("e1", "A", now))
	_ = repo.Save(context.Background(), ev("e2", "A", now.Add(time.Millisecond)))
	_ = repo.Save(context.Background(), ev("x1", "B", now.Add(2*time.Millisecond)))

	flaky := &flakyOnce{failID: "e1"}
	runFor(outbox.NewWorker(cfg(), repo, flaky, nil, nil), 300*time.Millisecond)

	order := flaky.order()
	idx := func(id string) int {
		for i, v := range order {
			if v == id {
				return i
			}
		}
		return -1
	}
	require.NotEqual(t, -1, idx("e1"))
	require.NotEqual(t, -1, idx("e2"))
	assert.Less(t, idx("e1"), idx("e2"), "e2 must not overtake the failed e1: %v", order)
	assert.NotEqual(t, -1, idx("x1"), "other aggregates are not blocked")
}

type flakyOnce struct {
	mu     sync.Mutex
	failID string
	failed bool
	seen   []string
}

func (f *flakyOnce) Publish(_ context.Context, _ string, m messaging.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m.ID == f.failID && !f.failed {
		f.failed = true
		return errors.New("broker down")
	}
	f.seen = append(f.seen, m.ID)
	return nil
}
func (f *flakyOnce) order() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seen...)
}

func TestWorker_MovesToDLQAfterMaxRetries(t *testing.T) {
	repo := outbox.NewMemoryRepository()
	e := ev("bad", "1", time.Now())
	e.MaxRetries = 2
	_ = repo.Save(context.Background(), e)

	runFor(outbox.NewWorker(cfg(), repo, &fakePublisher{err: errors.New("down")}, nil, nil), 300*time.Millisecond)

	got, _ := repo.Get("bad")
	assert.Equal(t, outbox.StatusDLQ, got.Status)
	assert.Len(t, repo.DLQ(), 1)
}

func TestWorker_PermanentErrorGoesStraightToDLQ(t *testing.T) {
	repo := outbox.NewMemoryRepository()
	_ = repo.Save(context.Background(), ev("poison", "1", time.Now()))
	pub := &fakePublisher{err: messaging.Permanent(errors.New("schema invalid"))}
	runFor(outbox.NewWorker(cfg(), repo, pub, nil, nil), 100*time.Millisecond)
	got, _ := repo.Get("poison")
	assert.Equal(t, outbox.StatusDLQ, got.Status)
	assert.Equal(t, 1, got.RetryCount)
}

func TestRepository_SaveDoesNotResurrectPublishedEvent(t *testing.T) {
	repo := outbox.NewMemoryRepository()
	ctx := context.Background()
	_ = repo.Save(ctx, ev("e1", "A", time.Now()))
	claimed, _ := repo.Claim(ctx, 0, 10, "w1", time.Minute, false)
	require.Len(t, claimed, 1)
	require.NoError(t, repo.MarkPublished(ctx, "e1", "w1"))

	require.NoError(t, repo.Save(ctx, ev("e1", "A", time.Now()))) // e.g. reconciler re-enqueue

	got, _ := repo.Get("e1")
	assert.Equal(t, outbox.StatusPublished, got.Status, "must stay PUBLISHED, not reset to PENDING")
}

func TestRepository_ExpiredLeaseIsReclaimedAndOldOwnerIsFenced(t *testing.T) {
	repo := outbox.NewMemoryRepository()
	ctx := context.Background()
	_ = repo.Save(ctx, ev("e1", "A", time.Now()))

	a, _ := repo.Claim(ctx, 0, 10, "podA", 20*time.Millisecond, false)
	require.Len(t, a, 1)
	time.Sleep(40 * time.Millisecond) // podA crashed; lease expires
	b, _ := repo.Claim(ctx, 0, 10, "podB", time.Minute, false)
	require.Len(t, b, 1, "a PROCESSING event with an expired lease must be reclaimed")

	assert.ErrorIs(t, repo.MarkPublished(ctx, "e1", "podA"), outbox.ErrLeaseLost)
	assert.NoError(t, repo.MarkPublished(ctx, "e1", "podB"))
}

func TestRepository_ConcurrentClaimsNeverOverlap(t *testing.T) {
	repo := outbox.NewMemoryRepository()
	ctx := context.Background()
	for i := 0; i < 50; i++ {
		_ = repo.Save(ctx, ev(string(rune('a'+i%26))+string(rune('A'+i/26)), string(rune('a'+i%26))+string(rune('A'+i/26)), time.Now()))
	}
	var mu sync.Mutex
	seen := map[string]int{}
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			got, _ := repo.Claim(ctx, 0, 10, string(rune('0'+w)), time.Minute, false)
			mu.Lock()
			for _, e := range got {
				seen[e.ID]++
			}
			mu.Unlock()
		}(w)
	}
	wg.Wait()
	for id, n := range seen {
		assert.Equal(t, 1, n, "event %s claimed %d times", id, n)
	}
}

func TestBackoffIsExponentialCappedAndJittered(t *testing.T) {
	for attempt, want := range map[int]time.Duration{1: time.Second, 2: 2 * time.Second, 3: 4 * time.Second, 20: 10 * time.Second} {
		for i := 0; i < 20; i++ {
			d := outbox.Backoff(time.Second, 10*time.Second, attempt)
			assert.GreaterOrEqual(t, d, time.Duration(float64(want)*0.8)-1)
			assert.LessOrEqual(t, d, time.Duration(float64(want)*1.2)+1)
		}
	}
}

func TestPartitionFor(t *testing.T) {
	assert.Equal(t, outbox.PartitionFor("x", 8), outbox.PartitionFor("x", 8))
	assert.Equal(t, 0, outbox.PartitionFor("x", 1))
	assert.Less(t, outbox.PartitionFor("anything", 5), 5)
}
