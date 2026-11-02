package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

type contextKey struct{}

var tenantContextKey = contextKey{}

var (
	ErrNoReplicaAvailable = errors.New("no read replica available")
	ErrTenantNotFound     = errors.New("tenant database not found")
)

// WithTenantID stores tenant identifier in context.
func WithTenantID(ctx context.Context, tenantID string) context.Context {
	return context.WithValue(ctx, tenantContextKey, tenantID)
}

// TenantIDFromContext retrieves tenant identifier from context.
func TenantIDFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	id, ok := ctx.Value(tenantContextKey).(string)
	return id, ok
}

// DBGroup encapsulates Primary (Write) and Replica (Read) connections.
type DBGroup struct {
	Primary  *sql.DB
	Replicas []*sql.DB
	rrIndex  uint64
	// down[i] is 1 while replica i failed its last health probe.
	down []atomic.Bool
}

// NewDBGroup initializes a read/write split database cluster.
func NewDBGroup(primary *sql.DB, replicas ...*sql.DB) *DBGroup {
	return &DBGroup{
		Primary:  primary,
		Replicas: replicas,
		down:     make([]atomic.Bool, len(replicas)),
	}
}

// Write returns the primary database for mutations.
func (g *DBGroup) Write() *sql.DB {
	return g.Primary
}

// ReadPrimary returns the primary database for reads that require the latest
// committed data, such as read-after-write and consistency-critical decisions.
func (g *DBGroup) ReadPrimary() *sql.DB {
	return g.Primary
}

// Read returns the next healthy replica (round-robin) and falls back to the
// primary when there are no replicas or all of them are marked down.
//
// Replicas lag the primary. Anything that must observe its own just-committed
// write, or that makes a money/stock/permission decision, must use ReadPrimary.
func (g *DBGroup) Read() *sql.DB {
	n := len(g.Replicas)
	for i := 0; i < n; i++ {
		idx := int(atomic.AddUint64(&g.rrIndex, 1) % uint64(n)) //nolint:gosec // result is < n
		if g.Replicas[idx] != nil && !g.down[idx].Load() {
			return g.Replicas[idx]
		}
	}
	return g.Primary
}

// ReplicaHealthy reports whether replica i passed its last probe.
func (g *DBGroup) ReplicaHealthy(i int) bool {
	return i >= 0 && i < len(g.down) && !g.down[i].Load()
}

// MonitorReplicas probes every replica until ctx is cancelled and routes reads
// away from the ones that fail. It blocks; run it in a goroutine.
func (g *DBGroup) MonitorReplicas(ctx context.Context, interval, probeTimeout time.Duration, onChange func(i int, healthy bool)) {
	if len(g.Replicas) == 0 {
		return
	}
	if interval <= 0 {
		interval = 5 * time.Second
	}
	if probeTimeout <= 0 {
		probeTimeout = 2 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		for i, r := range g.Replicas {
			pctx, cancel := context.WithTimeout(ctx, probeTimeout)
			err := r.PingContext(pctx)
			cancel()
			if ctx.Err() != nil {
				return
			}
			isDown := err != nil
			if prev := g.down[i].Swap(isDown); prev != isDown && onChange != nil {
				onChange(i, !isDown)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// TenantResolver manages dynamic multi-tenant database routing.
type TenantResolver interface {
	Resolve(ctx context.Context) (*DBGroup, error)
	Register(tenantID string, group *DBGroup)
}

// DynamicTenantRouter routes queries to tenant-specific DBGroup based on context tenant ID.
//
// Unknown tenants return ErrTenantNotFound by default. Falling back to the shared
// database for an unknown tenant would leak data between tenants when isolation is
// per-database, so the fallback is opt-in via AllowDefaultFallback.
type DynamicTenantRouter struct {
	mu        sync.RWMutex
	defaultDB *DBGroup
	tenants   map[string]*DBGroup
	fallback  bool
}

// AllowDefaultFallback opts in to routing unknown/absent tenants to the default DB.
func (r *DynamicTenantRouter) AllowDefaultFallback() *DynamicTenantRouter {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fallback = true
	return r
}

// NewDynamicTenantRouter initializes DynamicTenantRouter.
func NewDynamicTenantRouter(defaultDB *DBGroup) *DynamicTenantRouter {
	return &DynamicTenantRouter{
		defaultDB: defaultDB,
		tenants:   make(map[string]*DBGroup),
	}
}

// Register maps tenantID to its dedicated DBGroup.
func (r *DynamicTenantRouter) Register(tenantID string, group *DBGroup) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tenants[tenantID] = group
}

// Resolve looks up the appropriate DBGroup for the current context.
func (r *DynamicTenantRouter) Resolve(ctx context.Context) (*DBGroup, error) {
	r.mu.RLock()
	fallback, defaultDB := r.fallback, r.defaultDB
	r.mu.RUnlock()

	tenantID, ok := TenantIDFromContext(ctx)
	if !ok || tenantID == "" {
		if fallback && defaultDB != nil {
			return defaultDB, nil
		}
		return nil, ErrTenantNotFound
	}

	r.mu.RLock()
	group, exists := r.tenants[tenantID]
	r.mu.RUnlock()
	if exists {
		return group, nil
	}
	if fallback && defaultDB != nil {
		return defaultDB, nil
	}
	return nil, fmt.Errorf("%w: %s", ErrTenantNotFound, tenantID)
}

// MarkReplica overrides the health flag of replica i (used by probes and tests).
func (g *DBGroup) MarkReplica(i int, healthy bool) {
	if i >= 0 && i < len(g.down) {
		g.down[i].Store(!healthy)
	}
}
