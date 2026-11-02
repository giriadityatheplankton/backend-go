// Package app is the composition root ("Main component"): the one place where
// configuration, infrastructure, platform and modules meet. Everything below
// receives its dependencies through constructors; nothing reaches for globals.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"backend-go/internal/config"
	"backend-go/internal/platform/grpcserver"
	"backend-go/internal/platform/httpserver"
	"backend-go/internal/platform/shutdown"
)

// Role selects which parts of the service a process runs.
type Role int

const (
	RoleAPI    Role = 1 << iota // HTTP + gRPC servers
	RoleWorker                  // outbox relay, consumers, jobs
	RoleAll    = RoleAPI | RoleWorker
)

// App is a fully wired service. Create it with New and block in Run.
type App struct {
	cfg  *config.Config
	role Role

	infra   *infra
	modules []Module

	httpSrv  *httpserver.Server
	adminSrv *httpserver.Server
	grpcSrv  *grpcserver.Server
	workers  []runner

	mu       sync.Mutex
	firstErr error
	failCtx  context.Context
	fail     context.CancelFunc
}

// New opens connections and builds every component. It fails fast: a bad config,
// an unreachable required dependency or a busy port stops startup with a clear error.
func New(ctx context.Context, cfg *config.Config, role Role) (*App, error) {
	in, err := openInfra(ctx, cfg)
	if err != nil {
		return nil, err
	}
	a := &App{cfg: cfg, role: role, infra: in}
	a.failCtx, a.fail = context.WithCancel(context.Background())

	a.modules = buildModules(&moduleDeps{
		DB: in.db, Tx: in.tx, Cache: in.cache, CacheTTL: cfg.CacheTTL, OutboxPartitions: cfg.OutboxPartitions,
	})
	for _, m := range a.modules {
		slog.Info("Module loaded", "module", m.Name())
	}

	e, err := a.buildEdge()
	if err != nil {
		_ = in.close()
		return nil, err
	}
	if role&RoleAPI != 0 {
		if err := a.buildHTTP(e); err != nil { //nolint:contextcheck // builds handlers only; request contexts come from each request
			_ = in.close()
			return nil, err
		}
		if err := a.buildGRPC(e); err != nil {
			_ = in.close()
			return nil, err
		}
	} else {
		a.adminSrv = a.buildAdmin() // workers still expose probes and metrics
	}
	if role&RoleWorker != 0 {
		a.workers = a.buildWorkers()
	}

	// Bind listeners now so a port conflict aborts startup instead of leaving a zombie.
	for _, l := range []interface{ Listen() error }{a.adminSrv, a.httpSrv, a.grpcSrv} {
		if isNil(l) {
			continue
		}
		if err := l.Listen(); err != nil {
			_ = in.close()
			return nil, err
		}
	}
	return a, nil
}

func isNil(l interface{ Listen() error }) bool {
	switch v := l.(type) {
	case *httpserver.Server:
		return v == nil
	case *grpcserver.Server:
		return v == nil
	}
	return l == nil
}

func (a *App) setErr(err error) {
	if err == nil {
		return
	}
	a.mu.Lock()
	if a.firstErr == nil {
		a.firstErr = err
	}
	a.mu.Unlock()
	a.fail() // any component dying takes the whole process down with a non-zero exit
}

// Run starts every component and blocks until SIGINT/SIGTERM or a component fails.
//
// Shutdown order (see platform/shutdown): flip readiness -> drain delay -> stop
// servers -> stop workers -> close pools. Workers outlive servers so in-flight
// requests can still enqueue outbox events, and pools outlive both.
func (a *App) Run(ctx context.Context) error {
	serverCtx, stopServers := context.WithCancel(context.WithoutCancel(ctx))
	workerCtx, stopWorkers := context.WithCancel(context.WithoutCancel(ctx))
	bgCtx, stopBG := context.WithCancel(context.WithoutCancel(ctx))
	var servers, workers, background sync.WaitGroup

	start := func(wg *sync.WaitGroup, name string, c context.Context, fn func(context.Context) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := fn(c); err != nil {
				a.setErr(fmt.Errorf("%s: %w", name, err))
			}
		}()
	}

	start(&servers, "admin", serverCtx, a.adminSrv.Run)
	if a.httpSrv != nil {
		start(&servers, "http", serverCtx, a.httpSrv.Run)
	}
	if a.grpcSrv != nil {
		start(&servers, "grpc", serverCtx, a.grpcSrv.Run)
	}
	for _, w := range a.workers {
		w := w
		start(&workers, w.name, workerCtx, w.run)
	}
	start(&background, "pool-stats", bgCtx, a.infra.recordPoolStats)
	if a.infra.db != nil && len(a.infra.db.Replicas) > 0 {
		start(&background, "replica-monitor", bgCtx, func(c context.Context) error {
			a.infra.db.MonitorReplicas(c, a.cfg.DBReplicaProbeEvery, 2*a.cfg.DBReplicaProbeEvery/5+1, func(i int, healthy bool) {
				slog.Warn("PostgreSQL replica health changed", "replica", i, "healthy", healthy)
			})
			return nil
		})
	}
	slog.Info("Service started", "role", a.role, "env", a.cfg.AppEnv, "version", a.cfg.ServiceVersion)

	eng := shutdown.NewEngine(a.cfg.DrainDelay, a.cfg.ShutdownTimeout)
	eng.AddPreDrain(shutdown.Task{Name: "flip readiness", Fn: func(context.Context) error {
		a.infra.health.SetReady(false)
		if a.grpcSrv != nil {
			a.grpcSrv.SetServing(false)
		}
		return nil
	}})
	eng.AddPhase(shutdown.Task{Name: "stop servers", Fn: func(c context.Context) error {
		stopServers()
		return waitOrTimeout(c, &servers, "servers")
	}})
	eng.AddPhase(shutdown.Task{Name: "stop workers", Fn: func(c context.Context) error {
		stopWorkers()
		return waitOrTimeout(c, &workers, "workers")
	}})
	eng.AddPhase(shutdown.Task{Name: "stop background", Fn: func(c context.Context) error {
		stopBG()
		return waitOrTimeout(c, &background, "background tasks")
	}})
	eng.AddPhase(shutdown.Task{Name: "close pools", Fn: func(context.Context) error { return a.infra.close() }})

	// ctx is cancelled by the caller's signal handling; failCtx by a component failure.
	waitCtx, cancel := context.WithCancel(a.failCtx)
	defer cancel()
	go func() { <-ctx.Done(); cancel() }()

	shutdownErr := eng.Wait(waitCtx) //nolint:contextcheck // shutdown must outlive the (already cancelled) run context
	a.mu.Lock()
	runErr := a.firstErr
	a.mu.Unlock()
	return errors.Join(runErr, shutdownErr)
}

func waitOrTimeout(ctx context.Context, wg *sync.WaitGroup, what string) error {
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("%s did not stop within the shutdown budget", what)
	}
}

// Shutdown asks Run to begin graceful shutdown (what a SIGTERM does). It is
// safe to call more than once and from any goroutine.
func (a *App) Shutdown() { a.fail() }
