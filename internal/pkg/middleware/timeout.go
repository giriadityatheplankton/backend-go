package middleware

import (
	"context"
	"net/http"
	"time"

	"backend-go/internal/pkg/response"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc"
)

// TimeoutMiddleware injects a request deadline into HTTP context.
func TimeoutMiddleware(timeout time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)
		defer cancel()

		c.Request = c.Request.WithContext(ctx)

		done := make(chan struct{})
		go func() {
			c.Next()
			close(done)
		}()

		select {
		case <-done:
			return
		case <-ctx.Done():
			if ctx.Err() == context.DeadlineExceeded {
				c.Abort()
				response.Error(c, http.StatusGatewayTimeout, "REQUEST_TIMEOUT", "Request deadline exceeded")
			}
		}
	}
}

// GRPCUnaryTimeoutInterceptor ensures gRPC requests have a default deadline context.
func GRPCUnaryTimeoutInterceptor(defaultTimeout time.Duration) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req interface{},
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (interface{}, error) {
		if _, hasDeadline := ctx.Deadline(); !hasDeadline && defaultTimeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, defaultTimeout)
			defer cancel()
		}
		return handler(ctx, req)
	}
}
