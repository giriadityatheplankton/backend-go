// Package middleware contains the standard Gin middleware chain and gRPC
// interceptors that are not tied to a specific domain.
package middleware

import (
	"log/slog"
	"net/url"
	"time"

	"backend-go/internal/platform/observability/requestid"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// HeaderRequestID is kept for callers that referenced the old constant.
const HeaderRequestID = requestid.Header

// RequestID accepts a well-formed inbound request ID or generates one, and
// stores it in the request context so logs, gRPC calls and outbox events share it.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(requestid.Header)
		if !requestid.Valid(id) {
			id = uuid.NewString()
		}
		c.Request = c.Request.WithContext(requestid.With(c.Request.Context(), id))
		c.Set(requestid.Header, id)
		c.Header(requestid.Header, id)
		c.Next()
	}
}

// Logger returns a Gin middleware for structured access logging. Query strings
// are not logged because they frequently contain tokens.
func Logger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		ctx := c.Request.Context()
		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}
		attrs := []any{
			slog.Int("status", c.Writer.Status()),
			slog.String("method", c.Request.Method),
			slog.String("route", route),
			slog.String("path", sanitizePath(c.Request.URL)),
			slog.String("ip", c.ClientIP()),
			slog.Duration("latency", time.Since(start)),
		}
		switch {
		case len(c.Errors) > 0:
			attrs = append(attrs, slog.String("errors", c.Errors.String()))
			slog.ErrorContext(ctx, "HTTP request error", attrs...)
		case c.Writer.Status() >= 500:
			slog.ErrorContext(ctx, "HTTP server error", attrs...)
		case c.Writer.Status() >= 400:
			slog.WarnContext(ctx, "HTTP client error", attrs...)
		default:
			slog.InfoContext(ctx, "HTTP request", attrs...)
		}
	}
}

func sanitizePath(u *url.URL) string {
	if len(u.Path) > 256 {
		return u.Path[:256]
	}
	return u.Path
}
