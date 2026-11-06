package resilience

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"backend-go/internal/pkg/auth"
	"backend-go/internal/pkg/httpserver/respond"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

var slidingWindowScript = redis.NewScript(`
local key = KEYS[1]
local now = tonumber(ARGV[1])
local clearBefore = tonumber(ARGV[2])
local limit = tonumber(ARGV[3])
local windowSec = tonumber(ARGV[4])
redis.call('ZREMRANGEBYSCORE', key, 0, clearBefore)
local currentCount = redis.call('ZCARD', key)
if currentCount < limit then
  redis.call('ZADD', key, now, ARGV[5])
  redis.call('EXPIRE', key, windowSec)
  return {1, limit - (currentCount + 1)}
end
return {0, 0}`)

// RateLimiter defines interface for distributed rate limiting.
type RateLimiter interface {
	Allow(ctx context.Context, key string, limit int64, window time.Duration) (bool, int64, time.Duration, error)
}

// RedisSlidingWindowLimiter implements precise sliding-window rate limiting via Redis sorted sets.
type RedisSlidingWindowLimiter struct {
	client redis.UniversalClient
	prefix string
}

// NewRedisSlidingWindowLimiter initializes RedisSlidingWindowLimiter. An optional
// key prefix namespaces keys per service.
func NewRedisSlidingWindowLimiter(client redis.UniversalClient, prefix ...string) *RedisSlidingWindowLimiter {
	l := &RedisSlidingWindowLimiter{client: client}
	if len(prefix) > 0 {
		l.prefix = prefix[0]
	}
	return l
}

// Allow checks if key has capacity within sliding window.
func (r *RedisSlidingWindowLimiter) Allow(ctx context.Context, key string, limit int64, window time.Duration) (bool, int64, time.Duration, error) {
	if limit <= 0 || window <= 0 {
		return false, 0, 0, fmt.Errorf("rate limit and window must be positive")
	}
	if r.client == nil {
		return false, 0, 0, errors.New("rate limiter: redis client is not configured")
	}

	randomID := make([]byte, 8)
	if _, err := rand.Read(randomID); err != nil {
		return false, 0, 0, fmt.Errorf("generate rate-limit request ID: %w", err)
	}
	now := time.Now()
	nowMicro := now.UnixMicro()
	windowMicro := window.Microseconds()
	clearBefore := nowMicro - windowMicro
	rKey := fmt.Sprintf("%sratelimit:%s", r.prefix, key)

	// Atomic sliding-window script (single key: safe on Redis Cluster).
	member := strconv.FormatInt(now.UnixMicro(), 10) + ":" + hex.EncodeToString(randomID)
	res, err := slidingWindowScript.Run(ctx, r.client, []string{rKey}, nowMicro, clearBefore, limit, int(window.Seconds())+1, member).Result()
	if err != nil {
		return true, limit, 0, err
	}

	vals, ok := res.([]interface{})
	if !ok || len(vals) != 2 {
		return false, 0, 0, fmt.Errorf("unexpected Redis rate-limit response %T", res)
	}
	allowedValue, ok := vals[0].(int64)
	if !ok {
		return false, 0, 0, fmt.Errorf("unexpected Redis rate-limit decision type %T", vals[0])
	}
	remaining, ok := vals[1].(int64)
	if !ok {
		return false, 0, 0, fmt.Errorf("unexpected Redis rate-limit remaining type %T", vals[1])
	}
	if allowedValue != 0 && allowedValue != 1 {
		return false, 0, 0, fmt.Errorf("unexpected Redis rate-limit decision value %d", allowedValue)
	}

	return allowedValue == 1, remaining, window, nil
}

// MemoryRateLimiter provides local sliding window limiter for tests/stand-alone mode.
type MemoryRateLimiter struct {
	mu      sync.Mutex
	buckets map[string][]time.Time
}

// NewMemoryRateLimiter initializes MemoryRateLimiter.
func NewMemoryRateLimiter() *MemoryRateLimiter {
	return &MemoryRateLimiter{
		buckets: make(map[string][]time.Time),
	}
}

func (m *MemoryRateLimiter) Allow(ctx context.Context, key string, limit int64, window time.Duration) (bool, int64, time.Duration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	threshold := now.Add(-window)

	var valid []time.Time
	for _, t := range m.buckets[key] {
		if t.After(threshold) {
			valid = append(valid, t)
		}
	}

	if int64(len(valid)) < limit {
		valid = append(valid, now)
		m.buckets[key] = valid
		return true, limit - int64(len(valid)), window, nil
	}

	m.buckets[key] = valid
	return false, 0, window, nil
}

// KeyExtractorFunc determines the rate-limit partition key.
type KeyExtractorFunc func(c *gin.Context) string

// PrincipalOrIPKey keys on the authenticated principal, falling back to the client
// IP for anonymous calls. It deliberately ignores X-Tenant-ID / X-User-ID: those
// are client-controlled and would let any caller choose their own bucket.
func PrincipalOrIPKey(c *gin.Context) string {
	if p, ok := auth.From(c.Request.Context()); ok {
		return p.Key()
	}
	return "ip:" + c.ClientIP()
}

// Observer receives limiter decisions for metrics; nil is allowed.
type Observer interface {
	IncRateLimit(result string)
}

// LimitConfig configures the HTTP/gRPC rate-limit middleware.
type LimitConfig struct {
	Limiter RateLimiter
	Limit   int64
	Window  time.Duration
	// FailOpen lets requests through when the limiter backend errors. Rate limiting
	// protects capacity, so open is the usual choice; set false to fail closed.
	FailOpen bool
	Observer Observer
}

func (c LimitConfig) observe(result string) {
	if c.Observer != nil {
		c.Observer.IncRateLimit(result)
	}
}

// HTTPRateLimit creates a Gin middleware enforcing cfg.
func HTTPRateLimit(cfg LimitConfig, keyFunc KeyExtractorFunc) gin.HandlerFunc {
	if keyFunc == nil {
		keyFunc = PrincipalOrIPKey
	}
	return func(c *gin.Context) {
		if cfg.Limiter == nil {
			cfg.observe("error")
			slog.ErrorContext(c.Request.Context(), "Rate limiter is not configured")
			if cfg.FailOpen {
				c.Next()
				return
			}
			respond.Error(c, http.StatusServiceUnavailable, "RATE_LIMIT_UNAVAILABLE", "Rate limiting is unavailable")
			c.Abort()
			return
		}
		key := keyFunc(c)
		allowed, remaining, _, err := cfg.Limiter.Allow(c.Request.Context(), key, cfg.Limit, cfg.Window)
		if err != nil {
			cfg.observe("error")
			slog.ErrorContext(c.Request.Context(), "Rate limiter failed", "error", err, "fail_open", cfg.FailOpen)
			if cfg.FailOpen {
				c.Next()
				return
			}
			respond.Error(c, http.StatusServiceUnavailable, "RATE_LIMIT_UNAVAILABLE", "Rate limiting is unavailable")
			c.Abort()
			return
		}
		c.Header("X-RateLimit-Limit", strconv.FormatInt(cfg.Limit, 10))
		c.Header("X-RateLimit-Remaining", strconv.FormatInt(remaining, 10))
		if !allowed {
			cfg.observe("denied")
			c.Header("Retry-After", strconv.Itoa(int(cfg.Window.Seconds())))
			respond.Error(c, http.StatusTooManyRequests, "RATE_LIMIT_EXCEEDED", "Too many requests. Please retry later.")
			c.Abort()
			return
		}
		cfg.observe("allowed")
		c.Next()
	}
}
