package idempotency_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"backend-go/internal/platform/idempotency"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type downStore struct{}

func (downStore) Acquire(context.Context, string, string, time.Duration) (idempotency.Lease, *idempotency.Record, error) {
	return idempotency.Lease{}, nil, errors.New("redis down")
}
func (downStore) Complete(context.Context, idempotency.Lease, *idempotency.Record, time.Duration) error {
	return errors.New("redis down")
}
func (downStore) Release(context.Context, idempotency.Lease) error { return errors.New("redis down") }
func (downStore) Extend(context.Context, idempotency.Lease, time.Duration) error {
	return errors.New("redis down")
}

func newRouter(cfg idempotency.HTTPConfig, handler gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(idempotency.HTTPMiddleware(cfg))
	r.POST("/orders", handler)
	return r
}

func do(r http.Handler, key, body string, hdr ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/orders", strings.NewReader(body))
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestHTTP_ConcurrentSameKeyExecutesOnce(t *testing.T) {
	var calls int32
	r := newRouter(idempotency.HTTPConfig{Store: idempotency.NewMemoryStore()}, func(c *gin.Context) {
		atomic.AddInt32(&calls, 1)
		time.Sleep(50 * time.Millisecond)
		c.JSON(http.StatusCreated, gin.H{"id": "o1"})
	})

	var wg sync.WaitGroup
	codes := make([]int, 16)
	for i := range codes {
		wg.Add(1)
		go func(i int) { defer wg.Done(); codes[i] = do(r, "k1", `{"a":1}`).Code }(i)
	}
	wg.Wait()

	assert.Equal(t, int32(1), atomic.LoadInt32(&calls), "handler must run exactly once")
	created := 0
	for _, c := range codes {
		if c == http.StatusCreated {
			created++
		} else {
			assert.Equal(t, http.StatusConflict, c)
		}
	}
	assert.Equal(t, 1, created)
}

func TestHTTP_ReplayAfterCompletion(t *testing.T) {
	var calls int32
	r := newRouter(idempotency.HTTPConfig{Store: idempotency.NewMemoryStore()}, func(c *gin.Context) {
		atomic.AddInt32(&calls, 1)
		c.Header("Location", "/orders/o1")
		c.JSON(http.StatusCreated, gin.H{"id": "o1"})
	})
	first := do(r, "k1", `{"a":1}`)
	second := do(r, "k1", `{"a":1}`)

	assert.Equal(t, int32(1), atomic.LoadInt32(&calls))
	assert.Equal(t, http.StatusCreated, second.Code)
	assert.JSONEq(t, first.Body.String(), second.Body.String())
	assert.Equal(t, "true", second.Header().Get("Idempotent-Replayed"))
	assert.Equal(t, "/orders/o1", second.Header().Get("Location"))
	assert.Contains(t, second.Header().Get("Content-Type"), "application/json")
	assert.Empty(t, first.Header().Get("Idempotent-Replayed"))
}

func TestHTTP_LegacyHeaderAccepted(t *testing.T) {
	var calls int32
	r := newRouter(idempotency.HTTPConfig{Store: idempotency.NewMemoryStore()}, func(c *gin.Context) {
		atomic.AddInt32(&calls, 1)
		c.Status(http.StatusNoContent)
	})
	do(r, "", "{}", "X-Idempotency-Key", "legacy")
	do(r, "", "{}", "X-Idempotency-Key", "legacy")
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls))
}

func TestHTTP_SameKeyDifferentBodyIsRejected(t *testing.T) {
	r := newRouter(idempotency.HTTPConfig{Store: idempotency.NewMemoryStore()}, func(c *gin.Context) { c.Status(http.StatusCreated) })
	do(r, "k1", `{"a":1}`)
	w := do(r, "k1", `{"a":2}`)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	assert.Contains(t, w.Body.String(), "IDEMPOTENCY_KEY_REUSED")
}

func TestHTTP_ScopeIsolation(t *testing.T) {
	var calls int32
	scope := "tenant-a"
	r := newRouter(idempotency.HTTPConfig{
		Store: idempotency.NewMemoryStore(),
		Scope: func(*gin.Context) string { return scope },
	}, func(c *gin.Context) {
		n := atomic.AddInt32(&calls, 1)
		c.JSON(http.StatusCreated, gin.H{"n": n})
	})
	a := do(r, "same-key", `{}`)
	scope = "tenant-b"
	b := do(r, "same-key", `{}`)

	assert.Equal(t, int32(2), atomic.LoadInt32(&calls), "different principals must not share a key")
	assert.NotEqual(t, a.Body.String(), b.Body.String())
	assert.Empty(t, b.Header().Get("Idempotent-Replayed"))
}

func TestHTTP_ServerErrorsAndAuthErrorsAreNotCached(t *testing.T) {
	for _, status := range []int{500, 503, 401, 403, 408, 429} {
		var calls int32
		r := newRouter(idempotency.HTTPConfig{Store: idempotency.NewMemoryStore()}, func(c *gin.Context) {
			atomic.AddInt32(&calls, 1)
			c.Status(status)
		})
		do(r, "k", `{}`)
		do(r, "k", `{}`)
		assert.Equal(t, int32(2), atomic.LoadInt32(&calls), "status %d must release the key", status)
	}
}

func TestHTTP_BusinessClientErrorsAreCached(t *testing.T) {
	var calls int32
	r := newRouter(idempotency.HTTPConfig{Store: idempotency.NewMemoryStore()}, func(c *gin.Context) {
		atomic.AddInt32(&calls, 1)
		c.JSON(http.StatusUnprocessableEntity, gin.H{"e": 1})
	})
	do(r, "k", `{}`)
	w := do(r, "k", `{}`)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls))
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
}

func TestHTTP_ClientDisconnectStillSavesResult(t *testing.T) {
	var calls int32
	r := newRouter(idempotency.HTTPConfig{Store: idempotency.NewMemoryStore()}, func(c *gin.Context) {
		atomic.AddInt32(&calls, 1)
		ctx, cancel := context.WithCancel(c.Request.Context())
		c.Request = c.Request.WithContext(ctx)
		cancel() // client went away / deadline passed, but the work succeeded
		c.JSON(http.StatusCreated, gin.H{"ok": true})
	})
	do(r, "k", `{}`)
	w := do(r, "k", `{}`)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls))
	assert.Equal(t, http.StatusCreated, w.Code, "retry must replay, not return 409")
	assert.Equal(t, "true", w.Header().Get("Idempotent-Replayed"))
}

func TestHTTP_PanicReleasesKey(t *testing.T) {
	var calls int32
	store := idempotency.NewMemoryStore()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(gin.CustomRecovery(func(c *gin.Context, _ any) { c.AbortWithStatus(500) }))
	r.Use(idempotency.HTTPMiddleware(idempotency.HTTPConfig{Store: store}))
	r.POST("/orders", func(c *gin.Context) {
		if atomic.AddInt32(&calls, 1) == 1 {
			panic("boom")
		}
		c.Status(http.StatusCreated)
	})
	assert.Equal(t, 500, do(r, "k", `{}`).Code)
	assert.Equal(t, http.StatusCreated, do(r, "k", `{}`).Code, "key must be retryable after a panic")
}

func TestHTTP_LeaseHeartbeatKeepsSlowRequestsExclusive(t *testing.T) {
	var calls int32
	r := newRouter(idempotency.HTTPConfig{Store: idempotency.NewMemoryStore(), InFlightTTL: 90 * time.Millisecond}, func(c *gin.Context) {
		atomic.AddInt32(&calls, 1)
		time.Sleep(300 * time.Millisecond) // 3x longer than the in-flight TTL
		c.Status(http.StatusCreated)
	})
	done := make(chan struct{})
	go func() { do(r, "k", `{}`); close(done) }()
	time.Sleep(200 * time.Millisecond)
	w := do(r, "k", `{}`)
	<-done
	assert.Equal(t, http.StatusConflict, w.Code, "heartbeat must keep the lease; retry sees in-progress")
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls))
}

func TestHTTP_StoreDownFailClosed(t *testing.T) {
	var calls int32
	r := newRouter(idempotency.HTTPConfig{Store: downStore{}, FailureMode: idempotency.FailClosed}, func(c *gin.Context) {
		atomic.AddInt32(&calls, 1)
		c.Status(201)
	})
	w := do(r, "k", `{}`)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Equal(t, int32(0), atomic.LoadInt32(&calls))
}

func TestHTTP_StoreDownFailOpen(t *testing.T) {
	var calls int32
	r := newRouter(idempotency.HTTPConfig{Store: downStore{}, FailureMode: idempotency.FailOpen}, func(c *gin.Context) {
		atomic.AddInt32(&calls, 1)
		c.Status(201)
	})
	assert.Equal(t, 201, do(r, "k", `{}`).Code)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls))
}

func TestHTTP_KeyValidationAndRequired(t *testing.T) {
	r := newRouter(idempotency.HTTPConfig{Store: idempotency.NewMemoryStore(), Required: true}, func(c *gin.Context) { c.Status(201) })
	assert.Equal(t, 400, do(r, "", `{}`).Code)
	assert.Equal(t, 400, do(r, strings.Repeat("a", 129), `{}`).Code)
	assert.Equal(t, 400, do(r, "has space", `{}`).Code)
	assert.Equal(t, 201, do(r, "ok-key_1.2", `{}`).Code)
}

func TestHTTP_NoKeyPassesThrough(t *testing.T) {
	var calls int32
	r := newRouter(idempotency.HTTPConfig{Store: idempotency.NewMemoryStore()}, func(c *gin.Context) { atomic.AddInt32(&calls, 1); c.Status(201) })
	do(r, "", `{}`)
	do(r, "", `{}`)
	assert.Equal(t, int32(2), atomic.LoadInt32(&calls))
}

func TestFingerprint_NoBoundaryAmbiguity(t *testing.T) {
	require.NotEqual(t, idempotency.Fingerprint("ab", "c"), idempotency.Fingerprint("a", "bc"))
}
