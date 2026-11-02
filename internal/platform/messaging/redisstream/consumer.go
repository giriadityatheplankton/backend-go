package redisstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"backend-go/internal/platform/messaging"
	"backend-go/internal/platform/observability/requestid"
	"backend-go/internal/platform/observability/telemetry"

	"github.com/redis/go-redis/v9"
)

// ConsumerConfig configures a consumer-group reader.
type ConsumerConfig struct {
	Stream string // full stream key
	Group  string // one group per service (and per logical subscriber)
	// Name identifies this consumer in the group; defaults to $HOSTNAME (the pod name).
	Name string
	// StartID is where a NEW group starts reading ("0" = all retained, "$" = only new).
	StartID string

	BatchSize int           // XREADGROUP COUNT
	Block     time.Duration // XREADGROUP BLOCK
	// Workers bounds concurrent handlers (backpressure). Use 1 when strict order matters.
	Workers        int
	HandlerTimeout time.Duration
	// MaxDeliveries is the total attempts before a message is dead-lettered.
	MaxDeliveries int
	// ClaimIdle is both the crash-visibility timeout (a dead pod's messages are
	// reclaimed after this) and the minimum delay between retries of a failing message.
	ClaimIdle     time.Duration
	ClaimInterval time.Duration
	DLQStream     string // defaults to Stream + ".dlq"
	DLQMaxLen     int64
	StatsInterval time.Duration
}

func (c *ConsumerConfig) normalise() error {
	if c.Stream == "" || c.Group == "" {
		return errors.New("redisstream: Stream and Group are required")
	}
	if c.Name == "" {
		c.Name, _ = os.Hostname()
		if c.Name == "" {
			c.Name = "consumer"
		}
	}
	if c.StartID == "" {
		c.StartID = "0"
	}
	if c.BatchSize <= 0 {
		c.BatchSize = 32
	}
	if c.Block <= 0 {
		c.Block = 2 * time.Second
	}
	if c.Workers <= 0 {
		c.Workers = 8
	}
	if c.HandlerTimeout <= 0 {
		c.HandlerTimeout = 30 * time.Second
	}
	if c.MaxDeliveries <= 0 {
		c.MaxDeliveries = 5
	}
	if c.ClaimIdle <= 0 {
		c.ClaimIdle = time.Minute
	}
	if c.ClaimInterval <= 0 {
		c.ClaimInterval = c.ClaimIdle / 2
	}
	if c.DLQStream == "" {
		c.DLQStream = c.Stream + ".dlq"
	}
	if c.DLQMaxLen == 0 {
		c.DLQMaxLen = 100_000
	}
	if c.StatsInterval <= 0 {
		c.StatsInterval = 15 * time.Second
	}
	return nil
}

type pendingObserver interface {
	SetConsumerPending(stream string, n float64)
}

// Consumer reads a stream through a consumer group. It implements messaging.Consumer.
type Consumer struct {
	c       Client
	cfg     ConsumerConfig
	handler messaging.Handler
	obs     messaging.Observer
}

var _ messaging.Consumer = (*Consumer)(nil)

// NewConsumer creates a consumer. obs may be nil.
func NewConsumer(c Client, cfg ConsumerConfig, handler messaging.Handler, obs messaging.Observer) (*Consumer, error) {
	if err := cfg.normalise(); err != nil {
		return nil, err
	}
	if handler == nil {
		return nil, errors.New("redisstream: handler is required")
	}
	return &Consumer{c: c, cfg: cfg, handler: handler, obs: obs}, nil
}

// Run blocks until ctx is cancelled, then waits for in-flight handlers. Messages
// that were not acknowledged stay pending and are reclaimed by another pod.
func (c *Consumer) Run(ctx context.Context) error {
	if err := c.ensureGroup(ctx); err != nil {
		return err
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, c.cfg.Workers)

	wg.Add(2)
	go func() { defer wg.Done(); c.reclaimLoop(ctx, &wg, sem) }()
	go func() { defer wg.Done(); c.statsLoop(ctx) }()

	c.readLoop(ctx, &wg, sem)
	wg.Wait()
	return nil
}

func (c *Consumer) ensureGroup(ctx context.Context) error {
	err := c.c.XGroupCreateMkStream(ctx, c.cfg.Stream, c.cfg.Group, c.cfg.StartID).Err()
	if err != nil && !strings.HasPrefix(err.Error(), "BUSYGROUP") {
		return fmt.Errorf("redisstream: create group %s/%s: %w", c.cfg.Stream, c.cfg.Group, err)
	}
	return nil
}

func (c *Consumer) readLoop(ctx context.Context, wg *sync.WaitGroup, sem chan struct{}) {
	id := "0" // first drain our own pending entries, then switch to new messages
	backoff := 100 * time.Millisecond
	for ctx.Err() == nil {
		streams, err := c.c.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group: c.cfg.Group, Consumer: c.cfg.Name, Streams: []string{c.cfg.Stream, id},
			Count: int64(c.cfg.BatchSize), Block: c.cfg.Block,
		}).Result()
		switch {
		case errors.Is(err, redis.Nil):
			if id == "0" {
				id = ">"
			}
			backoff = 100 * time.Millisecond
			continue
		case err != nil:
			if ctx.Err() != nil {
				return
			}
			slog.ErrorContext(ctx, "Redis stream read failed", "stream", c.cfg.Stream, "error", err)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			if backoff < 5*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = 100 * time.Millisecond
		var msgs []redis.XMessage
		for _, s := range streams {
			msgs = append(msgs, s.Messages...)
		}
		if id == "0" && len(msgs) == 0 {
			id = ">"
			continue
		}
		if id == "0" {
			// Re-delivered entries: attempt counts come from XPENDING.
			c.dispatch(ctx, wg, sem, msgs, true)
			id = msgs[len(msgs)-1].ID
			continue
		}
		c.dispatch(ctx, wg, sem, msgs, false)
	}
}

func (c *Consumer) reclaimLoop(ctx context.Context, wg *sync.WaitGroup, sem chan struct{}) {
	t := time.NewTicker(c.cfg.ClaimInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		start := "0-0"
		for ctx.Err() == nil {
			msgs, next, err := c.c.XAutoClaim(ctx, &redis.XAutoClaimArgs{
				Stream: c.cfg.Stream, Group: c.cfg.Group, Consumer: c.cfg.Name,
				MinIdle: c.cfg.ClaimIdle, Start: start, Count: int64(c.cfg.BatchSize),
			}).Result()
			if err != nil {
				if ctx.Err() == nil {
					slog.ErrorContext(ctx, "Redis stream autoclaim failed", "stream", c.cfg.Stream, "error", err)
				}
				break
			}
			c.dispatch(ctx, wg, sem, msgs, true)
			if next == "0-0" || next == "" || len(msgs) == 0 {
				break
			}
			start = next
		}
	}
}

func (c *Consumer) statsLoop(ctx context.Context) {
	po, ok := c.obs.(pendingObserver)
	if !ok {
		return
	}
	t := time.NewTicker(c.cfg.StatsInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if p, err := c.c.XPending(ctx, c.cfg.Stream, c.cfg.Group).Result(); err == nil {
				po.SetConsumerPending(c.cfg.Stream, float64(p.Count))
			}
		}
	}
}

func (c *Consumer) dispatch(ctx context.Context, wg *sync.WaitGroup, sem chan struct{}, msgs []redis.XMessage, redelivered bool) {
	for _, m := range msgs {
		select {
		case sem <- struct{}{}: // blocks when all workers are busy: backpressure
		case <-ctx.Done():
			return
		}
		attempt := 1
		if redelivered {
			attempt = c.deliveryCount(ctx, m.ID)
		}
		wg.Add(1)
		go func(m redis.XMessage, attempt int) {
			defer func() { <-sem; wg.Done() }()
			c.handle(ctx, m, attempt)
		}(m, attempt)
	}
}

func (c *Consumer) deliveryCount(ctx context.Context, id string) int {
	res, err := c.c.XPendingExt(ctx, &redis.XPendingExtArgs{
		Stream: c.cfg.Stream, Group: c.cfg.Group, Start: id, End: id, Count: 1,
	}).Result()
	if err != nil || len(res) == 0 {
		return 1
	}
	return int(res[0].RetryCount)
}

func (c *Consumer) handle(ctx context.Context, x redis.XMessage, attempt int) {
	// Detach from shutdown: finish what we started, but never hang forever.
	hctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.cfg.HandlerTimeout)
	defer cancel()

	msg, err := decode(x, c.cfg.Stream, attempt)
	if err != nil {
		c.deadLetter(hctx, x, attempt, err, messaging.ResultPoison)
		return
	}
	if id := msg.Headers[requestid.Header]; requestid.Valid(id) {
		hctx = requestid.With(hctx, id)
	}
	if tp := msg.Headers[telemetry.HeaderTraceParent]; tp != "" {
		hctx = telemetry.ContextWithSpan(hctx, telemetry.ExtractMap(msg.Headers))
	}

	herr := c.safeHandle(hctx, msg)
	switch {
	case herr == nil:
		if err := c.ack(hctx, x.ID); err != nil {
			slog.ErrorContext(hctx, "Failed to ack message; it will be redelivered and de-duplicated", "id", msg.ID, "error", err)
			return
		}
		c.observe(messaging.ResultOK)
	case messaging.IsPermanent(herr):
		c.deadLetter(hctx, x, attempt, herr, messaging.ResultPoison)
	case attempt >= c.cfg.MaxDeliveries:
		c.deadLetter(hctx, x, attempt, herr, messaging.ResultDLQ)
	default:
		c.observe(messaging.ResultRetry)
		slog.WarnContext(hctx, "Message handler failed; will be redelivered", "id", msg.ID, "type", msg.Type, "attempt", attempt, "error", herr)
	}
}

func (c *Consumer) safeHandle(ctx context.Context, m messaging.Message) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("handler panic: %v", r)
			slog.ErrorContext(ctx, "Message handler panicked", "id", m.ID, "panic", r)
		}
	}()
	return c.handler(ctx, m)
}

func (c *Consumer) ack(ctx context.Context, id string) error {
	return c.c.XAck(ctx, c.cfg.Stream, c.cfg.Group, id).Err()
}

func (c *Consumer) deadLetter(ctx context.Context, x redis.XMessage, attempt int, cause error, result string) {
	values := make(map[string]any, len(x.Values)+4)
	for k, v := range x.Values {
		values[k] = v
	}
	values["dlq_error"] = cause.Error()
	values["dlq_attempts"] = attempt
	values["dlq_original_id"] = x.ID
	values["dlq_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	args := &redis.XAddArgs{Stream: c.cfg.DLQStream, Values: values}
	if c.cfg.DLQMaxLen > 0 {
		args.MaxLen, args.Approx = c.cfg.DLQMaxLen, true
	}
	if err := c.c.XAdd(ctx, args).Err(); err != nil {
		slog.ErrorContext(ctx, "Failed to write DLQ entry; message stays pending", "stream", c.cfg.Stream, "error", err)
		return // do not ack: never lose a message
	}
	if err := c.ack(ctx, x.ID); err != nil {
		slog.ErrorContext(ctx, "Failed to ack dead-lettered message", "error", err)
		return
	}
	c.observe(result)
	slog.ErrorContext(ctx, "Message moved to DLQ", "stream", c.cfg.Stream, "dlq", c.cfg.DLQStream, "attempts", attempt, "error", cause)
}

func (c *Consumer) observe(result string) {
	if c.obs != nil {
		c.obs.IncConsumer(c.cfg.Stream, result)
	}
}

func decode(x redis.XMessage, stream string, attempt int) (messaging.Message, error) {
	get := func(k string) string {
		switch v := x.Values[k].(type) {
		case string:
			return v
		case []byte:
			return string(v)
		}
		return ""
	}
	m := messaging.Message{
		ID: get(fieldID), Type: get(fieldType), Key: get(fieldKey),
		Payload: []byte(get(fieldPayload)), Stream: stream, Attempt: attempt,
	}
	if m.ID == "" {
		return m, errors.New("message has no event ID")
	}
	if h := get(fieldHeaders); h != "" {
		if err := json.Unmarshal([]byte(h), &m.Headers); err != nil {
			return m, fmt.Errorf("decode headers: %w", err)
		}
	}
	return m, nil
}
