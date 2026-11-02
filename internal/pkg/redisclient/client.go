// Package redisclient builds go-redis clients for single node, Sentinel and
// Cluster deployments with production-safe timeouts, pool sizing, TLS and ACL.
//
// Use two logical Redis deployments in production:
//   - cache : eviction allowed (maxmemory-policy allkeys-lru)
//   - state : idempotency keys, locks, streams (noeviction, appendonly yes)
//
// Sharing one instance lets eviction delete idempotency keys and locks.
package redisclient

import (
	"crypto/tls"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	ModeSingle   = "single"
	ModeSentinel = "sentinel"
	ModeCluster  = "cluster"
)

// Options configures a client.
type Options struct {
	Mode      string
	Addresses []string
	Username  string // ACL user
	Password  string
	DB        int

	// Sentinel only.
	MasterName       string
	SentinelPassword string

	TLS           bool
	TLSSkipVerify bool // never enable in production

	DialTimeout  time.Duration
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	PoolTimeout  time.Duration
	PoolSize     int
	MinIdleConns int
	MaxRetries   int
	// ClientName shows up in CLIENT LIST, which helps when debugging connection storms.
	ClientName string
}

func (o *Options) defaults() {
	if o.DialTimeout <= 0 {
		o.DialTimeout = 2 * time.Second
	}
	if o.ReadTimeout <= 0 {
		o.ReadTimeout = time.Second
	}
	if o.WriteTimeout <= 0 {
		o.WriteTimeout = time.Second
	}
	if o.PoolTimeout <= 0 {
		o.PoolTimeout = 2 * time.Second
	}
	if o.MaxRetries == 0 {
		o.MaxRetries = 2
	}
}

// New is the legacy constructor kept for simple callers and tests.
func New(mode string, addresses []string, password string, db int) (redis.UniversalClient, error) {
	return NewClient(Options{Mode: mode, Addresses: addresses, Password: password, DB: db})
}

// NewClient creates a redis.UniversalClient according to opts.Mode.
//
// Stream consumers block in XREADGROUP for up to their Block duration, which is
// longer than ReadTimeout. go-redis extends the socket deadline by the command's
// block time, so no special handling is needed.
func NewClient(o Options) (redis.UniversalClient, error) {
	o.defaults()
	if len(o.Addresses) == 0 {
		return nil, errors.New("at least one Redis address is required")
	}
	var tlsCfg *tls.Config
	if o.TLS {
		tlsCfg = &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: o.TLSSkipVerify} //nolint:gosec // opt-in for dev only
	}

	switch o.Mode {
	case ModeSingle:
		if len(o.Addresses) != 1 {
			return nil, fmt.Errorf("redis single mode requires exactly one address, got %d", len(o.Addresses))
		}
		return redis.NewClient(&redis.Options{
			Addr: o.Addresses[0], Username: o.Username, Password: o.Password, DB: o.DB,
			DialTimeout: o.DialTimeout, ReadTimeout: o.ReadTimeout, WriteTimeout: o.WriteTimeout,
			PoolTimeout: o.PoolTimeout, PoolSize: o.PoolSize, MinIdleConns: o.MinIdleConns,
			MaxRetries: o.MaxRetries, TLSConfig: tlsCfg, ClientName: o.ClientName,
		}), nil
	case ModeSentinel:
		if o.MasterName == "" {
			return nil, errors.New("redis sentinel mode requires REDIS_MASTER_NAME")
		}
		return redis.NewFailoverClient(&redis.FailoverOptions{
			MasterName: o.MasterName, SentinelAddrs: o.Addresses, SentinelPassword: o.SentinelPassword,
			Username: o.Username, Password: o.Password, DB: o.DB,
			DialTimeout: o.DialTimeout, ReadTimeout: o.ReadTimeout, WriteTimeout: o.WriteTimeout,
			PoolTimeout: o.PoolTimeout, PoolSize: o.PoolSize, MinIdleConns: o.MinIdleConns,
			MaxRetries: o.MaxRetries, TLSConfig: tlsCfg, ClientName: o.ClientName,
		}), nil
	case ModeCluster:
		if o.DB != 0 {
			return nil, errors.New("redis cluster does not support selecting a logical database: REDIS_DB must be 0")
		}
		return redis.NewClusterClient(&redis.ClusterOptions{
			Addrs: o.Addresses, Username: o.Username, Password: o.Password,
			DialTimeout: o.DialTimeout, ReadTimeout: o.ReadTimeout, WriteTimeout: o.WriteTimeout,
			PoolTimeout: o.PoolTimeout, PoolSize: o.PoolSize, MinIdleConns: o.MinIdleConns,
			MaxRetries: o.MaxRetries, TLSConfig: tlsCfg, ClientName: o.ClientName,
		}), nil
	default:
		return nil, fmt.Errorf("unsupported Redis mode %q: use %q, %q or %q", o.Mode, ModeSingle, ModeSentinel, ModeCluster)
	}
}
