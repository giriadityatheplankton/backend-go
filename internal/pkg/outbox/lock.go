package outbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

var (
	ErrLockFailed  = errors.New("failed to acquire distributed lock")
	ErrLockNotHeld = errors.New("lock not held or token mismatch")
)

// DistributedLock is a best-effort mutual exclusion for singleton background jobs
// (cleaner, reconciliation). It is NOT used for outbox exclusivity (Postgres
// leases do that), and jobs protected by it must be idempotent anyway.
type DistributedLock interface {
	Acquire(ctx context.Context, key string, ttl time.Duration) (token string, err error)
	Release(ctx context.Context, key string, token string) error
	Extend(ctx context.Context, key string, token string, ttl time.Duration) error
}

var (
	releaseScript = redis.NewScript(`
if redis.call("get", KEYS[1]) == ARGV[1] then return redis.call("del", KEYS[1]) end
return 0`)
	extendScript = redis.NewScript(`
if redis.call("get", KEYS[1]) == ARGV[1] then return redis.call("pexpire", KEYS[1], ARGV[2]) end
return 0`)
)

var errNoRedis = errors.New("redis client unavailable")

// RedisDistributedLock implements the SET NX PX pattern with token-fenced release/extend.
type RedisDistributedLock struct {
	client redis.UniversalClient
	prefix string
}

// NewRedisDistributedLock creates a lock; prefix namespaces keys per service.
func NewRedisDistributedLock(client redis.UniversalClient, prefix string) *RedisDistributedLock {
	return &RedisDistributedLock{client: client, prefix: prefix}
}

func (l *RedisDistributedLock) Acquire(ctx context.Context, key string, ttl time.Duration) (string, error) {
	if l.client == nil {
		return "", errNoRedis
	}
	token := generateToken()
	ok, err := l.client.SetNX(ctx, l.prefix+key, token, ttl).Result()
	if err != nil {
		return "", err
	}
	if !ok {
		return "", ErrLockFailed
	}
	return token, nil
}

func (l *RedisDistributedLock) Release(ctx context.Context, key, token string) error {
	if l.client == nil {
		return errNoRedis
	}
	n, err := releaseScript.Run(ctx, l.client, []string{l.prefix + key}, token).Int()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrLockNotHeld
	}
	return nil
}

func (l *RedisDistributedLock) Extend(ctx context.Context, key, token string, ttl time.Duration) error {
	if l.client == nil {
		return errNoRedis
	}
	n, err := extendScript.Run(ctx, l.client, []string{l.prefix + key}, token, ttl.Milliseconds()).Int()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrLockNotHeld
	}
	return nil
}

func generateToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
