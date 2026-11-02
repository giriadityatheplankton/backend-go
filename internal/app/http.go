package app

import (
	"time"

	"backend-go/internal/pkg/auth"
	"backend-go/internal/pkg/httpserver"
	"backend-go/internal/pkg/httpserver/middleware"
	"backend-go/internal/pkg/idempotency"
	"backend-go/internal/pkg/resilience"

	"github.com/gin-gonic/gin"
)

// edge groups the cross-cutting policies shared by HTTP and gRPC.
type edge struct {
	authenticator auth.Authenticator
	store         idempotency.Store
	failureMode   idempotency.FailureMode
	limiter       resilience.RateLimiter
}

func (a *App) buildEdge() (*edge, error) {
	authn, err := buildAuthenticator(a.cfg)
	if err != nil {
		return nil, err
	}
	mode, err := idempotency.ParseFailureMode(a.cfg.IdempotencyFailureMode)
	if err != nil {
		return nil, err
	}
	e := &edge{authenticator: authn, failureMode: mode}
	if a.infra.redisState != nil {
		e.store = idempotency.NewRedisStore(a.infra.redisState, a.cfg.ServiceName+":")
		e.limiter = resilience.NewRedisSlidingWindowLimiter(a.infra.redisState, a.cfg.ServiceName+":")
	} else {
		e.store = unavailableStore{}
		e.limiter = resilience.NewRedisSlidingWindowLimiter(nil) // errors -> RateLimitFailOpen decides
	}
	return e, nil
}

func (a *App) idempotencyHTTP(e *edge, required bool) gin.HandlerFunc {
	return idempotency.HTTPMiddleware(idempotency.HTTPConfig{
		Store:        e.store,
		Scope:        func(c *gin.Context) string { return auth.ScopeKey(c.Request.Context()) },
		Required:     required,
		FailureMode:  e.failureMode,
		InFlightTTL:  a.cfg.IdempotencyInFlightTTL,
		CompletedTTL: a.cfg.IdempotencyTTL,
		MaxBodyBytes: a.cfg.MaxBodyBytes,
		Observer:     a.infra.metrics,
	})
}

func (a *App) limitConfig(e *edge) resilience.LimitConfig {
	return resilience.LimitConfig{
		Limiter: e.limiter, Limit: a.cfg.RateLimitRequests, Window: a.cfg.RateLimitWindow,
		FailOpen: a.cfg.RateLimitFailOpen, Observer: a.infra.metrics,
	}
}

// buildHTTP assembles the public router and the admin server.
func (a *App) buildHTTP(e *edge) error {
	origins := a.cfg.CORSAllowedOrigins
	corsCfg := middleware.DefaultCORSConfig()
	corsCfg.AllowedOrigins, corsCfg.AllowCredentials = origins, a.cfg.CORSAllowCredentials

	router, err := httpserver.NewRouter(httpserver.RouterOptions{
		Production: a.cfg.IsProduction(), CORS: corsCfg, Metrics: a.infra.metrics,
		RequestTimeout: a.cfg.HTTPRequestTimeout, MaxBodyBytes: a.cfg.MaxBodyBytes, TrustedProxies: a.cfg.TrustedProxies,
	})
	if err != nil {
		return err
	}

	mw := httpserver.Middlewares{
		Authenticate:       auth.HTTPMiddleware(e.authenticator),
		RequireScope:       func(scope string) gin.HandlerFunc { return auth.RequireScope(e.authenticator, scope) },
		RateLimit:          resilience.HTTPRateLimit(a.limitConfig(e), nil),
		Idempotent:         a.idempotencyHTTP(e, false),
		IdempotentRequired: a.idempotencyHTTP(e, true),
	}
	v1 := router.Group("/v1", mw.Authenticate)
	for _, m := range a.modules {
		m.RegisterHTTP(v1, mw)
	}

	a.httpSrv = httpserver.New("http", httpserver.Config{
		Address: a.cfg.HTTPAddress, ReadHeaderTimeout: a.cfg.ReadHeaderTimeout, ReadTimeout: a.cfg.ReadTimeout,
		WriteTimeout: a.cfg.WriteTimeout, IdleTimeout: a.cfg.IdleTimeout, ShutdownTimeout: a.cfg.ShutdownTimeout,
	}, router)
	a.adminSrv = a.buildAdmin()
	return nil
}

// buildAdmin serves probes, metrics and pprof on a separate, internal-only port.
func (a *App) buildAdmin() *httpserver.Server {
	a.infra.health.Verbose = !a.cfg.IsProduction()
	return httpserver.New("admin", httpserver.Config{
		Address: a.cfg.AdminAddress, WriteTimeout: 60 * time.Second, ReadTimeout: 15 * time.Second, ShutdownTimeout: 5 * time.Second,
	}, httpserver.AdminHandler(a.infra.health, a.cfg.PprofEnabled))
}
