package redisstream_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"backend-go/internal/platform/messaging"
	"backend-go/internal/platform/messaging/redisstream"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setup(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	mr := miniredis.RunT(t)
	c := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = c.Close() })
	return mr, c
}

func baseCfg() redisstream.ConsumerConfig {
	return redisstream.ConsumerConfig{
		Stream: "orders", Group: "svc", Name: "pod-1", Block: 50 * time.Millisecond,
		ClaimIdle: 40 * time.Millisecond, ClaimInterval: 20 * time.Millisecond,
		MaxDeliveries: 3, Workers: 4, HandlerTimeout: time.Second,
	}
}

func runConsumer(t *testing.T, c redisstream.Client, cfg redisstream.ConsumerConfig, h messaging.Handler, obs messaging.Observer) (stop func()) {
	cons, err := redisstream.NewConsumer(c, cfg, h, obs)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = cons.Run(ctx); close(done) }()
	return func() { cancel(); <-done }
}

type obsRecorder struct {
	mu sync.Mutex
	n  map[string]int
}

func (o *obsRecorder) IncConsumer(_, result string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.n == nil {
		o.n = map[string]int{}
	}
	o.n[result]++
}
func (o *obsRecorder) get(r string) int { o.mu.Lock(); defer o.mu.Unlock(); return o.n[r] }

func publish(t *testing.T, c *redis.Client, id string) {
	p := redisstream.NewPublisher(c, redisstream.PublisherConfig{MaxLen: 1000})
	require.NoError(t, p.Publish(context.Background(), "orders", messaging.Message{
		ID: id, Type: "order.created", Key: "agg-1", Payload: []byte(`{"a":1}`), Headers: map[string]string{"X-Request-ID": "r1"},
	}))
}

func pendingCount(t *testing.T, c *redis.Client) int64 {
	p, err := c.XPending(context.Background(), "orders", "svc").Result()
	if err != nil {
		return 0
	}
	return p.Count
}

func TestPublishConsumeAck(t *testing.T) {
	_, c := setup(t)
	got := make(chan messaging.Message, 1)
	obs := &obsRecorder{}
	stop := runConsumer(t, c, baseCfg(), func(_ context.Context, m messaging.Message) error { got <- m; return nil }, obs)
	defer stop()

	publish(t, c, "evt-1")

	select {
	case m := <-got:
		assert.Equal(t, "evt-1", m.ID)
		assert.Equal(t, "order.created", m.Type)
		assert.Equal(t, "agg-1", m.Key)
		assert.Equal(t, []byte(`{"a":1}`), m.Payload)
		assert.Equal(t, "r1", m.Headers["X-Request-ID"])
		assert.Equal(t, 1, m.Attempt)
	case <-time.After(2 * time.Second):
		t.Fatal("no message")
	}
	require.Eventually(t, func() bool { return pendingCount(t, c) == 0 && obs.get("ok") == 1 }, 2*time.Second, 10*time.Millisecond)
}

func TestFailingHandlerIsRetriedThenDeadLettered(t *testing.T) {
	_, c := setup(t)
	var calls int32
	obs := &obsRecorder{}
	stop := runConsumer(t, c, baseCfg(), func(context.Context, messaging.Message) error {
		atomic.AddInt32(&calls, 1)
		return errors.New("downstream down")
	}, obs)
	defer stop()

	publish(t, c, "evt-bad")

	require.Eventually(t, func() bool { return obs.get("dlq") == 1 }, 5*time.Second, 20*time.Millisecond)
	assert.Equal(t, int32(3), atomic.LoadInt32(&calls), "exactly MaxDeliveries attempts")
	assert.Equal(t, int64(0), pendingCount(t, c), "dead-lettered message is acked")

	dlq, err := c.XRange(context.Background(), "orders.dlq", "-", "+").Result()
	require.NoError(t, err)
	require.Len(t, dlq, 1)
	assert.Equal(t, "evt-bad", dlq[0].Values["id"])
	assert.Contains(t, dlq[0].Values["dlq_error"], "downstream down")
}

func TestPermanentErrorSkipsRetries(t *testing.T) {
	_, c := setup(t)
	var calls int32
	obs := &obsRecorder{}
	stop := runConsumer(t, c, baseCfg(), func(context.Context, messaging.Message) error {
		atomic.AddInt32(&calls, 1)
		return messaging.Permanent(errors.New("schema invalid"))
	}, obs)
	defer stop()

	publish(t, c, "evt-poison")

	require.Eventually(t, func() bool { return obs.get("poison") == 1 }, 3*time.Second, 20*time.Millisecond)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls))
}

func TestUndecodableMessageGoesToDLQ(t *testing.T) {
	_, c := setup(t)
	obs := &obsRecorder{}
	stop := runConsumer(t, c, baseCfg(), func(context.Context, messaging.Message) error { return nil }, obs)
	defer stop()

	require.NoError(t, c.XAdd(context.Background(), &redis.XAddArgs{Stream: "orders", Values: map[string]any{"garbage": "x"}}).Err())

	require.Eventually(t, func() bool { return obs.get("poison") == 1 }, 3*time.Second, 20*time.Millisecond)
}

func TestMessagesOfDeadConsumerAreReclaimed(t *testing.T) {
	_, c := setup(t)
	ctx := context.Background()
	require.NoError(t, c.XGroupCreateMkStream(ctx, "orders", "svc", "0").Err())
	publish(t, c, "evt-orphan")
	// "pod-dead" reads the message and crashes without acking.
	res, err := c.XReadGroup(ctx, &redis.XReadGroupArgs{Group: "svc", Consumer: "pod-dead", Streams: []string{"orders", ">"}, Count: 1}).Result()
	require.NoError(t, err)
	require.Len(t, res[0].Messages, 1)

	got := make(chan messaging.Message, 1)
	stop := runConsumer(t, c, baseCfg(), func(_ context.Context, m messaging.Message) error { got <- m; return nil }, nil)
	defer stop()

	select {
	case m := <-got:
		assert.Equal(t, "evt-orphan", m.ID)
		assert.GreaterOrEqual(t, m.Attempt, 2, "a reclaimed message counts as a redelivery")
	case <-time.After(3 * time.Second):
		t.Fatal("orphaned message was never reclaimed")
	}
}

func TestWorkerLimitBoundsConcurrency(t *testing.T) {
	_, c := setup(t)
	cfg := baseCfg()
	cfg.Workers = 2
	var cur, max int32
	release := make(chan struct{})
	stop := runConsumer(t, c, cfg, func(context.Context, messaging.Message) error {
		n := atomic.AddInt32(&cur, 1)
		for {
			m := atomic.LoadInt32(&max)
			if n <= m || atomic.CompareAndSwapInt32(&max, m, n) {
				break
			}
		}
		<-release
		atomic.AddInt32(&cur, -1)
		return nil
	}, nil)
	defer stop()
	for i := 0; i < 6; i++ {
		publish(t, c, string(rune('a'+i)))
	}
	time.Sleep(300 * time.Millisecond)
	assert.LessOrEqual(t, atomic.LoadInt32(&max), int32(2))
	close(release)
}

func TestGracefulShutdownWaitsForInFlight(t *testing.T) {
	_, c := setup(t)
	started := make(chan struct{})
	var finished int32
	cons, err := redisstream.NewConsumer(c, baseCfg(), func(context.Context, messaging.Message) error {
		close(started)
		time.Sleep(150 * time.Millisecond)
		atomic.StoreInt32(&finished, 1)
		return nil
	}, nil)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = cons.Run(ctx); close(done) }()

	publish(t, c, "evt-slow")
	<-started
	cancel()
	<-done
	assert.Equal(t, int32(1), atomic.LoadInt32(&finished), "Run must not return before in-flight handlers finish")
}

func TestPublisherTrimsAndRequiresID(t *testing.T) {
	_, c := setup(t)
	p := redisstream.NewPublisher(c, redisstream.PublisherConfig{Prefix: "svc:", MaxLen: 10})
	err := p.Publish(context.Background(), "orders", messaging.Message{})
	require.Error(t, err)
	assert.True(t, messaging.IsPermanent(err))
	require.NoError(t, p.Publish(context.Background(), "orders", messaging.Message{ID: "1", Payload: []byte("x")}))
	n, _ := c.XLen(context.Background(), "svc:orders").Result()
	assert.Equal(t, int64(1), n)
}
