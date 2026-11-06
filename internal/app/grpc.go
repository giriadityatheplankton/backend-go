package app

import (
	"backend-go/internal/pkg/auth"
	"backend-go/internal/pkg/grpcserver"
	"backend-go/internal/pkg/httpserver/middleware"
	"backend-go/internal/pkg/idempotency"
	"backend-go/internal/pkg/observability/telemetry"
	"backend-go/internal/pkg/resilience"

	"google.golang.org/grpc"
)

// exemptFromAuth lists methods that must work without credentials (probes, tooling).
var exemptFromAuth = []string{
	"/grpc.health.v1.Health/Check",
	"/grpc.health.v1.Health/Watch",
	"/grpc.reflection.v1.ServerReflection/ServerReflectionInfo",
	"/grpc.reflection.v1alpha.ServerReflection/ServerReflectionInfo",
}

func (a *App) buildGRPC(e *edge) error {
	// Order: recover panics, then trace/request-id, metrics, deadline, authenticate
	// (principal needed by the next two), rate limit, idempotency.
	unary := []grpc.UnaryServerInterceptor{
		middleware.GRPCUnaryRecoveryInterceptor(),
		telemetry.UnaryServerTraceInterceptor(),
		a.infra.metrics.GRPCUnaryInterceptor(),
		middleware.GRPCUnaryTimeoutInterceptor(a.cfg.GRPCRequestTimeout),
		auth.UnaryServerInterceptor(e.authenticator, exemptFromAuth...),
		resilience.UnaryServerRateLimitInterceptor(a.limitConfig(e), nil),
		idempotency.UnaryServerInterceptor(idempotency.GRPCConfig{
			Store: e.store, Scope: auth.ScopeKey, FailureMode: e.failureMode,
			InFlightTTL: a.cfg.IdempotencyInFlightTTL, CompletedTTL: a.cfg.IdempotencyTTL, Observer: a.infra.metrics,
		}),
	}
	stream := []grpc.StreamServerInterceptor{
		middleware.GRPCStreamRecoveryInterceptor(),
		telemetry.StreamServerTraceInterceptor(),
		auth.StreamServerInterceptor(e.authenticator, exemptFromAuth...),
	}
	srv, err := grpcserver.New(grpcserver.Config{
		Address: a.cfg.GRPCAddress, EnableReflection: a.cfg.GRPCReflection,
		TLSCertFile: a.cfg.GRPCTLSCertFile, TLSKeyFile: a.cfg.GRPCTLSKeyFile, ShutdownTimeout: a.cfg.ShutdownTimeout,
	}, unary, stream)
	if err != nil {
		return err
	}
	for _, m := range a.modules {
		m.RegisterGRPC(srv.Server)
	}
	a.grpcSrv = srv
	return nil
}
