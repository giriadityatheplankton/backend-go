package idempotency_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"backend-go/internal/pkg/idempotency"

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func TestGRPCIdempotencyInterceptor(t *testing.T) {
	storage := idempotency.NewMemoryStorage()
	interceptor := idempotency.UnaryServerInterceptor(storage, 5*time.Second, 1*time.Minute)

	var callCount int32
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		atomic.AddInt32(&callCount, 1)
		return map[string]interface{}{"status": "success", "tx_id": "tx-100"}, nil
	}

	md := metadata.Pairs(idempotency.GRPCMetadataIdempotencyKey, "idem-grpc-1")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	// First RPC
	res1, err := interceptor(ctx, nil, &grpc.UnaryServerInfo{}, handler)
	assert.NoError(t, err)
	assert.NotNil(t, res1)
	assert.Equal(t, int32(1), atomic.LoadInt32(&callCount))

	// Second RPC with duplicate key
	res2, err := interceptor(ctx, nil, &grpc.UnaryServerInfo{}, handler)
	assert.NoError(t, err)
	assert.NotNil(t, res2)
	assert.Equal(t, int32(1), atomic.LoadInt32(&callCount), "Handler should not be called again")
}
