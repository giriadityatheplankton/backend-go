//go:build integration

// Package integration_test runs against real PostgreSQL and Redis. Set
// TEST_POSTGRES_DSN and TEST_REDIS_ADDR to use existing instances; otherwise
// testcontainers starts disposable ones (requires a Docker-compatible runtime).
package integration_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"testing"

	"backend-go/internal/platform/database"
	"backend-go/internal/platform/database/migrate"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

func randSuffix() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// newDatabase returns the DSN of a fresh, empty database.
func newDatabase(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	if base := os.Getenv("TEST_POSTGRES_DSN"); base != "" {
		admin, err := sql.Open("pgx", base)
		require.NoError(t, err)
		defer admin.Close()
		name := "t_" + randSuffix()
		_, err = admin.ExecContext(ctx, `CREATE DATABASE `+name)
		require.NoError(t, err)
		u, err := url.Parse(base)
		require.NoError(t, err)
		u.Path = "/" + name
		t.Cleanup(func() {
			a, err := sql.Open("pgx", base)
			if err == nil {
				_, _ = a.Exec(`DROP DATABASE IF EXISTS ` + name + ` WITH (FORCE)`)
				_ = a.Close()
			}
		})
		return u.String()
	}
	c, err := tcpostgres.Run(ctx, "postgres:16-alpine", tcpostgres.WithDatabase("app"),
		tcpostgres.WithUsername("u"), tcpostgres.WithPassword("p"), tcpostgres.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Terminate(ctx) })
	dsn, err := c.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	return dsn
}

func openDB(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	database.ConfigurePool(db, database.PoolConfig{MaxOpenConns: 20, MaxIdleConns: 5})
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.PingContext(context.Background()))
	return db
}

func migrated(t *testing.T, sources ...migrate.Source) (*sql.DB, string) {
	t.Helper()
	dsn := newDatabase(t)
	db := openDB(t, dsn)
	require.NoError(t, migrate.NewEngine(sources...).Run(context.Background(), db))
	return db, dsn
}

// redisAddr returns the address of a Redis usable for tests and flushes it.
func redisAddr(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		c, err := tcredis.Run(ctx, "redis:7-alpine")
		require.NoError(t, err)
		t.Cleanup(func() { _ = c.Terminate(ctx) })
		uri, err := c.ConnectionString(ctx)
		require.NoError(t, err)
		opt, err := redis.ParseURL(uri)
		require.NoError(t, err)
		addr = opt.Addr
	}
	cl := redis.NewClient(&redis.Options{Addr: addr})
	defer cl.Close()
	require.NoError(t, cl.FlushAll(ctx).Err())
	return addr
}

func port(t *testing.T) string {
	t.Helper()
	l, err := netListen()
	require.NoError(t, err)
	defer l.Close()
	return fmt.Sprintf("127.0.0.1:%d", l.Addr().(*tcpAddr).Port)
}
