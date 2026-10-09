package telemetry_test

import (
	"context"
	"testing"

	"backend-go/internal/pkg/telemetry"

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func TestGRPCTraceInterceptors(t *testing.T) {
	// Server Interceptor
	interceptor := telemetry.UnaryServerTraceInterceptor()
	md := metadata.Pairs(telemetry.HeaderTraceParent, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	incomingCtx := metadata.NewIncomingContext(context.Background(), md)

	var extractedTraceID string
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		sc, ok := telemetry.SpanFromContext(ctx)
		if ok {
			extractedTraceID = sc.TraceID
		}
		return "ok", nil
	}

	_, err := interceptor(incomingCtx, nil, &grpc.UnaryServerInfo{}, handler)
	assert.NoError(t, err)
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", extractedTraceID)
}
