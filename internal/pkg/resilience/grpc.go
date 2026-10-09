package resilience

import (
	"context"
	"strings"
	"time"

	"github.com/sony/gobreaker/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// GRPCKeyExtractor determines rate limit key from gRPC context and request.
type GRPCKeyExtractor func(ctx context.Context, req interface{}) string

// DefaultGRPCTenantOrPeerKey extracts tenant or client peer address from gRPC metadata.
func DefaultGRPCTenantOrPeerKey(ctx context.Context, req interface{}) string {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if vals := md.Get("x-tenant-id"); len(vals) > 0 && vals[0] != "" {
			return "tenant:" + vals[0]
		}
		if vals := md.Get("x-user-id"); len(vals) > 0 && vals[0] != "" {
			return "user:" + vals[0]
		}
	}
	if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
		return "peer:" + strings.Split(p.Addr.String(), ":")[0]
	}
	return "grpc:default"
}

// UnaryServerRateLimitInterceptor enforces distributed rate limits on gRPC unary RPCs.
func UnaryServerRateLimitInterceptor(limiter RateLimiter, keyFunc GRPCKeyExtractor, limit int64, window time.Duration) grpc.UnaryServerInterceptor {
	if keyFunc == nil {
		keyFunc = DefaultGRPCTenantOrPeerKey
	}

	return func(
		ctx context.Context,
		req interface{},
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (interface{}, error) {
		key := keyFunc(ctx, req)
		allowed, _, _, err := limiter.Allow(ctx, key, limit, window)
		if err != nil {
			// Fail open on limiter infrastructure failure
			return handler(ctx, req)
		}

		if !allowed {
			return nil, status.Error(codes.ResourceExhausted, "rate limit exceeded; try again later")
		}

		return handler(ctx, req)
	}
}

// UnaryClientCircuitBreakerInterceptor wraps gRPC client calls with circuit breaker protection.
func UnaryClientCircuitBreakerInterceptor(cb *CircuitBreaker) grpc.UnaryClientInterceptor {
	return func(
		ctx context.Context,
		method string,
		req, reply interface{},
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) error {
		_, err := cb.Execute(
			ctx,
			func(callCtx context.Context) (interface{}, error) {
				return nil, invoker(callCtx, method, req, reply, cc, opts...)
			},
			nil,
		)
		if err != nil && cb.CurrentState() == gobreaker.StateOpen {
			return status.Error(codes.Unavailable, "circuit breaker open: downstream gRPC service unavailable")
		}
		return err
	}
}
