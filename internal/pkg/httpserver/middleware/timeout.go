package middleware

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc"
)

// Timeout attaches a deadline to the request context. It deliberately does NOT
// run the handler in another goroutine: gin.Context and its ResponseWriter are
// not goroutine safe and are recycled after the handler returns. Handlers, DB
// and Redis calls honour ctx; use http.Server.WriteTimeout (or http.TimeoutHandler)
// as the hard limit.
func Timeout(d time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		if d <= 0 {
			c.Next()
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), d)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

// TimeoutMiddleware is the former name of Timeout.
//
// Deprecated: use Timeout.
func TimeoutMiddleware(d time.Duration) gin.HandlerFunc { return Timeout(d) }

// GRPCUnaryTimeoutInterceptor ensures gRPC requests have a default deadline.
func GRPCUnaryTimeoutInterceptor(defaultTimeout time.Duration) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if _, has := ctx.Deadline(); !has && defaultTimeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, defaultTimeout)
			defer cancel()
		}
		return handler(ctx, req)
	}
}

// BodyLimit caps every request body. Bind failures surface as 413 via http.MaxBytesError.
func BodyLimit(n int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, n)
		c.Next()
	}
}
