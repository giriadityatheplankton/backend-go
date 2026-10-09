package telemetry

import (
	"context"
	"log/slog"

	"github.com/gin-gonic/gin"
)

// TraceHandler wraps an slog.Handler to inject trace_id and span_id from context.
type TraceHandler struct {
	slog.Handler
}

// NewTraceHandler wraps an existing slog.Handler.
func NewTraceHandler(next slog.Handler) *TraceHandler {
	return &TraceHandler{Handler: next}
}

// Handle extracts trace context and appends trace attributes before delegating.
func (h *TraceHandler) Handle(ctx context.Context, r slog.Record) error {
	if ctx != nil {
		if sc, ok := SpanFromContext(ctx); ok {
			r.AddAttrs(
				slog.String("trace_id", sc.TraceID),
				slog.String("span_id", sc.SpanID),
			)
		}
	}
	return h.Handler.Handle(ctx, r)
}

// WithAttrs returns a new handler with given attributes.
func (h *TraceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &TraceHandler{Handler: h.Handler.WithAttrs(attrs)}
}

// WithGroup returns a new handler with given group.
func (h *TraceHandler) WithGroup(name string) slog.Handler {
	return &TraceHandler{Handler: h.Handler.WithGroup(name)}
}

// HTTPMiddleware extracts incoming traceparent or generates new trace context,
// attaching it to both gin.Context and request context.
func HTTPMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		sc := ExtractHTTP(c.Request)
		ctx := ContextWithSpan(c.Request.Context(), sc)
		c.Request = c.Request.WithContext(ctx)

		// Set outgoing response headers
		c.Header(HeaderTraceParent, sc.Traceparent())

		c.Next()
	}
}
