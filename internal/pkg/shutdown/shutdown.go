package shutdown

import (
	"context"
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

// Engine coordinates sequential and phased graceful shutdown.
type Engine struct {
	mu          sync.Mutex
	phases      [][]Task
	drainDelay  time.Duration
	gracePeriod time.Duration
}

// NewEngine initializes shutdown engine.
func NewEngine(drainDelay, gracePeriod time.Duration) *Engine {
	if drainDelay < 0 {
		drainDelay = 3 * time.Second
	}
	if gracePeriod <= 0 {
		gracePeriod = 15 * time.Second
	}
	return &Engine{
		phases:      make([][]Task, 0),
		drainDelay:  drainDelay,
		gracePeriod: gracePeriod,
	}
}

// AddPhase appends an ordered phase of cleanup tasks executed sequentially.
// Tasks within the same phase run concurrently.
func (e *Engine) AddPhase(tasks ...Task) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.phases = append(e.phases, tasks)
}

// ListenAndServe blocks until SIGTERM or SIGINT is received, then executes phased shutdown.
func (e *Engine) ListenAndServe() error {
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	sig := <-quit
	slog.Info("Shutdown signal received, initiating graceful termination", "signal", sig.String())

	// Wait for K8s Ingress/Kube-proxy endpoint deregistration buffer
	if e.drainDelay > 0 {
		slog.Info("Waiting drain delay for K8s traffic propagation", "delay", e.drainDelay)
		time.Sleep(e.drainDelay)
	}

	ctx, cancel := context.WithTimeout(context.Background(), e.gracePeriod)
	defer cancel()

	for phaseIdx, tasks := range e.phases {
		slog.Info("Executing shutdown phase", "phase", phaseIdx+1, "tasks_count", len(tasks))

		var wg sync.WaitGroup
		for _, task := range tasks {
			wg.Add(1)
			go func(t Task) {
				defer wg.Done()
				slog.Info("Starting cleanup task", "task", t.Name)
				if err := t.Fn(ctx); err != nil {
					slog.Error("Cleanup task failed", "task", t.Name, "error", err)
				} else {
					slog.Info("Cleanup task completed", "task", t.Name)
				}
			}(task)
		}
		wg.Wait()
	}

	slog.Info("All shutdown phases completed cleanly")
	return nil
}
