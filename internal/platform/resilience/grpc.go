package resilience

import (
	"context"
	"log/slog"
	"net"

	"backend-go/internal/platform/auth"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// GRPCKeyExtractor determines the rate-limit key from the gRPC context.
type GRPCKeyExtractor func(ctx context.Context, req interface{}) string

// GRPCPrincipalOrPeerKey keys on the authenticated principal, else the peer IP.
// Client-supplied metadata such as x-tenant-id is ignored on purpose.
func GRPCPrincipalOrPeerKey(ctx context.Context, _ interface{}) string {
	if p, ok := auth.From(ctx); ok {
		return p.Key()
	}
	if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
		host, _, err := net.SplitHostPort(p.Addr.String())
		if err != nil {
			host = p.Addr.String()
		}
		return "peer:" + host
	}
	return "grpc:unknown"
}

// UnaryServerRateLimitInterceptor enforces cfg on gRPC unary calls.
func UnaryServerRateLimitInterceptor(cfg LimitConfig, keyFunc GRPCKeyExtractor) grpc.UnaryServerInterceptor {
	if keyFunc == nil {
		keyFunc = GRPCPrincipalOrPeerKey
	}
	return func(ctx context.Context, req interface{}, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if cfg.Limiter == nil { // nil-guard: never panic on a missing backend
			cfg.observe("error")
			if cfg.FailOpen {
				return handler(ctx, req)
			}
			return nil, status.Error(codes.Unavailable, "rate limiting unavailable")
		}
		allowed, _, _, err := cfg.Limiter.Allow(ctx, keyFunc(ctx, req), cfg.Limit, cfg.Window)
		if err != nil {
			cfg.observe("error")
			slog.ErrorContext(ctx, "Rate limiter failed", "error", err, "fail_open", cfg.FailOpen)
			if cfg.FailOpen {
				return handler(ctx, req)
			}
			return nil, status.Error(codes.Unavailable, "rate limiting unavailable")
		}
		if !allowed {
			cfg.observe("denied")
			return nil, status.Error(codes.ResourceExhausted, "rate limit exceeded; try again later")
		}
		cfg.observe("allowed")
		return handler(ctx, req)
	}
}

// IsInfraFailure reports whether a gRPC error indicates a sick dependency
// (and should therefore count against a circuit breaker) rather than a caller
// mistake such as InvalidArgument or NotFound.
func IsInfraFailure(err error) bool {
	if err == nil {
		return false
	}
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded, codes.Internal, codes.ResourceExhausted, codes.Unknown, codes.DataLoss, codes.Aborted:
		return true
	}
	return false
}

// UnaryClientCircuitBreakerInterceptor wraps client calls with breaker protection.
// Only infrastructure failures trip the breaker; business errors pass through.
func UnaryClientCircuitBreakerInterceptor(cb *CircuitBreaker) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		var callErr error
		_, brErr := cb.Execute(ctx, func(callCtx context.Context) (interface{}, error) {
			callErr = invoker(callCtx, method, req, reply, cc, opts...)
			if IsInfraFailure(callErr) {
				return nil, callErr
			}
			return nil, nil
		}, nil)
		if IsOpen(brErr) {
			return status.Error(codes.Unavailable, "circuit breaker open: downstream gRPC service unavailable")
		}
		if callErr != nil {
			return callErr
		}
		return brErr
	}
}
