package telemetry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
)

const (
	HeaderTraceParent = "traceparent"
	HeaderTraceState  = "tracestate"
)

type contextKey struct{}

var traceContextKey = contextKey{}

// SpanContext represents W3C compliant distributed trace context.
type SpanContext struct {
	TraceID    string
	SpanID     string
	TraceFlags string
	TraceState string
}

// NewSpanContext creates a new root SpanContext with random IDs.
func NewSpanContext() SpanContext {
	return SpanContext{
		TraceID:    generateHex(16),
		SpanID:     generateHex(8),
		TraceFlags: "01",
	}
}

// NewChildSpan creates a child SpanContext inheriting the parent's TraceID.
func (sc SpanContext) NewChildSpan() SpanContext {
	return SpanContext{
		TraceID:    sc.TraceID,
		SpanID:     generateHex(8),
		TraceFlags: sc.TraceFlags,
		TraceState: sc.TraceState,
	}
}

// Traceparent formats the context into standard W3C traceparent string.
func (sc SpanContext) Traceparent() string {
	if sc.TraceID == "" || sc.SpanID == "" {
		return ""
	}
	flags := sc.TraceFlags
	if flags == "" {
		flags = "01"
	}
	return fmt.Sprintf("00-%s-%s-%s", sc.TraceID, sc.SpanID, flags)
}

// ContextWithSpan injects SpanContext into standard context.Context.
func ContextWithSpan(ctx context.Context, sc SpanContext) context.Context {
	return context.WithValue(ctx, traceContextKey, sc)
}

// SpanFromContext extracts SpanContext from context.Context.
func SpanFromContext(ctx context.Context) (SpanContext, bool) {
	if ctx == nil {
		return SpanContext{}, false
	}
	sc, ok := ctx.Value(traceContextKey).(SpanContext)
	return sc, ok
}

// ParseTraceparent parses standard W3C traceparent header value.
func ParseTraceparent(header string) (SpanContext, bool) {
	parts := strings.Split(header, "-")
	if len(parts) != 4 || parts[0] != "00" {
		return SpanContext{}, false
	}
	if len(parts[1]) != 32 || len(parts[2]) != 16 || len(parts[3]) != 2 {
		return SpanContext{}, false
	}
	return SpanContext{
		TraceID:    parts[1],
		SpanID:     parts[2],
		TraceFlags: parts[3],
	}, true
}

// ExtractHTTP extracts or initializes SpanContext from HTTP request.
func ExtractHTTP(req *http.Request) SpanContext {
	header := req.Header.Get(HeaderTraceParent)
	if sc, ok := ParseTraceparent(header); ok {
		sc.TraceState = req.Header.Get(HeaderTraceState)
		return sc
	}
	return NewSpanContext()
}

// InjectHTTP injects SpanContext into outgoing HTTP request headers.
func InjectHTTP(ctx context.Context, req *http.Request) {
	if sc, ok := SpanFromContext(ctx); ok {
		req.Header.Set(HeaderTraceParent, sc.Traceparent())
		if sc.TraceState != "" {
			req.Header.Set(HeaderTraceState, sc.TraceState)
		}
	}
}

// InjectMap injects traceparent into generic string map (e.g. NATS/Kafka headers).
func InjectMap(ctx context.Context, carrier map[string]string) {
	if sc, ok := SpanFromContext(ctx); ok {
		carrier[HeaderTraceParent] = sc.Traceparent()
		if sc.TraceState != "" {
			carrier[HeaderTraceState] = sc.TraceState
		}
	}
}

// ExtractMap extracts SpanContext from generic string map.
func ExtractMap(carrier map[string]string) SpanContext {
	if val, ok := carrier[HeaderTraceParent]; ok {
		if sc, parsed := ParseTraceparent(val); parsed {
			sc.TraceState = carrier[HeaderTraceState]
			return sc
		}
	}
	return NewSpanContext()
}

func generateHex(byteLen int) string {
	b := make([]byte, byteLen)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
