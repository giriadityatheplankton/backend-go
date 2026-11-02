// Package httpserver builds the public HTTP server and router with the standard
// middleware chain, plus a separate admin server for probes, metrics and pprof
// so those never share the public port.
package httpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"time"

	"backend-go/internal/pkg/health"
	"backend-go/internal/pkg/httpserver/middleware"
	"backend-go/internal/pkg/httpserver/respond"
	"backend-go/internal/pkg/observability/metrics"
	"backend-go/internal/pkg/observability/telemetry"

	"github.com/gin-gonic/gin"
)

// Config tunes the http.Server. Every timeout is set: a zero ReadHeaderTimeout
// allows Slowloris, and a zero IdleTimeout leaks keep-alive connections.
type Config struct {
	Address           string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	MaxHeaderBytes    int
	ShutdownTimeout   time.Duration
}

func (c *Config) defaults() {
	if c.ReadHeaderTimeout <= 0 {
		c.ReadHeaderTimeout = 5 * time.Second
	}
	if c.ReadTimeout <= 0 {
		c.ReadTimeout = 10 * time.Second
	}
	if c.WriteTimeout <= 0 {
		c.WriteTimeout = 15 * time.Second
	}
	if c.IdleTimeout <= 0 {
		c.IdleTimeout = 60 * time.Second
	}
	if c.MaxHeaderBytes <= 0 {
		c.MaxHeaderBytes = 1 << 20
	}
	if c.ShutdownTimeout <= 0 {
		c.ShutdownTimeout = 10 * time.Second
	}
}

// Server runs an http.Server with fail-fast listening.
type Server struct {
	name string
	srv  *http.Server
	lis  net.Listener
	cfg  Config
}

// New creates a Server for handler.
func New(name string, cfg Config, handler http.Handler) *Server {
	cfg.defaults()
	return &Server{name: name, cfg: cfg, srv: &http.Server{
		Addr: cfg.Address, Handler: handler,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout, ReadTimeout: cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout, IdleTimeout: cfg.IdleTimeout, MaxHeaderBytes: cfg.MaxHeaderBytes,
	}}
}

// Listen binds now so a busy port fails startup instead of leaving a zombie process.
func (s *Server) Listen() error {
	if s.lis != nil {
		return nil
	}
	lis, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", s.cfg.Address)
	if err != nil {
		return fmt.Errorf("%s: listen on %s: %w", s.name, s.cfg.Address, err)
	}
	s.lis = lis
	return nil
}

// Addr returns the bound address.
func (s *Server) Addr() string {
	if s.lis == nil {
		return s.cfg.Address
	}
	return s.lis.Addr().String()
}

// Run serves until ctx is cancelled (graceful shutdown) or the listener fails.
func (s *Server) Run(ctx context.Context) error {
	if err := s.Listen(); err != nil { //nolint:contextcheck // binding a listener takes no context
		return err
	}
	errCh := make(chan error, 1)
	go func() {
		slog.Info("HTTP server is starting", "server", s.name, "address", s.lis.Addr().String())
		errCh <- s.srv.Serve(s.lis)
	}()
	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("%s: serve: %w", s.name, err)
		}
		return nil
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), s.cfg.ShutdownTimeout)
		defer cancel()
		if err := s.srv.Shutdown(shutCtx); err != nil { //nolint:contextcheck // ctx is cancelled: shutdown needs a fresh, bounded context
			_ = s.srv.Close()
			return fmt.Errorf("%s: shutdown: %w", s.name, err)
		}
		return nil
	}
}

// RouterOptions configures the public router.
type RouterOptions struct {
	Production     bool
	CORS           middleware.CORSConfig
	Metrics        *metrics.Metrics
	RequestTimeout time.Duration
	MaxBodyBytes   int64
	// TrustedProxies lists proxy CIDRs allowed to set X-Forwarded-For. Empty trusts
	// none, so a client cannot spoof its IP to dodge rate limits.
	TrustedProxies []string
}

// NewRouter builds a Gin engine with the standard chain. Order matters:
// trace and request-ID first so recovery and access logs carry them.
func NewRouter(o RouterOptions) (*gin.Engine, error) {
	if o.Production {
		gin.SetMode(gin.ReleaseMode)
	}
	respond.StrictJSON()
	r := gin.New()
	if err := r.SetTrustedProxies(o.TrustedProxies); err != nil {
		return nil, fmt.Errorf("invalid trusted proxies: %w", err)
	}
	r.HandleMethodNotAllowed = true

	r.Use(telemetry.HTTPMiddleware())
	r.Use(middleware.RequestID())
	r.Use(middleware.RecoveryMiddleware())
	r.Use(middleware.SecurityHeaders())
	r.Use(middleware.CORS(o.CORS))
	if o.Metrics != nil {
		r.Use(o.Metrics.HTTPMiddleware())
	}
	r.Use(middleware.Logger())
	r.Use(middleware.Timeout(o.RequestTimeout))
	if o.MaxBodyBytes > 0 {
		r.Use(middleware.BodyLimit(o.MaxBodyBytes))
	}
	r.NoRoute(func(c *gin.Context) { respond.Error(c, http.StatusNotFound, "NOT_FOUND", "Route not found") })
	r.NoMethod(func(c *gin.Context) {
		respond.Error(c, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed")
	})
	return r, nil
}

// AdminHandler serves probes, metrics and (optionally) pprof. Bind it to an
// internal-only port.
func AdminHandler(h *health.Manager, enablePprof bool) http.Handler {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(middleware.RecoveryMiddleware())
	r.GET("/healthz/live", h.LivenessHandler)
	r.GET("/healthz/ready", h.ReadinessHandler)
	r.GET("/metrics", metrics.Handler())
	if enablePprof {
		g := r.Group("/debug/pprof")
		g.GET("/", gin.WrapF(pprof.Index))
		g.GET("/cmdline", gin.WrapF(pprof.Cmdline))
		g.GET("/profile", gin.WrapF(pprof.Profile))
		g.GET("/symbol", gin.WrapF(pprof.Symbol))
		g.GET("/trace", gin.WrapF(pprof.Trace))
		g.GET("/:name", func(c *gin.Context) { pprof.Handler(c.Param("name")).ServeHTTP(c.Writer, c.Request) })
	}
	return r
}

// Middlewares are the cross-cutting handlers app/ builds once (they need Redis,
// auth, metrics) and hands to every module, so modules never construct
// infrastructure themselves.
type Middlewares struct {
	// Authenticate resolves the Principal; applied to the whole /v1 group by app/.
	Authenticate gin.HandlerFunc
	// RequireScope returns a handler enforcing a scope.
	RequireScope func(scope string) gin.HandlerFunc
	// RateLimit applies the shared limiter.
	RateLimit gin.HandlerFunc
	// Idempotent applies edge idempotency (layer 1); it is optional on the wire.
	Idempotent gin.HandlerFunc
	// IdempotentRequired is Idempotent but rejects requests without a key.
	IdempotentRequired gin.HandlerFunc
}

// Noop is a pass-through handler for tests.
func Noop(c *gin.Context) { c.Next() }

// NoopMiddlewares returns Middlewares that do nothing (unit tests).
func NoopMiddlewares() Middlewares {
	return Middlewares{
		Authenticate: Noop, RateLimit: Noop, Idempotent: Noop, IdempotentRequired: Noop,
		RequireScope: func(string) gin.HandlerFunc { return Noop },
	}
}
