// Package grpcserver builds a production-ready gRPC server: bounded messages and
// streams, keepalive, optional TLS, health service wired to readiness,
// reflection (non-production only) and a graceful stop that cannot hang.
package grpcserver

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/reflection"
)

// Config tunes the server.
type Config struct {
	Address              string
	MaxRecvMsgBytes      int
	MaxSendMsgBytes      int
	MaxConcurrentStreams uint32
	// KeepaliveMinTime is the smallest client ping interval the server tolerates.
	KeepaliveMinTime  time.Duration
	KeepaliveInterval time.Duration
	KeepaliveTimeout  time.Duration
	MaxConnectionAge  time.Duration
	// ShutdownTimeout bounds GracefulStop before forcing Stop (open streams can block it forever).
	ShutdownTimeout time.Duration
	// EnableReflection must stay false in production.
	EnableReflection bool
	// TLS: when both are set the server terminates TLS (otherwise rely on a mesh/mTLS sidecar).
	TLSCertFile string
	TLSKeyFile  string
}

func (c *Config) defaults() {
	if c.MaxRecvMsgBytes <= 0 {
		c.MaxRecvMsgBytes = 4 << 20
	}
	if c.MaxSendMsgBytes <= 0 {
		c.MaxSendMsgBytes = 4 << 20
	}
	if c.MaxConcurrentStreams == 0 {
		c.MaxConcurrentStreams = 250
	}
	if c.KeepaliveMinTime <= 0 {
		c.KeepaliveMinTime = 30 * time.Second
	}
	if c.KeepaliveInterval <= 0 {
		c.KeepaliveInterval = 2 * time.Minute
	}
	if c.KeepaliveTimeout <= 0 {
		c.KeepaliveTimeout = 20 * time.Second
	}
	if c.MaxConnectionAge <= 0 {
		c.MaxConnectionAge = 30 * time.Minute // forces rebalancing across pods
	}
	if c.ShutdownTimeout <= 0 {
		c.ShutdownTimeout = 10 * time.Second
	}
}

// Server wraps *grpc.Server with lifecycle and health.
type Server struct {
	*grpc.Server
	cfg    Config
	lis    net.Listener
	health *health.Server
}

// New creates the server. Register services on s.Server, then call Run.
func New(cfg Config, unary []grpc.UnaryServerInterceptor, stream []grpc.StreamServerInterceptor) (*Server, error) {
	cfg.defaults()
	if cfg.Address == "" {
		return nil, errors.New("grpcserver: address is required")
	}
	opts := []grpc.ServerOption{
		grpc.MaxRecvMsgSize(cfg.MaxRecvMsgBytes),
		grpc.MaxSendMsgSize(cfg.MaxSendMsgBytes),
		grpc.MaxConcurrentStreams(cfg.MaxConcurrentStreams),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: cfg.KeepaliveMinTime, PermitWithoutStream: false}),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time: cfg.KeepaliveInterval, Timeout: cfg.KeepaliveTimeout,
			MaxConnectionAge: cfg.MaxConnectionAge, MaxConnectionAgeGrace: 30 * time.Second,
		}),
		grpc.ChainUnaryInterceptor(unary...),
		grpc.ChainStreamInterceptor(stream...),
	}
	if cfg.TLSCertFile != "" || cfg.TLSKeyFile != "" {
		cert, err := tls.LoadX509KeyPair(cfg.TLSCertFile, cfg.TLSKeyFile)
		if err != nil {
			return nil, fmt.Errorf("grpcserver: load TLS key pair: %w", err)
		}
		opts = append(opts, grpc.Creds(credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})))
	}

	s := &Server{Server: grpc.NewServer(opts...), cfg: cfg, health: health.NewServer()}
	healthpb.RegisterHealthServer(s.Server, s.health)
	if cfg.EnableReflection {
		reflection.Register(s.Server)
	}
	return s, nil
}

// Listen binds the address now so a port conflict fails startup immediately.
func (s *Server) Listen() error {
	if s.lis != nil {
		return nil
	}
	lis, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", s.cfg.Address)
	if err != nil {
		return fmt.Errorf("grpcserver: listen on %s: %w", s.cfg.Address, err)
	}
	s.lis = lis
	return nil
}

// Addr returns the bound address (after Listen), useful with ":0" in tests.
func (s *Server) Addr() string {
	if s.lis == nil {
		return s.cfg.Address
	}
	return s.lis.Addr().String()
}

// SetServing mirrors readiness into the gRPC health service so Kubernetes' native
// grpc probe and client-side load balancers see the same state as HTTP.
func (s *Server) SetServing(ok bool) {
	st := healthpb.HealthCheckResponse_NOT_SERVING
	if ok {
		st = healthpb.HealthCheckResponse_SERVING
	}
	s.health.SetServingStatus("", st)
}

// Run serves until ctx is cancelled or Serve fails. On cancellation it drains
// gracefully, then forces Stop after ShutdownTimeout.
func (s *Server) Run(ctx context.Context) error {
	if err := s.Listen(); err != nil { //nolint:contextcheck // binding a listener takes no context
		return err
	}
	s.SetServing(true)
	errCh := make(chan error, 1)
	go func() {
		slog.Info("gRPC server is starting", "address", s.lis.Addr().String())
		errCh <- s.Serve(s.lis)
	}()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			return fmt.Errorf("grpcserver: serve: %w", err)
		}
		return nil
	case <-ctx.Done():
		return s.Shutdown()
	}
}

// Shutdown stops accepting calls, drains, and force-stops after ShutdownTimeout.
func (s *Server) Shutdown() error {
	s.SetServing(false)
	done := make(chan struct{})
	go func() { s.GracefulStop(); close(done) }()
	select {
	case <-done:
		return nil
	case <-time.After(s.cfg.ShutdownTimeout):
		s.Stop()
		return fmt.Errorf("grpcserver: graceful stop exceeded %s; forced stop", s.cfg.ShutdownTimeout)
	}
}
