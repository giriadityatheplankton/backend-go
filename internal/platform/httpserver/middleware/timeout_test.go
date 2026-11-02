package middleware_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"backend-go/internal/platform/httpserver/middleware"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The old implementation ran c.Next() in a second goroutine, which raced with
// the timeout response (go test -race failed) and let a pooled gin.Context be
// reused while a handler was still writing to it.
func TestTimeoutSetsDeadlineWithoutSpawningGoroutines(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.Timeout(50 * time.Millisecond))
	r.GET("/slow", func(c *gin.Context) {
		select {
		case <-c.Request.Context().Done(): // a well-behaved handler honours ctx
			c.String(http.StatusGatewayTimeout, "deadline: %v", c.Request.Context().Err())
		case <-time.After(time.Second):
			c.String(http.StatusOK, "done")
		}
	})
	r.GET("/fast", func(c *gin.Context) {
		dl, ok := c.Request.Context().Deadline()
		require.True(t, ok)
		assert.WithinDuration(t, time.Now().Add(50*time.Millisecond), dl, 40*time.Millisecond)
		c.String(http.StatusOK, "fast")
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/slow", nil))
	assert.Equal(t, http.StatusGatewayTimeout, w.Code)
	assert.Contains(t, w.Body.String(), context.DeadlineExceeded.Error())

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/fast", nil))
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestTimeoutIsRaceFreeUnderConcurrency(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.Timeout(5 * time.Millisecond))
	r.GET("/x", func(c *gin.Context) { <-c.Request.Context().Done(); c.String(http.StatusGatewayTimeout, "t") })
	done := make(chan struct{})
	for i := 0; i < 20; i++ {
		go func() {
			r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
			done <- struct{}{}
		}()
	}
	for i := 0; i < 20; i++ {
		<-done
	}
}

func TestBodyLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.BodyLimit(8))
	r.POST("/x", func(c *gin.Context) {
		if _, err := c.GetRawData(); err != nil {
			c.Status(http.StatusRequestEntityTooLarge)
			return
		}
		c.Status(http.StatusOK)
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("123456789")))
	assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
}
