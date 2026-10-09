package health_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"backend-go/internal/pkg/health"

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
