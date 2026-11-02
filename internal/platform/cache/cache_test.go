package cache_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"backend-go/internal/platform/cache"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
)

type mockUser struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func TestSingleflightCacheProtection(t *testing.T) {
	mgr := cache.NewManager(nil) // in-memory fallback without redis
	ctx := context.Background()

	var dbQueryCount int32

	loader := func(c context.Context) (mockUser, error) {
		time.Sleep(20 * time.Millisecond) // simulate expensive query
		atomic.AddInt32(&dbQueryCount, 1)
		return mockUser{ID: 10, Name: "Aditya"}, nil
	}

	// 50 concurrent requests for the exact same cache key
	const concurrency = 50
	var wg sync.WaitGroup
	wg.Add(concurrency)

	for i := 0; i < concurrency; i++ {
		go func() {
			defer wg.Done()
			user, err := cache.GetOrSet(ctx, mgr, "user:10", 1*time.Minute, loader)
			assert.NoError(t, err)
			assert.Equal(t, 10, user.ID)
			assert.Equal(t, "Aditya", user.Name)
		}()
	}

	wg.Wait()

	// Singleflight must ensure loader was invoked ONLY ONCE despite 50 concurrent requests
	assert.Equal(t, int32(1), atomic.LoadInt32(&dbQueryCount), "Singleflight should suppress multiple DB queries for identical key")
}

func TestLoadWithoutCacheAlwaysCallsLoader(t *testing.T) {
	ctx := context.Background()
	var calls int
	want := mockUser{ID: 11, Name: "Fresh"}

	got, err := cache.LoadWithoutCache(ctx, func(context.Context) (mockUser, error) {
		calls++
		return want, nil
	})

	assert.NoError(t, err)
	assert.Equal(t, want, got)
	assert.Equal(t, 1, calls)
}

func TestLoadWithoutCacheRejectsNilLoader(t *testing.T) {
	_, err := cache.LoadWithoutCache[mockUser](context.Background(), nil)
	assert.Error(t, err)
}

func newRedis(t *testing.T) *redis.Client {
	mr := miniredis.RunT(t)
	c := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestFirstCallerCancellationDoesNotFailOtherWaiters(t *testing.T) {
	mgr := cache.NewManager(newRedis(t))
	started := make(chan struct{})
	loader := func(context.Context) (mockUser, error) {
		close(started)
		time.Sleep(80 * time.Millisecond)
		return mockUser{ID: 1}, nil
	}

	ctx1, cancel1 := context.WithCancel(context.Background())
	go func() { _, _ = cache.GetOrSet(ctx1, mgr, "k", time.Minute, loader) }()
	<-started

	res := make(chan error, 1)
	go func() {
		_, err := cache.GetOrSet(context.Background(), mgr, "k", time.Minute, loader)
		res <- err
	}()
	time.Sleep(10 * time.Millisecond)
	cancel1() // the first caller goes away mid-load
	assert.NoError(t, <-res, "waiter must still get the loaded value")
}

func TestCallerStopsWaitingWhenOwnContextExpires(t *testing.T) {
	mgr := cache.NewManager(nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := cache.GetOrSet(ctx, mgr, "slow", time.Minute, func(context.Context) (mockUser, error) {
		time.Sleep(200 * time.Millisecond)
		return mockUser{}, nil
	})
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestNegativeCachingStopsPenetration(t *testing.T) {
	errMissing := errors.New("missing")
	mgr := cache.NewManager(newRedis(t), cache.WithNegativeCache(time.Minute, func(err error) bool { return errors.Is(err, errMissing) }, errMissing))
	var calls int32
	loader := func(context.Context) (mockUser, error) { atomic.AddInt32(&calls, 1); return mockUser{}, errMissing }

	for i := 0; i < 5; i++ {
		_, err := cache.GetOrSet(context.Background(), mgr, "ghost", time.Minute, loader)
		assert.ErrorIs(t, err, errMissing)
	}
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls))
}

func TestPrefixAndInvalidate(t *testing.T) {
	c := newRedis(t)
	mgr := cache.NewManager(c, cache.WithPrefix("svc:"))
	_, _ = cache.GetOrSet(context.Background(), mgr, "k", time.Minute, func(context.Context) (mockUser, error) { return mockUser{ID: 3}, nil })
	n, _ := c.Exists(context.Background(), "svc:k").Result()
	assert.Equal(t, int64(1), n)
	assert.NoError(t, mgr.Invalidate(context.Background(), "k"))
	n, _ = c.Exists(context.Background(), "svc:k").Result()
	assert.Equal(t, int64(0), n)
}
