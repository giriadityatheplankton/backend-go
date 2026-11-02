package health_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"backend-go/internal/platform/health"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestHealthManager(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mgr := health.NewManager(100 * time.Millisecond)

	r := gin.New()
	r.GET("/healthz/live", mgr.LivenessHandler)
	r.GET("/healthz/ready", mgr.ReadinessHandler)

	// 1. Live probe always 200
	wLive := httptest.NewRecorder()
	reqLive := httptest.NewRequest(http.MethodGet, "/healthz/live", nil)
	r.ServeHTTP(wLive, reqLive)
	assert.Equal(t, http.StatusOK, wLive.Code)

	// 2. Ready probe with healthy component
	mgr.Register("redis", health.CheckerFunc(func(ctx context.Context) error {
		return nil
	}))

	wReady := httptest.NewRecorder()
	reqReady := httptest.NewRequest(http.MethodGet, "/healthz/ready", nil)
	r.ServeHTTP(wReady, reqReady)
	assert.Equal(t, http.StatusOK, wReady.Code)

	// 3. Ready probe with failing component
	mgr.Register("db", health.CheckerFunc(func(ctx context.Context) error {
		return errors.New("db connection timeout")
	}))

	wFail := httptest.NewRecorder()
	r.ServeHTTP(wFail, reqReady)
	assert.Equal(t, http.StatusServiceUnavailable, wFail.Code)

	// 4. Set ready to false during graceful shutdown
	mgr.SetReady(false)
	wShutdown := httptest.NewRecorder()
	r.ServeHTTP(wShutdown, reqReady)
	assert.Equal(t, http.StatusServiceUnavailable, wShutdown.Code)
}

func TestDegradedComponentDoesNotFailReadiness(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mgr := health.NewManager(100 * time.Millisecond)
	mgr.Register("primary", health.CheckerFunc(func(context.Context) error { return nil }))
	mgr.RegisterWithSeverity("replica-0", health.CheckerFunc(func(context.Context) error {
		return errors.New("dial tcp 10.0.0.5:5432: connection refused")
	}), health.Degraded)

	r := gin.New()
	r.GET("/ready", mgr.ReadinessHandler)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ready", nil))

	assert.Equal(t, http.StatusOK, w.Code, "a down replica must not make the pod unready")
	assert.Contains(t, w.Body.String(), "DEGRADED")
	assert.NotContains(t, w.Body.String(), "10.0.0.5", "internal error text must not leak")
}

func TestCriticalFailureDoesNotLeakErrorText(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mgr := health.NewManager(100 * time.Millisecond)
	mgr.Register("db", health.CheckerFunc(func(context.Context) error { return errors.New("secret-host:5432 refused") }))
	r := gin.New()
	r.GET("/ready", mgr.ReadinessHandler)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ready", nil))
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.NotContains(t, w.Body.String(), "secret-host")
}
