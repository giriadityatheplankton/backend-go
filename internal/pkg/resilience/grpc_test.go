package resilience_test

import (
	"context"
	"testing"
	"time"

	"backend-go/internal/pkg/resilience"

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestGRPCRateLimitInterceptor(t *testing.T) {
	limiter := resilience.NewMemoryRateLimiter()
	interceptor := resilience.UnaryServerRateLimitInterceptor(limiter, nil, 1, 100*time.Millisecond)

	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return "ok", nil
	}

	md := metadata.Pairs("x-tenant-id", "tenant-xyz")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	// 1st request succeeds
	res, err := interceptor(ctx, nil, &grpc.UnaryServerInfo{}, handler)
	assert.NoError(t, err)
	assert.Equal(t, "ok", res)

	// 2nd request exceeded
	_, err = interceptor(ctx, nil, &grpc.UnaryServerInfo{}, handler)
	assert.Error(t, err)
	st, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.ResourceExhausted, st.Code())
}
