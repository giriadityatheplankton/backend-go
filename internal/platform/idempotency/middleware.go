package idempotency

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"backend-go/internal/platform/httpserver/respond"

	"github.com/gin-gonic/gin"
)

// HTTPConfig configures the HTTP idempotency middleware.
type HTTPConfig struct {
	Store Store
	// Scope returns the principal/tenant namespace for the key, taken from the
	// AUTH context (never from a client-supplied header). Two principals sending
	// the same Idempotency-Key must never see each other's responses.
	Scope func(*gin.Context) string
	// Required rejects requests that carry no key with 400.
	Required     bool
	FailureMode  FailureMode
	InFlightTTL  time.Duration
	CompletedTTL time.Duration
	MaxBodyBytes int64
	// ReplayHeaders are extra response headers persisted and replayed (Content-Type is always kept).
	ReplayHeaders []string
	// Cacheable decides which statuses are stored. Default: 2xx and deterministic
	// business 4xx (400, 404, 409, 422). Everything else releases the key so the
	// client can retry (401/403/408/429/5xx).
	Cacheable func(status int) bool
	Observer  Observer
}

// DefaultCacheable stores successes and deterministic client errors only.
func DefaultCacheable(status int) bool {
	if status >= 200 && status < 300 {
		return true
	}
	switch status {
	case http.StatusBadRequest, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity:
		return true
	}
	return false
}

func (c *HTTPConfig) defaults() {
	if c.InFlightTTL <= 0 {
		c.InFlightTTL = DefaultInFlightTTL
	}
	if c.CompletedTTL <= 0 {
		c.CompletedTTL = DefaultCompletedTTL
	}
	if c.MaxBodyBytes <= 0 {
		c.MaxBodyBytes = DefaultMaxBodyBytes
	}
	if c.Cacheable == nil {
		c.Cacheable = DefaultCacheable
	}
	if c.Scope == nil {
		c.Scope = func(*gin.Context) string { return "anonymous" }
	}
	c.ReplayHeaders = append([]string{"Location", "ETag"}, c.ReplayHeaders...)
}

type recorder struct {
	gin.ResponseWriter
	body      bytes.Buffer
	limit     int64
	truncated bool
}

func (w *recorder) capture(b []byte) {
	if w.truncated {
		return
	}
	if int64(w.body.Len()+len(b)) > w.limit {
		w.truncated = true
		return
	}
	w.body.Write(b)
}
func (w *recorder) Write(b []byte) (int, error) { w.capture(b); return w.ResponseWriter.Write(b) }
func (w *recorder) WriteString(s string) (int, error) {
	w.capture([]byte(s))
	return w.ResponseWriter.WriteString(s)
}

// HTTPMiddleware returns Gin middleware implementing edge idempotency.
func HTTPMiddleware(cfg HTTPConfig) gin.HandlerFunc {
	cfg.defaults()

	return func(c *gin.Context) {
		key := strings.TrimSpace(c.GetHeader(HeaderIdempotencyKey))
		if key == "" {
			key = strings.TrimSpace(c.GetHeader(HeaderLegacyIdempotencyKey))
		}
		if key == "" {
			if cfg.Required {
				respond.Error(c, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "The Idempotency-Key header is required")
				c.Abort()
				return
			}
			c.Next()
			return
		}
		if !ValidKey(key) {
			respond.Error(c, http.StatusBadRequest, "INVALID_IDEMPOTENCY_KEY", "Idempotency-Key must be 1-128 printable ASCII characters without spaces")
			c.Abort()
			return
		}

		c.Request = c.Request.WithContext(WithKey(c.Request.Context(), key))
		ctx := c.Request.Context()
		body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, cfg.MaxBodyBytes))
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				respond.Error(c, http.StatusRequestEntityTooLarge, "REQUEST_BODY_TOO_LARGE", "Request body exceeds the allowed size")
			} else {
				respond.Error(c, http.StatusBadRequest, "INVALID_REQUEST_BODY", "Request body could not be read")
			}
			c.Abort()
			return
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(body))

		route := c.FullPath()
		fingerprint := Fingerprint(c.Request.Method, route, string(body))
		storeKey := ScopedKey(cfg.Scope(c), c.Request.Method, route, key)

		lease, existing, err := cfg.Store.Acquire(ctx, storeKey, fingerprint, cfg.InFlightTTL)
		if err != nil {
			observe(cfg.Observer, ResultStoreError)
			slog.ErrorContext(ctx, "Idempotency store unavailable", "error", err, "mode", cfg.FailureMode)
			if cfg.FailureMode == FailOpen {
				observe(cfg.Observer, ResultBypassed)
				c.Next()
				return
			}
			c.Header("Retry-After", "1")
			respond.Error(c, http.StatusServiceUnavailable, "IDEMPOTENCY_UNAVAILABLE", "Idempotency service is unavailable; retry later")
			c.Abort()
			return
		}

		if existing != nil {
			if existing.Fingerprint != "" && existing.Fingerprint != fingerprint {
				observe(cfg.Observer, ResultMismatch)
				respond.Error(c, http.StatusUnprocessableEntity, "IDEMPOTENCY_KEY_REUSED", "This Idempotency-Key was already used with a different request")
				c.Abort()
				return
			}
			if existing.State != StateCompleted {
				observe(cfg.Observer, ResultInProgress)
				c.Header("Retry-After", "1")
				respond.Error(c, http.StatusConflict, "REQUEST_IN_PROGRESS", "A request with this Idempotency-Key is still being processed")
				c.Abort()
				return
			}
			observe(cfg.Observer, ResultReplayed)
			for k, v := range existing.Headers {
				c.Header(k, v)
			}
			c.Header(HeaderReplayed, "true")
			ct := existing.ContentType
			if ct == "" {
				ct = "application/json; charset=utf-8"
			}
			c.Data(existing.StatusCode, ct, existing.Body)
			c.Abort()
			return
		}

		observe(cfg.Observer, ResultAcquired)
		rec := &recorder{ResponseWriter: c.Writer, limit: cfg.MaxBodyBytes}
		c.Writer = rec
		stop := heartbeat(ctx, cfg.Store, lease, cfg.InFlightTTL, cfg.Observer)

		// Even if the handler panics, release the key so the client can retry.
		completed := false
		defer func() {
			stop()
			if completed {
				return
			}
			release(ctx, cfg, lease)
		}()

		c.Next()

		status := c.Writer.Status()
		if !cfg.Cacheable(status) || rec.truncated {
			return // deferred release
		}
		out := &Record{
			Fingerprint: fingerprint,
			StatusCode:  status,
			ContentType: c.Writer.Header().Get("Content-Type"),
			Headers:     map[string]string{},
			Body:        rec.body.Bytes(),
		}
		for _, h := range cfg.ReplayHeaders {
			if v := c.Writer.Header().Get(h); v != "" {
				out.Headers[h] = v
			}
		}
		saveCtx, cancel := persistContext(ctx)
		defer cancel()
		stop() // stop the heartbeat before fencing the final write
		switch err := cfg.Store.Complete(saveCtx, lease, out, cfg.CompletedTTL); {
		case err == nil:
			completed = true
		case errors.Is(err, ErrLeaseLost):
			completed = true // nothing to release; someone else owns the key now
			observe(cfg.Observer, ResultLeaseLost)
			slog.ErrorContext(ctx, "Idempotency lease lost before result was saved; in-flight TTL may be too short", "key", lease.Key)
		default:
			slog.ErrorContext(ctx, "Failed to save idempotent response", "error", err)
		}
	}
}

func release(ctx context.Context, cfg HTTPConfig, lease Lease) {
	rctx, cancel := persistContext(ctx)
	defer cancel()
	if err := cfg.Store.Release(rctx, lease); err != nil && !errors.Is(err, ErrLeaseLost) {
		slog.ErrorContext(ctx, "Failed to release idempotency lease", "error", err)
		return
	}
	observe(cfg.Observer, ResultReleased)
}

// Fingerprint hashes the parts of a request that define its identity.
func Fingerprint(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		var l [8]byte
		binary.LittleEndian.PutUint64(l[:], uint64(len(p))) //nolint:gosec // len is never negative
		h.Write(l[:])                                       // length prefix avoids ambiguity between ("ab","c") and ("a","bc")
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ScopedKey builds the storage key: hashed so length and charset are bounded.
func ScopedKey(scope, method, route, clientKey string) string {
	return Fingerprint(scope, method, route, clientKey)
}
