package middleware_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"backend-go/internal/pkg/httpserver/middleware"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestRecoveryMiddlewareHidesPanicDetails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.RecoveryMiddleware())
	r.GET("/panic", func(c *gin.Context) { panic("db password is hunter2") })

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/panic", nil))

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Contains(t, w.Body.String(), "INTERNAL_SERVER_ERROR")
	assert.NotContains(t, w.Body.String(), "hunter2")
}

func TestGRPCRecoveryDoesNotLeakPanicValue(t *testing.T) {
	ic := middleware.GRPCUnaryRecoveryInterceptor()
	_, err := ic(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/t.S/M"},
		func(context.Context, any) (any, error) { panic("secret internal detail") })

	st, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.Internal, st.Code())
	assert.NotContains(t, st.Message(), "secret")
}

func TestRequestIDAcceptsValidAndReplacesMalicious(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.RequestID())
	r.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-ID", "abc-123")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, "abc-123", w.Header().Get("X-Request-ID"))

	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-ID", "bad id\twith spaces")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.NotEqual(t, "bad id\twith spaces", w.Header().Get("X-Request-ID"))
	assert.Len(t, w.Header().Get("X-Request-ID"), 36, "a fresh UUID replaces untrusted input")
}
