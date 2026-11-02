package resilience_test

import (
	"context"
	"testing"
	"time"

	"backend-go/internal/platform/resilience"

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestGRPCRateLimitInterceptor(t *testing.T) {
	limiter := resilience.NewMemoryRateLimiter()
	interceptor := resilience.UnaryServerRateLimitInterceptor(resilience.LimitConfig{Limiter: limiter, Limit: 1, Window: 100 * time.Millisecond}, nil)

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

func TestGRPCRateLimitIgnoresSpoofedTenantMetadata(t *testing.T) {
	limiter := resilience.NewMemoryRateLimiter()
	ic := resilience.UnaryServerRateLimitInterceptor(resilience.LimitConfig{Limiter: limiter, Limit: 1, Window: time.Minute}, nil)
	h := func(context.Context, interface{}) (interface{}, error) { return "ok", nil }

	call := func(tenant string) error {
		ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-tenant-id", tenant))
		_, err := ic(ctx, nil, &grpc.UnaryServerInfo{}, h)
		return err
	}
	assert.NoError(t, call("a"))
	assert.Equal(t, codes.ResourceExhausted, status.Code(call("b")), "changing the header must not yield a fresh bucket")
}

func TestGRPCRateLimitNilLimiterDoesNotPanic(t *testing.T) {
	h := func(context.Context, interface{}) (interface{}, error) { return "ok", nil }
	open := resilience.UnaryServerRateLimitInterceptor(resilience.LimitConfig{FailOpen: true}, nil)
	_, err := open(context.Background(), nil, &grpc.UnaryServerInfo{}, h)
	assert.NoError(t, err)
	closed := resilience.UnaryServerRateLimitInterceptor(resilience.LimitConfig{}, nil)
	_, err = closed(context.Background(), nil, &grpc.UnaryServerInfo{}, h)
	assert.Equal(t, codes.Unavailable, status.Code(err))
}

func TestClientBreakerOnlyCountsInfrastructureFailures(t *testing.T) {
	cb := resilience.NewCircuitBreaker(resilience.CircuitBreakerConfig{Name: "t", ConsecutiveFailures: 2, Timeout: time.Minute})
	ic := resilience.UnaryClientCircuitBreakerInterceptor(cb)
	call := func(code codes.Code) error {
		return ic(context.Background(), "/s/m", nil, nil, nil, func(context.Context, string, interface{}, interface{}, *grpc.ClientConn, ...grpc.CallOption) error {
			return status.Error(code, "x")
		})
	}
	for i := 0; i < 5; i++ {
		assert.Equal(t, codes.NotFound, status.Code(call(codes.NotFound)))
	}
	assert.Equal(t, "closed", cb.CurrentState().String(), "NotFound must not trip the breaker")

	_ = call(codes.Unavailable)
	_ = call(codes.Unavailable)
	assert.Equal(t, "open", cb.CurrentState().String())
	assert.Equal(t, codes.Unavailable, status.Code(call(codes.NotFound)), "open breaker rejects without calling downstream")
}
