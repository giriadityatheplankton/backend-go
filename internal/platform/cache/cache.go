// Package cache offers read-through caching with stampede protection.
//
// Caching is an optimisation for data that may be briefly stale. Decisions about
// money, stock or permissions must bypass it (LoadWithoutCache / ReadPrimary).
package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"
)

const negativeMarker = "\x00negative"

// Manager provides cache operations with singleflight stampede protection.
type Manager struct {
	client      redis.UniversalClient
	sf          singleflight.Group
	prefix      string
	jitter      float64
	loadTimeout time.Duration

	negTTL  time.Duration
	isMiss  func(error) bool
	missErr error
}

// Option customises a Manager.
type Option func(*Manager)

// WithPrefix namespaces keys per service.
func WithPrefix(p string) Option { return func(m *Manager) { m.prefix = p } }

// WithJitter randomises each TTL by +/- fraction (e.g. 0.1) so keys written together do not expire together.
func WithJitter(fraction float64) Option { return func(m *Manager) { m.jitter = fraction } }

// WithLoadTimeout bounds a shared load. It runs detached from any single caller's context.
func WithLoadTimeout(d time.Duration) Option { return func(m *Manager) { m.loadTimeout = d } }

// WithNegativeCache remembers "does not exist" results for ttl, which stops
// repeated lookups of missing keys from reaching the database (cache penetration).
// isMiss classifies the loader error, and missErr is returned on a negative hit.
func WithNegativeCache(ttl time.Duration, isMiss func(error) bool, missErr error) Option {
	return func(m *Manager) { m.negTTL, m.isMiss, m.missErr = ttl, isMiss, missErr }
}

// NewManager initializes the cache manager. A nil client makes it a pure
// singleflight wrapper without storage.
func NewManager(client redis.UniversalClient, opts ...Option) *Manager {
	m := &Manager{client: client, jitter: 0.1, loadTimeout: 5 * time.Second}
	for _, o := range opts {
		o(m)
	}
	return m
}

// LoadWithoutCache invokes loader directly without touching Redis. Use for data
// that must come from its authoritative source.
func LoadWithoutCache[T any](ctx context.Context, loader func(context.Context) (T, error)) (T, error) {
	var zero T
	if loader == nil {
		return zero, errors.New("cache loader is required")
	}
	return loader(ctx)
}

func (m *Manager) ttlWithJitter(ttl time.Duration) time.Duration {
	if m.jitter <= 0 {
		return ttl
	}
	return time.Duration(float64(ttl) * (1 - m.jitter + 2*m.jitter*rand.Float64()))
}

// GetOrSet returns the cached value or runs loader once per key across concurrent callers.
func GetOrSet[T any](ctx context.Context, m *Manager, key string, ttl time.Duration, loader func(ctx context.Context) (T, error)) (T, error) {
	var zero T
	if m == nil {
		return loader(ctx)
	}
	key = m.prefix + key

	if m.client != nil {
		val, err := m.client.Get(ctx, key).Result()
		switch {
		case err == nil && val == negativeMarker && m.missErr != nil:
			return zero, m.missErr
		case err == nil:
			var out T
			if json.Unmarshal([]byte(val), &out) == nil {
				return out, nil
			}
		case !errors.Is(err, redis.Nil):
			slog.WarnContext(ctx, "Redis cache read failed; using loader", "key", key, "error", err)
		}
	}

	// The shared load must not die because the first caller cancelled: detach it,
	// bound it, and let each caller wait only as long as its own context allows.
	ch := m.sf.DoChan(key, func() (any, error) {
		lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), m.loadTimeout)
		defer cancel()

		data, err := loader(lctx)
		if err != nil {
			if m.isMiss != nil && m.isMiss(err) && m.client != nil && m.negTTL > 0 {
				_ = m.client.Set(lctx, key, negativeMarker, m.ttlWithJitter(m.negTTL)).Err()
			}
			return zero, err
		}
		if m.client != nil && ttl > 0 {
			if b, mErr := json.Marshal(data); mErr == nil {
				if sErr := m.client.Set(lctx, key, b, m.ttlWithJitter(ttl)).Err(); sErr != nil {
					slog.WarnContext(lctx, "Redis cache write failed", "key", key, "error", sErr)
				}
			}
		}
		return data, nil
	})

	select {
	case r := <-ch:
		if r.Err != nil {
			return zero, r.Err
		}
		out, ok := r.Val.(T)
		if !ok {
			return zero, fmt.Errorf("cache: unexpected value type %T", r.Val)
		}
		return out, nil
	case <-ctx.Done():
		return zero, ctx.Err()
	}
}

// Invalidate removes a cached key.
func (m *Manager) Invalidate(ctx context.Context, key string) error {
	if m == nil || m.client == nil {
		return nil
	}
	return m.client.Del(ctx, m.prefix+key).Err()
}

// FormatKey formats an entity name and ID into a standard key.
func FormatKey(entity string, id any) string { return fmt.Sprintf("cache:%s:%v", entity, id) }
