package grpcclient_test

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"backend-go/internal/platform/grpcclient"
	"backend-go/internal/platform/idempotency"
	"backend-go/internal/platform/resilience"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// fakeUsers reuses the standard health service as a tiny unary RPC: the request's
// Service field plays the role of an ID, so the test needs no generated code.
type fakeUsers struct {
	healthpb.UnimplementedHealthServer
	calls     int32
	failFirst int32
	lastKey   atomic.Value
	delay     time.Duration
}

func (f *fakeUsers) Check(ctx context.Context, req *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	n := atomic.AddInt32(&f.calls, 1)
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if v := md.Get("idempotency-key"); len(v) > 0 {
			f.lastKey.Store(v[0])
		}
	}
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, status.FromContextError(ctx.Err()).Err()
		}
	}
	switch {
	case req.Service == "404":
		return nil, status.Error(codes.NotFound, "no")
	case n <= f.failFirst:
		return nil, status.Error(codes.Unavailable, "warming up")
	}
	return &healthpb.HealthCheckResponse{Status: healthpb.HealthCheckResponse_SERVING}, nil
}

func serve(t *testing.T, f *fakeUsers) string {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := grpc.NewServer()
	healthpb.RegisterHealthServer(srv, f)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis.Addr().String()
}

func call(cl healthpb.HealthClient, ctx context.Context, id string) (*healthpb.HealthCheckResponse, error) {
	return cl.Check(ctx, &healthpb.HealthCheckRequest{Service: id})
}

func TestDialRequiresExplicitTransportSecurity(t *testing.T) {
	_, err := grpcclient.Dial("localhost:1", grpcclient.Options{})
	assert.Error(t, err)
	_, err = grpcclient.Dial("", grpcclient.Options{Insecure: true})
	assert.Error(t, err)
}

func TestDefaultDeadlineIsApplied(t *testing.T) {
	addr := serve(t, &fakeUsers{delay: 2 * time.Second})
	conn, err := grpcclient.Dial(addr, grpcclient.Options{Insecure: true, DefaultTimeout: 100 * time.Millisecond})
	require.NoError(t, err)
	defer conn.Close()

	start := time.Now()
	_, err = call(healthpb.NewHealthClient(conn), context.Background(), "1")
	assert.Equal(t, codes.DeadlineExceeded, status.Code(err))
	assert.Less(t, time.Since(start), time.Second)
}

func TestRetriesTransientUnavailable(t *testing.T) {
	f := &fakeUsers{failFirst: 2}
	addr := serve(t, f)
	conn, err := grpcclient.Dial(addr, grpcclient.Options{Insecure: true, Retry: true, MaxAttempts: 4})
	require.NoError(t, err)
	defer conn.Close()

	res, err := call(healthpb.NewHealthClient(conn), context.Background(), "7")
	require.NoError(t, err)
	assert.Equal(t, healthpb.HealthCheckResponse_SERVING, res.Status)
	assert.EqualValues(t, 3, atomic.LoadInt32(&f.calls))
}

func TestBreakerOpensOnOutageButNotOnBusinessErrors(t *testing.T) {
	f := &fakeUsers{failFirst: 1000}
	addr := serve(t, f)
	br := resilience.NewCircuitBreaker(resilience.CircuitBreakerConfig{Name: "users", ConsecutiveFailures: 3, Timeout: time.Minute})
	conn, err := grpcclient.Dial(addr, grpcclient.Options{Name: "users", Insecure: true, Breaker: br})
	require.NoError(t, err)
	defer conn.Close()
	cl := healthpb.NewHealthClient(conn)

	for i := 0; i < 10; i++ {
		_, err := call(cl, context.Background(), "404")
		assert.Equal(t, codes.NotFound, status.Code(err))
	}
	assert.Equal(t, "closed", br.CurrentState().String())

	for i := 0; i < 3; i++ {
		_, _ = call(cl, context.Background(), "1")
	}
	assert.Equal(t, "open", br.CurrentState().String())

	before := atomic.LoadInt32(&f.calls)
	_, err = call(cl, context.Background(), "1")
	assert.Equal(t, codes.Unavailable, status.Code(err))
	assert.Equal(t, before, atomic.LoadInt32(&f.calls), "open breaker must not reach the dependency")
}

func TestDerivedIdempotencyKeyIsPropagated(t *testing.T) {
	f := &fakeUsers{}
	addr := serve(t, f)
	conn, err := grpcclient.Dial(addr, grpcclient.Options{Name: "users", Insecure: true, PropagateIdempotency: true})
	require.NoError(t, err)
	defer conn.Close()
	cl := healthpb.NewHealthClient(conn)

	ctx := idempotency.WithKey(context.Background(), "client-key-9")
	_, err = call(cl, ctx, "1")
	require.NoError(t, err)
	assert.Equal(t, "client-key-9:users", f.lastKey.Load())

	f.lastKey.Store("")
	_, err = call(cl, context.Background(), "1")
	require.NoError(t, err)
	assert.Equal(t, "", f.lastKey.Load(), "no inbound key, nothing to derive")
}
