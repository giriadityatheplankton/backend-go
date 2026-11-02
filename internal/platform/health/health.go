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

func (f CheckerFunc) Check(ctx context.Context) error { return f(ctx) }

// Severity decides how a failing component affects readiness.
type Severity int

const (
	// Critical components (primary DB, state Redis) make the pod NOT ready when down.
	Critical Severity = iota
	// Degraded components (read replica, cache, optional broker) are reported but
	// do not remove the pod from the load balancer: one failing replica must not
	// cause a fleet-wide outage while the primary is healthy.
	Degraded
)

// HealthStatus represents structured probe output.
type HealthStatus struct {
	Status     string            `json:"status"`
	Timestamp  time.Time         `json:"timestamp"`
	Components map[string]string `json:"components,omitempty"`
}

type entry struct {
	checker  Checker
	severity Severity
}

// Manager coordinates liveness and readiness state.
type Manager struct {
	mu           sync.RWMutex
	ready        int32
	checkers     map[string]entry
	checkTimeout time.Duration
	// Verbose exposes failure reasons in the response. Keep false on public ports:
	// errors can contain hostnames and credentials hints. Reasons are always logged by callers.
	Verbose bool
}

// NewManager initializes a Manager.
func NewManager(checkTimeout time.Duration) *Manager {
	if checkTimeout <= 0 {
		checkTimeout = 2 * time.Second
	}
	return &Manager{ready: 1, checkers: make(map[string]entry), checkTimeout: checkTimeout}
}

// Register adds a Critical component check.
func (m *Manager) Register(name string, checker Checker) {
	m.RegisterWithSeverity(name, checker, Critical)
}

// RegisterWithSeverity adds a component check with an explicit severity.
func (m *Manager) RegisterWithSeverity(name string, checker Checker, s Severity) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.checkers[name] = entry{checker: checker, severity: s}
}

// SetReady toggles readiness (flipped to false first thing during shutdown).
func (m *Manager) SetReady(ready bool) {
	var v int32
	if ready {
		v = 1
	}
	atomic.StoreInt32(&m.ready, v)
}

// IsReady reports current readiness.
func (m *Manager) IsReady() bool { return atomic.LoadInt32(&m.ready) == 1 }

// LivenessHandler answers the liveness probe. It must not depend on anything external.
func (m *Manager) LivenessHandler(c *gin.Context) {
	c.JSON(http.StatusOK, HealthStatus{Status: "UP", Timestamp: time.Now().UTC()})
}

// ReadinessHandler answers the readiness probe.
func (m *Manager) ReadinessHandler(c *gin.Context) {
	if !m.IsReady() {
		c.JSON(http.StatusServiceUnavailable, HealthStatus{Status: "NOT_READY", Timestamp: time.Now().UTC()})
		return
	}

	m.mu.RLock()
	snapshot := make(map[string]entry, len(m.checkers))
	for k, v := range m.checkers {
		snapshot[k] = v
	}
	m.mu.RUnlock()

	ctx, cancel := context.WithTimeout(c.Request.Context(), m.checkTimeout)
	defer cancel()

	type result struct {
		name string
		err  error
	}
	ch := make(chan result, len(snapshot))
	for name, e := range snapshot {
		go func(n string, chk Checker) { ch <- result{n, chk.Check(ctx)} }(name, e.checker)
	}

	components := make(map[string]string, len(snapshot))
	status, code := "UP", http.StatusOK
	for range snapshot {
		r := <-ch
		switch {
		case r.err == nil:
			components[r.name] = "UP"
		case snapshot[r.name].severity == Degraded:
			components[r.name] = detail("DEGRADED", r.err, m.Verbose)
			if status == "UP" {
				status = "DEGRADED"
			}
		default:
			components[r.name] = detail("DOWN", r.err, m.Verbose)
			status, code = "DOWN", http.StatusServiceUnavailable
		}
	}
	c.JSON(code, HealthStatus{Status: status, Timestamp: time.Now().UTC(), Components: components})
}

func detail(state string, err error, verbose bool) string {
	if verbose {
		return state + ": " + err.Error()
	}
	return state
}
