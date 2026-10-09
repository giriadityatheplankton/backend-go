package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"backend-go/internal/pkg/middleware"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestTimeoutMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(middleware.TimeoutMiddleware(50 * time.Millisecond))
	r.GET("/slow", func(c *gin.Context) {
		time.Sleep(100 * time.Millisecond)
		c.String(http.StatusOK, "done")
	})
	r.GET("/fast", func(c *gin.Context) {
		c.String(http.StatusOK, "fast")
	})

	// Slow route should timeout
	reqSlow := httptest.NewRequest(http.MethodGet, "/slow", nil)
	wSlow := httptest.NewRecorder()
	r.ServeHTTP(wSlow, reqSlow)
	assert.Equal(t, http.StatusGatewayTimeout, wSlow.Code)

	// Fast route should succeed
	reqFast := httptest.NewRequest(http.MethodGet, "/fast", nil)
	wFast := httptest.NewRecorder()
	r.ServeHTTP(wFast, reqFast)
	assert.Equal(t, http.StatusOK, wFast.Code)
}
