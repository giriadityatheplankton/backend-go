// Package redisstream implements messaging.Publisher and messaging.Consumer on
// Redis Streams with consumer groups, crash recovery (XAUTOCLAIM), bounded
// retries and a dead-letter stream.
//
// Durability: run the Redis that hosts streams with appendonly yes and
// appendfsync everysec. A failover can still lose about one second of writes, so
// the Postgres outbox remains the source of truth and can always re-publish.
package redisstream

import (
	"context"

	"github.com/redis/go-redis/v9"
)

// Client is the narrow slice of go-redis used here (interface segregation);
// redis.UniversalClient satisfies it.
type Client interface {
	XAdd(ctx context.Context, a *redis.XAddArgs) *redis.StringCmd
	XGroupCreateMkStream(ctx context.Context, stream, group, start string) *redis.StatusCmd
	XReadGroup(ctx context.Context, a *redis.XReadGroupArgs) *redis.XStreamSliceCmd
	XAck(ctx context.Context, stream, group string, ids ...string) *redis.IntCmd
	XAutoClaim(ctx context.Context, a *redis.XAutoClaimArgs) *redis.XAutoClaimCmd
	XPendingExt(ctx context.Context, a *redis.XPendingExtArgs) *redis.XPendingExtCmd
	XPending(ctx context.Context, stream, group string) *redis.XPendingCmd
}

const (
	fieldID      = "id"
	fieldType    = "type"
	fieldKey     = "key"
	fieldPayload = "payload"
	fieldHeaders = "headers"
)
