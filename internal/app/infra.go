package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"backend-go/internal/config"
	"backend-go/internal/pkg/cache"
	"backend-go/internal/pkg/database"
	"backend-go/internal/pkg/health"
	"backend-go/internal/pkg/observability/metrics"
	"backend-go/internal/pkg/redisclient"

	"github.com/redis/go-redis/v9"
)

// infra owns every connection pool and is the only place that opens them.
type infra struct {
	db         *database.DBGroup
	tx         *database.TxManager
	redisState redis.UniversalClient // idempotency, locks, streams (noeviction + AOF)
	redisCache redis.UniversalClient // cache (may evict)
	cache      *cache.Manager
	metrics    *metrics.Metrics
	health     *health.Manager
}

func openInfra(ctx context.Context, cfg *config.Config) (_ *infra, err error) {
	in := &infra{metrics: metrics.InitMetrics(nil), health: health.NewManager(2 * time.Second)}
	in.metrics.SetBuildInfo(cfg.ServiceName, cfg.ServiceVersion, cfg.Commit)
	defer func() {
		if err != nil {
			_ = in.close()
		}
	}()

	if cfg.DBPrimaryDSN != "" {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		in.db, err = database.OpenPostgresGroup(cctx, cfg.DBPrimaryDSN, cfg.DBReplicaDSNs, database.PoolConfig{
			MaxOpenConns: cfg.DBMaxOpenConns, MaxIdleConns: cfg.DBMaxIdleConns,
			ConnMaxLifetime: cfg.DBConnMaxLifetime, ConnMaxIdleTime: cfg.DBConnMaxIdleTime,
			StatementTimeout: cfg.DBStatementTimeout, IdleInTxTimeout: cfg.DBIdleInTxTimeout,
		})
		cancel()
		if err != nil {
			return nil, fmt.Errorf("connect PostgreSQL: %w", err)
		}
		in.tx = database.NewTxManager(in.db.Write())
		in.health.Register("postgres-primary", health.CheckerFunc(func(c context.Context) error { return in.db.Write().PingContext(c) }))
		for i, r := range in.db.Replicas {
			r := r
			// A replica problem must not make every pod unready while the primary is fine.
			in.health.RegisterWithSeverity(fmt.Sprintf("postgres-replica-%d", i),
				health.CheckerFunc(func(c context.Context) error { return r.PingContext(c) }), health.Degraded)
		}
		slog.Info("Connected to PostgreSQL", "replicas", len(in.db.Replicas))
	} else {
		slog.Warn("PostgreSQL is not configured")
	}

	if in.redisState, err = openRedis(ctx, cfg, cfg.RedisState, "state"); err != nil {
		return nil, err
	}
	switch {
	case in.redisState == nil:
	case sameRedis(cfg.RedisState, cfg.RedisCache):
		in.redisCache = in.redisState
	default:
		if in.redisCache, err = openRedis(ctx, cfg, cfg.RedisCache, "cache"); err != nil {
			return nil, err
		}
	}
	if in.redisState != nil {
		st := in.redisState
		in.health.Register("redis-state", health.CheckerFunc(func(c context.Context) error { return st.Ping(c).Err() }))
	}
	if in.redisCache != nil && in.redisCache != in.redisState {
		ca := in.redisCache
		in.health.RegisterWithSeverity("redis-cache", health.CheckerFunc(func(c context.Context) error { return ca.Ping(c).Err() }), health.Degraded)
	}
	in.cache = cache.NewManager(in.redisCache,
		cache.WithPrefix(cfg.ServiceName+":"), cache.WithJitter(0.1))
	return in, nil
}

func sameRedis(a, b config.Redis) bool {
	return a.Mode == b.Mode && len(a.Addresses) == len(b.Addresses) && len(a.Addresses) > 0 && a.Addresses[0] == b.Addresses[0] && a.DB == b.DB
}

func openRedis(ctx context.Context, cfg *config.Config, rc config.Redis, role string) (redis.UniversalClient, error) {
	client, err := redisclient.NewClient(redisclient.Options{
		Mode: rc.Mode, Addresses: rc.Addresses, Username: rc.Username, Password: rc.Password, DB: rc.DB,
		MasterName: rc.MasterName, SentinelPassword: rc.SentinelPassword,
		TLS: rc.TLS, TLSSkipVerify: rc.TLSSkipVerify,
		DialTimeout: rc.DialTimeout, ReadTimeout: rc.ReadTimeout, WriteTimeout: rc.WriteTimeout,
		PoolSize: rc.PoolSize, MinIdleConns: rc.MinIdleConns, ClientName: cfg.ServiceName + "-" + role,
	})
	if err != nil {
		return nil, fmt.Errorf("configure redis %s: %w", role, err)
	}
	pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := client.Ping(pctx).Err(); err != nil {
		_ = client.Close()
		if cfg.RedisRequired {
			return nil, fmt.Errorf("connect redis %s: %w", role, err)
		}
		slog.Warn("Redis unavailable; running degraded because REDIS_REQUIRED=false", "role", role, "error", err)
		return nil, nil
	}
	slog.Info("Connected to Redis", "role", role, "mode", rc.Mode)
	return client, nil
}

func (in *infra) close() error {
	var errs []error
	if in.redisCache != nil && in.redisCache != in.redisState {
		errs = append(errs, in.redisCache.Close())
	}
	if in.redisState != nil {
		errs = append(errs, in.redisState.Close())
	}
	if in.db != nil {
		errs = append(errs, in.db.Close())
	}
	return errors.Join(errs...)
}

// recordPoolStats exports pool gauges until ctx is cancelled.
func (in *infra) recordPoolStats(ctx context.Context) error {
	if in.db == nil {
		return nil
	}
	record := func() {
		in.metrics.RecordDBStats("primary", in.db.Write().Stats())
		for i, r := range in.db.Replicas {
			in.metrics.RecordDBStats(fmt.Sprintf("replica-%d", i), r.Stats())
		}
	}
	record()
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			record()
		}
	}
}
