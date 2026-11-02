package redisstream

import (
	"context"
	"encoding/json"
	"fmt"

	"backend-go/internal/platform/messaging"

	"github.com/redis/go-redis/v9"
)

// PublisherConfig tunes the stream publisher.
type PublisherConfig struct {
	// Prefix namespaces stream names, e.g. "myservice:".
	Prefix string
	// MaxLen caps each stream (approximate trimming, XADD MAXLEN ~) so memory stays bounded.
	// 0 disables trimming; set it in production.
	MaxLen int64
}

// Publisher writes messages to Redis Streams. It implements messaging.Publisher.
type Publisher struct {
	c   Client
	cfg PublisherConfig
}

var _ messaging.Publisher = (*Publisher)(nil)

func NewPublisher(c Client, cfg PublisherConfig) *Publisher { return &Publisher{c: c, cfg: cfg} }

// StreamName returns the full stream key for a topic.
func StreamName(prefix, topic string) string { return prefix + topic }

func (p *Publisher) Publish(ctx context.Context, topic string, m messaging.Message) error {
	if m.ID == "" {
		return messaging.Permanent(fmt.Errorf("redisstream: message ID is required"))
	}
	headers, err := json.Marshal(m.Headers)
	if err != nil {
		return messaging.Permanent(fmt.Errorf("redisstream: encode headers: %w", err))
	}
	args := &redis.XAddArgs{
		Stream: StreamName(p.cfg.Prefix, topic),
		Values: map[string]any{
			fieldID: m.ID, fieldType: m.Type, fieldKey: m.Key,
			fieldPayload: m.Payload, fieldHeaders: string(headers),
		},
	}
	if p.cfg.MaxLen > 0 {
		args.MaxLen, args.Approx = p.cfg.MaxLen, true
	}
	if err := p.c.XAdd(ctx, args).Err(); err != nil {
		return fmt.Errorf("redisstream: XADD %s: %w", args.Stream, err)
	}
	return nil
}
