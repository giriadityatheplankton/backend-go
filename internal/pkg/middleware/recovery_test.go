package middleware_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"backend-go/internal/pkg/middleware"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestRecoveryMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(middleware.RecoveryMiddleware())
	r.GET("/panic", func(c *gin.Context) {
		panic("simulated critical runtime failure")
	})

	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Contains(t, w.Body.String(), "INTERNAL_SERVER_ERROR")
}

func TestGRPCUnaryRecoveryInterceptor(t *testing.T) {
	interceptor := middleware.GRPCUnaryRecoveryInterceptor()

	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		panic("gRPC panic")
	}

	_, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/test.Service/Panic"}, handler)
	assert.Error(t, err)

	st, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.Internal, st.Code())
}
