package health

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
)

// Checker defines the contract for component health status.
type Checker interface {
	Check(ctx context.Context) error
}

// CheckerFunc allows functions to implement Checker.
type CheckerFunc func(ctx context.Context) error

func (f CheckerFunc) Check(ctx context.Context) error {
	return f(ctx)
}

// HealthStatus represents structured probe output.
type HealthStatus struct {
	Status     string            `json:"status"`
	Timestamp  time.Time         `json:"timestamp"`
	Components map[string]string `json:"components,omitempty"`
}

// Manager coordinates liveness and readiness state.
type Manager struct {
	mu           sync.RWMutex
	ready        int32 // 1 = ready, 0 = shutting down / not ready
	checkers     map[string]Checker
	checkTimeout time.Duration
}

// NewManager initializes HealthManager.
func NewManager(checkTimeout time.Duration) *Manager {
	if checkTimeout <= 0 {
		checkTimeout = 2 * time.Second
	}
	m := &Manager{
		ready:        1,
		checkers:     make(map[string]Checker),
		checkTimeout: checkTimeout,
	}
	return m
}

// Register adds a component health check.
func (m *Manager) Register(name string, checker Checker) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.checkers[name] = checker
}

// SetReady toggles readiness state (used during startup & shutdown).
func (m *Manager) SetReady(ready bool) {
	if ready {
		atomic.StoreInt32(&m.ready, 1)
	} else {
		atomic.StoreInt32(&m.ready, 0)
	}
}

// IsReady reports current readiness.
func (m *Manager) IsReady() bool {
	return atomic.LoadInt32(&m.ready) == 1
}

// LivenessHandler handles K8s liveness probe.
func (m *Manager) LivenessHandler(c *gin.Context) {
	c.JSON(http.StatusOK, HealthStatus{
		Status:    "UP",
		Timestamp: time.Now().UTC(),
	})
}

// ReadinessHandler handles K8s readiness probe.
func (m *Manager) ReadinessHandler(c *gin.Context) {
	if !m.IsReady() {
		c.JSON(http.StatusServiceUnavailable, HealthStatus{
			Status:    "NOT_READY",
			Timestamp: time.Now().UTC(),
		})
		return
	}

	m.mu.RLock()
	checkers := make(map[string]Checker, len(m.checkers))
	for k, v := range m.checkers {
		checkers[k] = v
	}
	m.mu.RUnlock()

	ctx, cancel := context.WithTimeout(c.Request.Context(), m.checkTimeout)
	defer cancel()

	type result struct {
		name string
		err  error
	}

	resCh := make(chan result, len(checkers))
	for name, chk := range checkers {
		go func(n string, c Checker) {
			resCh <- result{name: n, err: c.Check(ctx)}
		}(name, chk)
	}

	components := make(map[string]string)
	allHealthy := true

	for i := 0; i < len(checkers); i++ {
		res := <-resCh
		if res.err != nil {
			components[res.name] = "DOWN: " + res.err.Error()
			allHealthy = false
		} else {
			components[res.name] = "UP"
		}
	}

	statusStr := "UP"
	httpCode := http.StatusOK
	if !allHealthy {
		statusStr = "DOWN"
		httpCode = http.StatusServiceUnavailable
	}

	c.JSON(httpCode, HealthStatus{
		Status:     statusStr,
		Timestamp:  time.Now().UTC(),
		Components: components,
	})
}
