// Package grpcclient builds outbound gRPC connections with the resilience every
// service-to-service call needs: a default deadline, retries for transient
// failures, a circuit breaker, trace and request-ID propagation, derived
// idempotency keys, and keepalive.
package grpcclient

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"time"

	"backend-go/internal/platform/idempotency"
	"backend-go/internal/platform/observability/telemetry"
	"backend-go/internal/platform/resilience"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
)

// Options configures a connection.
type Options struct {
	// Name labels the dependency (breaker name, derived idempotency key suffix), e.g. "payment".
	Name string
	// DefaultTimeout is applied to calls whose context has no deadline.
	DefaultTimeout time.Duration
	// Retry enables transparent retries of UNAVAILABLE. Only enable it for RPCs that
	// are idempotent; the derived idempotency key makes mutating RPCs safe to retry.
	Retry       bool
	MaxAttempts int
	Breaker     *resilience.CircuitBreaker
	// TLS: set UseTLS for server-auth TLS; Insecure is allowed only for local/mesh use.
	UseTLS   bool
	Insecure bool
	// PropagateIdempotency adds "<clientKey>:<Name>" as the outgoing idempotency key.
	PropagateIdempotency bool
	// PerRPCCredentials attaches auth (e.g. a service token) to every call.
	PerRPCCredentials credentials.PerRPCCredentials
	ExtraDialOptions  []grpc.DialOption
}

func serviceConfig(maxAttempts int) string {
	return fmt.Sprintf(`{
  "methodConfig": [{
    "name": [{}],
    "retryPolicy": {
      "maxAttempts": %d,
      "initialBackoff": "0.1s",
      "maxBackoff": "2s",
      "backoffMultiplier": 2,
      "retryableStatusCodes": ["UNAVAILABLE"]
    }
  }]
}`, maxAttempts)
}

// Dial creates a lazily-connecting client connection.
func Dial(target string, o Options) (*grpc.ClientConn, error) {
	if target == "" {
		return nil, errors.New("grpcclient: target is required")
	}
	if o.DefaultTimeout <= 0 {
		o.DefaultTimeout = 5 * time.Second
	}
	if o.MaxAttempts < 2 {
		o.MaxAttempts = 3
	}

	var creds credentials.TransportCredentials
	switch {
	case o.UseTLS:
		creds = credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})
	case o.Insecure:
		creds = insecure.NewCredentials()
	default:
		return nil, errors.New("grpcclient: choose UseTLS or Insecure explicitly")
	}

	// Order matters: deadline first, then the breaker sees the final outcome,
	// and propagation decorates the outgoing metadata.
	unary := []grpc.UnaryClientInterceptor{deadlineInterceptor(o.DefaultTimeout)}
	if o.Breaker != nil {
		unary = append(unary, resilience.UnaryClientCircuitBreakerInterceptor(o.Breaker))
	}
	unary = append(unary, telemetry.UnaryClientTraceInterceptor(), propagateInterceptor(o))

	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(creds),
		grpc.WithChainUnaryInterceptor(unary...),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: 2 * time.Minute, Timeout: 20 * time.Second}),
	}
	if o.Retry {
		opts = append(opts, grpc.WithDefaultServiceConfig(serviceConfig(o.MaxAttempts)))
	}
	if o.PerRPCCredentials != nil {
		opts = append(opts, grpc.WithPerRPCCredentials(o.PerRPCCredentials))
	}
	opts = append(opts, o.ExtraDialOptions...)
	return grpc.NewClient(target, opts...)
}

func deadlineInterceptor(d time.Duration) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		if _, ok := ctx.Deadline(); !ok {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, d)
			defer cancel()
		}
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

// propagateInterceptor forwards the derived idempotency key.
func propagateInterceptor(o Options) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		if o.PropagateIdempotency {
			if md, ok := metadata.FromOutgoingContext(ctx); !ok || len(md.Get(idempotency.MetadataIdempotencyKey)) == 0 {
				if k := idempotency.DerivedKey(ctx, o.Name); k != "" {
					ctx = metadata.AppendToOutgoingContext(ctx, idempotency.MetadataIdempotencyKey, k)
				}
			}
		}
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}
