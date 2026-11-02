// Package shutdown runs an ordered, phased graceful shutdown.
//
// Order that avoids dropped requests on Kubernetes:
//
//  1. flip readiness to NOT_READY            (phase 0, immediately)
//  2. wait DrainDelay                         (endpoints/LB propagate; traffic stops)
//  3. stop servers, then workers, then pools  (later phases, each bounded by GracePeriod)
//
// DrainDelay + GracePeriod must be smaller than terminationGracePeriodSeconds minus the preStop sleep.
package shutdown

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// Task represents a named cleanup operation.
type Task struct {
	Name string
	Fn   func(ctx context.Context) error
}

// Engine coordinates phased graceful shutdown.
type Engine struct {
	mu          sync.Mutex
	pre         []Task // run immediately on signal, before the drain delay
	phases      [][]Task
	drainDelay  time.Duration
	gracePeriod time.Duration
}

// NewEngine initializes the engine.
func NewEngine(drainDelay, gracePeriod time.Duration) *Engine {
	if drainDelay < 0 {
		drainDelay = 0
	}
	if gracePeriod <= 0 {
		gracePeriod = 15 * time.Second
	}
	return &Engine{drainDelay: drainDelay, gracePeriod: gracePeriod}
}

// AddPreDrain registers tasks that run immediately when shutdown starts, before
// the drain delay. Use it to flip readiness.
func (e *Engine) AddPreDrain(tasks ...Task) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pre = append(e.pre, tasks...)
}

// AddPhase appends an ordered phase. Tasks within a phase run concurrently;
// phases run sequentially.
func (e *Engine) AddPhase(tasks ...Task) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.phases = append(e.phases, tasks)
}

// MinTerminationGrace is the smallest terminationGracePeriodSeconds that is safe
// for this engine plus a preStop sleep.
func (e *Engine) MinTerminationGrace(preStop time.Duration) time.Duration {
	return e.drainDelay + e.gracePeriod + preStop
}

// Wait blocks until SIGINT/SIGTERM or until ctx is cancelled (e.g. a server
// crashed), then runs the shutdown sequence. A second signal forces an immediate
// exit with status 1. The returned error aggregates failed tasks.
func (e *Engine) Wait(ctx context.Context) error {
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	select {
	case sig := <-sigCh:
		slog.Info("Shutdown signal received", "signal", sig.String())
	case <-ctx.Done():
		slog.Warn("Shutdown triggered by component failure")
	}

	go func() {
		<-sigCh
		slog.Error("Second shutdown signal received; forcing exit")
		os.Exit(1)
	}()
	return e.Run() //nolint:contextcheck // shutdown tasks get their own deadline, not the cancelled caller context
}

// ListenAndServe is kept for callers that only wait for signals.
func (e *Engine) ListenAndServe() error { return e.Wait(context.Background()) }

// Run executes the shutdown sequence without waiting for a signal.
func (e *Engine) Run() error {
	e.mu.Lock()
	pre := append([]Task(nil), e.pre...)
	phases := append([][]Task(nil), e.phases...)
	e.mu.Unlock()

	var errs []error
	collect := func(err error) {
		if err != nil {
			e.mu.Lock()
			errs = append(errs, err)
			e.mu.Unlock()
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), e.drainDelay+e.gracePeriod)
	defer cancel()

	if len(pre) > 0 {
		runTasks(ctx, "pre-drain", pre, collect)
	}
	if e.drainDelay > 0 {
		slog.Info("Waiting drain delay so load balancers stop sending traffic", "delay", e.drainDelay)
		time.Sleep(e.drainDelay)
	}
	for i, tasks := range phases {
		phaseCtx, pcancel := context.WithTimeout(ctx, e.gracePeriod)
		runTasks(phaseCtx, fmt.Sprintf("phase-%d", i+1), tasks, collect)
		pcancel()
	}

	if len(errs) > 0 {
		return fmt.Errorf("shutdown finished with %d error(s): %w", len(errs), joinErrs(errs))
	}
	slog.Info("All shutdown phases completed cleanly")
	return nil
}

func runTasks(ctx context.Context, label string, tasks []Task, collect func(error)) {
	slog.Info("Executing shutdown phase", "phase", label, "tasks", len(tasks))
	var wg sync.WaitGroup
	for _, t := range tasks {
		wg.Add(1)
		go func(t Task) {
			defer wg.Done()
			if err := t.Fn(ctx); err != nil {
				slog.Error("Cleanup task failed", "task", t.Name, "error", err)
				collect(fmt.Errorf("%s: %w", t.Name, err))
				return
			}
			slog.Info("Cleanup task completed", "task", t.Name)
		}(t)
	}
	wg.Wait()
}

func joinErrs(errs []error) error {
	if len(errs) == 1 {
		return errs[0]
	}
	msg := ""
	for i, e := range errs {
		if i > 0 {
			msg += "; "
		}
		msg += e.Error()
	}
	return fmt.Errorf("%s", msg)
}
