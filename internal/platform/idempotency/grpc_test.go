package idempotency_test

import (
	"context"
	"sync/atomic"
	"testing"

	"backend-go/internal/platform/idempotency"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func grpcCtx(key string) context.Context {
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs("idempotency-key", key))
}

var info = &grpc.UnaryServerInfo{FullMethod: "/order.v1.OrderService/CreateOrder"}

func TestGRPC_ReplaysResponseTypeNotRequestType(t *testing.T) {
	var calls int32
	ic := idempotency.UnaryServerInterceptor(idempotency.GRPCConfig{Store: idempotency.NewMemoryStore()})
	handler := func(context.Context, any) (any, error) {
		atomic.AddInt32(&calls, 1)
		return wrapperspb.Int64(42), nil // response type differs from request type
	}
	req := wrapperspb.String("create")

	first, err := ic(grpcCtx("k"), req, info, handler)
	require.NoError(t, err)
	second, err := ic(grpcCtx("k"), req, info, handler)
	require.NoError(t, err)

	assert.Equal(t, int32(1), atomic.LoadInt32(&calls))
	got, ok := second.(*wrapperspb.Int64Value)
	require.True(t, ok, "replay must have the RESPONSE type, got %T", second)
	assert.Equal(t, int64(42), got.Value)
	assert.Equal(t, first.(*wrapperspb.Int64Value).Value, got.Value)
}

func TestGRPC_FingerprintMismatch(t *testing.T) {
	ic := idempotency.UnaryServerInterceptor(idempotency.GRPCConfig{Store: idempotency.NewMemoryStore()})
	h := func(context.Context, any) (any, error) { return wrapperspb.Int64(1), nil }
	_, err := ic(grpcCtx("k"), wrapperspb.String("a"), info, h)
	require.NoError(t, err)
	_, err = ic(grpcCtx("k"), wrapperspb.String("b"), info, h)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestGRPC_DeterministicErrorReplayedTransientReleased(t *testing.T) {
	var calls int32
	ic := idempotency.UnaryServerInterceptor(idempotency.GRPCConfig{Store: idempotency.NewMemoryStore()})
	notFound := func(context.Context, any) (any, error) {
		atomic.AddInt32(&calls, 1)
		return nil, status.Error(codes.NotFound, "nope")
	}
	_, e1 := ic(grpcCtx("a"), wrapperspb.String("x"), info, notFound)
	_, e2 := ic(grpcCtx("a"), wrapperspb.String("x"), info, notFound)
	assert.Equal(t, codes.NotFound, status.Code(e1))
	assert.Equal(t, codes.NotFound, status.Code(e2))
	assert.Equal(t, "nope", status.Convert(e2).Message())
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls))

	atomic.StoreInt32(&calls, 0)
	unavailable := func(context.Context, any) (any, error) {
		atomic.AddInt32(&calls, 1)
		return nil, status.Error(codes.Unavailable, "down")
	}
	_, _ = ic(grpcCtx("b"), wrapperspb.String("x"), info, unavailable)
	_, _ = ic(grpcCtx("b"), wrapperspb.String("x"), info, unavailable)
	assert.Equal(t, int32(2), atomic.LoadInt32(&calls), "transient errors must not be cached")
}

func TestGRPC_StoreDownDoesNotPanic(t *testing.T) {
	h := func(context.Context, any) (any, error) { return wrapperspb.Int64(1), nil }
	closed := idempotency.UnaryServerInterceptor(idempotency.GRPCConfig{Store: downStore{}, FailureMode: idempotency.FailClosed})
	_, err := closed(grpcCtx("k"), wrapperspb.String("x"), info, h)
	assert.Equal(t, codes.Unavailable, status.Code(err))

	open := idempotency.UnaryServerInterceptor(idempotency.GRPCConfig{Store: downStore{}, FailureMode: idempotency.FailOpen})
	_, err = open(grpcCtx("k"), wrapperspb.String("x"), info, h)
	assert.NoError(t, err)
}

func TestGRPC_ScopeIsolationAndRequired(t *testing.T) {
	var calls int32
	scope := "a"
	ic := idempotency.UnaryServerInterceptor(idempotency.GRPCConfig{
		Store:    idempotency.NewMemoryStore(),
		Scope:    func(context.Context) string { return scope },
		Required: func(string) bool { return true },
	})
	h := func(context.Context, any) (any, error) { atomic.AddInt32(&calls, 1); return wrapperspb.Int64(1), nil }
	_, _ = ic(grpcCtx("k"), wrapperspb.String("x"), info, h)
	scope = "b"
	_, _ = ic(grpcCtx("k"), wrapperspb.String("x"), info, h)
	assert.Equal(t, int32(2), atomic.LoadInt32(&calls))

	_, err := ic(context.Background(), wrapperspb.String("x"), info, h)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}
