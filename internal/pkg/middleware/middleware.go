package middleware

import (
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const HeaderRequestID = "X-Request-ID"

// RequestID attaches a unique UUID request ID header and sets it in gin context.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		reqID := c.GetHeader(HeaderRequestID)
		if reqID == "" {
			reqID = uuid.New().String()
		}
		c.Set(HeaderRequestID, reqID)
		c.Header(HeaderRequestID, reqID)
		c.Next()
	}
}

// Logger returns a Gin middleware for structured request logging via slog.
func Logger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		rawQuery := c.Request.URL.RawQuery

		c.Next()

		latency := time.Since(start)
		statusCode := c.Writer.Status()
		clientIP := c.ClientIP()
		reqID, _ := c.Get(HeaderRequestID)

		if rawQuery != "" {
			path = path + "?" + rawQuery
		}

		attributes := []any{
			slog.Int("status", statusCode),
			slog.String("method", c.Request.Method),
			slog.String("path", path),
			slog.String("ip", clientIP),
			slog.Duration("latency", latency),
			slog.Any("request_id", reqID),
		}

		if len(c.Errors) > 0 {
			attributes = append(attributes, slog.String("errors", c.Errors.String()))
			slog.Error("HTTP Request Error", attributes...)
		} else if statusCode >= 500 {
			slog.Error("HTTP Server Error", attributes...)
		} else if statusCode >= 400 {
			slog.Warn("HTTP Client Error", attributes...)
		} else {
			slog.Info("HTTP Request", attributes...)
		}
	}
}
