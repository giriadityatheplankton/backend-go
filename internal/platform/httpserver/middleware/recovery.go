package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"runtime/debug"

	"backend-go/internal/platform/httpserver/respond"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// RecoveryMiddleware converts panics into a generic 500. The panic value and
// stack trace are logged, never returned to the client.
func RecoveryMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				slog.ErrorContext(c.Request.Context(), "Panic recovered in HTTP handler",
					slog.Any("panic", r),
					slog.String("stack", string(debug.Stack())),
					slog.String("route", c.FullPath()),
					slog.String("method", c.Request.Method),
				)
				c.Abort()
				respond.Error(c, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR", "An unexpected server error occurred")
			}
		}()
		c.Next()
	}
}

// GRPCUnaryRecoveryInterceptor converts panics into codes.Internal with a generic message.
func GRPCUnaryRecoveryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		defer func() {
			if r := recover(); r != nil {
				slog.ErrorContext(ctx, "Panic recovered in gRPC unary handler",
					slog.Any("panic", r),
					slog.String("stack", string(debug.Stack())),
					slog.String("method", info.FullMethod),
				)
				err = status.Error(codes.Internal, "internal server error")
			}
		}()
		return handler(ctx, req)
	}
}

// GRPCStreamRecoveryInterceptor converts panics in streaming handlers into codes.Internal.
func GRPCStreamRecoveryInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
		defer func() {
			if r := recover(); r != nil {
				slog.ErrorContext(ss.Context(), "Panic recovered in gRPC stream handler",
					slog.Any("panic", r),
					slog.String("stack", string(debug.Stack())),
					slog.String("method", info.FullMethod),
				)
				err = status.Error(codes.Internal, "internal server error")
			}
		}()
		return handler(srv, ss)
	}
}
