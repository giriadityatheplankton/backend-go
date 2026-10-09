package metrics_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"backend-go/internal/pkg/metrics"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
)

func TestMetricsCollection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	reg := prometheus.NewRegistry()
	m := metrics.InitMetrics(reg)

	r := gin.New()
	r.Use(m.HTTPMiddleware())
	r.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "pong")
	})

	// Run request
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	// Check metrics registered
	mfs, err := reg.Gather()
	assert.NoError(t, err)
	assert.NotEmpty(t, mfs)
}
