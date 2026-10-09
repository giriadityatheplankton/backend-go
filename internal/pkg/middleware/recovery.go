package middleware

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"

	"backend-go/internal/pkg/response"
	"backend-go/internal/pkg/telemetry"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// RecoveryMiddleware provides structured JSON panic recovery for Gin with trace context.
func RecoveryMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				stack := string(debug.Stack())
				ctx := c.Request.Context()

				attrs := []any{
					slog.Any("panic", r),
					slog.String("stack", stack),
					slog.String("path", c.Request.URL.Path),
					slog.String("method", c.Request.Method),
				}

				if sc, ok := telemetry.SpanFromContext(ctx); ok {
					attrs = append(attrs,
						slog.String("trace_id", sc.TraceID),
						slog.String("span_id", sc.SpanID),
					)
				}

				slog.ErrorContext(ctx, "Panic recovered in HTTP handler", attrs...)

				c.Abort()
				response.Error(c, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR", "An unexpected server error occurred")
			}
		}()

		c.Next()
	}
}

// GRPCUnaryRecoveryInterceptor provides structured JSON panic recovery for gRPC unary calls.
func GRPCUnaryRecoveryInterceptor() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req interface{},
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (resp interface{}, err error) {
		defer func() {
			if r := recover(); r != nil {
				stack := string(debug.Stack())

				attrs := []any{
					slog.Any("panic", r),
					slog.String("stack", stack),
					slog.String("method", info.FullMethod),
				}

				if sc, ok := telemetry.SpanFromContext(ctx); ok {
					attrs = append(attrs,
						slog.String("trace_id", sc.TraceID),
						slog.String("span_id", sc.SpanID),
					)
				}

				slog.ErrorContext(ctx, "Panic recovered in gRPC unary handler", attrs...)
				err = status.Errorf(codes.Internal, "internal server error: %v", fmt.Sprint(r))
			}
		}()

		return handler(ctx, req)
	}
}
