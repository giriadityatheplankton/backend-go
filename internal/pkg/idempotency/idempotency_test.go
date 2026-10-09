package idempotency_test

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"backend-go/internal/pkg/idempotency"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestIdempotencyMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	storage := idempotency.NewMemoryStorage()

	var counter int32

	r := gin.New()
	r.Use(idempotency.HTTPMiddleware(storage, 5*time.Second, 1*time.Minute))
	r.POST("/orders", func(c *gin.Context) {
		atomic.AddInt32(&counter, 1)
		c.JSON(http.StatusCreated, gin.H{"order_id": "ord-99", "count": atomic.LoadInt32(&counter)})
	})

	// First Request: Executes handler
	req1 := httptest.NewRequest(http.MethodPost, "/orders", nil)
	req1.Header.Set(idempotency.HeaderIdempotencyKey, "idem-key-1")
	w1 := httptest.NewRecorder()
	r.ServeHTTP(w1, req1)

	assert.Equal(t, http.StatusCreated, w1.Code)
	assert.Contains(t, w1.Body.String(), `"order_id":"ord-99"`)
	assert.Equal(t, int32(1), atomic.LoadInt32(&counter))

	// Second Request with SAME key: Replays cached response without executing handler
	req2 := httptest.NewRequest(http.MethodPost, "/orders", nil)
	req2.Header.Set(idempotency.HeaderIdempotencyKey, "idem-key-1")
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)

	assert.Equal(t, http.StatusCreated, w2.Code)
	assert.Contains(t, w2.Body.String(), `"order_id":"ord-99"`)
	assert.Equal(t, "HIT-IDEMPOTENT", w2.Header().Get("X-Cache-Lookup"))
	assert.Equal(t, int32(1), atomic.LoadInt32(&counter), "Handler should not have been called a second time")
}
