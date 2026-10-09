package resilience

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"backend-go/internal/pkg/response"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

// RateLimiter defines interface for distributed rate limiting.
type RateLimiter interface {
	Allow(ctx context.Context, key string, limit int64, window time.Duration) (bool, int64, time.Duration, error)
}

// RedisSlidingWindowLimiter implements precise sliding-window rate limiting via Redis sorted sets.
type RedisSlidingWindowLimiter struct {
	client *redis.Client
}

// NewRedisSlidingWindowLimiter initializes RedisSlidingWindowLimiter.
func NewRedisSlidingWindowLimiter(client *redis.Client) *RedisSlidingWindowLimiter {
	return &RedisSlidingWindowLimiter{client: client}
}

// Allow checks if key has capacity within sliding window.
func (r *RedisSlidingWindowLimiter) Allow(ctx context.Context, key string, limit int64, window time.Duration) (bool, int64, time.Duration, error) {
	if r.client == nil {
		return true, limit, 0, nil
	}

	now := time.Now()
	nowMicro := now.UnixMicro()
	windowMicro := window.Microseconds()
	clearBefore := nowMicro - windowMicro
	rKey := fmt.Sprintf("ratelimit:%s", key)

	// Atomic sliding window Lua script
	luaScript := `
		local key = KEYS[1]
		local now = tonumber(ARGV[1])
		local clearBefore = tonumber(ARGV[2])
		local limit = tonumber(ARGV[3])
		local windowSec = tonumber(ARGV[4])

		-- 1. Remove old timestamps outside current window
		redis.call('ZREMRANGEBYSCORE', key, 0, clearBefore)

		-- 2. Count current entries
		local currentCount = redis.call('ZCARD', key)

		-- 3. Check limit
		if currentCount < limit then
			redis.call('ZADD', key, now, now)
			redis.call('EXPIRE', key, windowSec)
			return {1, limit - (currentCount + 1)}
		else
			return {0, 0}
		end
	`

	res, err := r.client.Eval(ctx, luaScript, []string{rKey}, nowMicro, clearBefore, limit, int(window.Seconds())+1).Result()
	if err != nil {
		return true, limit, 0, err
	}

	vals, ok := res.([]interface{})
	if !ok || len(vals) < 2 {
		return true, limit, 0, nil
	}

	allowed := vals[0].(int64) == 1
	remaining := vals[1].(int64)

	return allowed, remaining, window, nil
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

// KeyExtractorFunc determines rate limit partition key (e.g. tenant, user, IP).
type KeyExtractorFunc func(c *gin.Context) string

// TenantOrIPKey extracts Tenant-ID header or falls back to client IP.
func TenantOrIPKey(c *gin.Context) string {
	tenantID := c.GetHeader("X-Tenant-ID")
	if tenantID != "" {
		return "tenant:" + tenantID
	}
	userID := c.GetHeader("X-User-ID")
	if userID != "" {
		return "user:" + userID
	}
	return "ip:" + c.ClientIP()
}

// HTTPRateLimitMiddleware creates a Gin middleware for rate limiting.
func HTTPRateLimitMiddleware(limiter RateLimiter, keyFunc KeyExtractorFunc, limit int64, window time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := keyFunc(c)
		allowed, remaining, _, err := limiter.Allow(c.Request.Context(), key, limit, window)
		if err != nil {
			// Fail open on rate limiter storage errors
			c.Next()
			return
		}

		c.Header("X-RateLimit-Limit", strconv.FormatInt(limit, 10))
		c.Header("X-RateLimit-Remaining", strconv.FormatInt(remaining, 10))

		if !allowed {
			response.Error(c, http.StatusTooManyRequests, "RATE_LIMIT_EXCEEDED", "Too many requests. Please retry later.")
			c.Abort()
			return
		}

		c.Next()
	}
}
