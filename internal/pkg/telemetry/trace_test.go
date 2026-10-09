package telemetry_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"backend-go/internal/pkg/telemetry"

	"github.com/stretchr/testify/assert"
)

func TestTraceContextPropagation(t *testing.T) {
	// 1. Root span generation
	sc := telemetry.NewSpanContext()
	assert.Len(t, sc.TraceID, 32)
	assert.Len(t, sc.SpanID, 16)
	assert.Equal(t, "01", sc.TraceFlags)

	tp := sc.Traceparent()
	assert.Contains(t, tp, "00-"+sc.TraceID)

	// 2. Child span generation
	child := sc.NewChildSpan()
	assert.Equal(t, sc.TraceID, child.TraceID)
	assert.NotEqual(t, sc.SpanID, child.SpanID)

	// 3. Context embedding & extraction
	ctx := telemetry.ContextWithSpan(context.Background(), sc)
	extracted, ok := telemetry.SpanFromContext(ctx)
	assert.True(t, ok)
	assert.Equal(t, sc.TraceID, extracted.TraceID)

	// 4. HTTP Header injection & extraction
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	telemetry.InjectHTTP(ctx, req)
	assert.NotEmpty(t, req.Header.Get(telemetry.HeaderTraceParent))

	scFromHTTP := telemetry.ExtractHTTP(req)
	assert.Equal(t, sc.TraceID, scFromHTTP.TraceID)

	// 5. Generic Map carrier (NATS / Kafka / Broker)
	carrier := make(map[string]string)
	telemetry.InjectMap(ctx, carrier)
	assert.NotEmpty(t, carrier[telemetry.HeaderTraceParent])

	scFromMap := telemetry.ExtractMap(carrier)
	assert.Equal(t, sc.TraceID, scFromMap.TraceID)
}
