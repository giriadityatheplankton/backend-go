package config_test

import (
	"strings"
	"testing"
	"time"

	"backend-go/internal/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultsAreValid(t *testing.T) {
	cfg, err := config.FromEnv()
	require.NoError(t, err)
	assert.Equal(t, "development", cfg.AppEnv)
	assert.Equal(t, 10*time.Second, cfg.ReadTimeout)
	assert.Equal(t, "closed", cfg.IdempotencyFailureMode, "idempotency must fail closed by default")
	assert.Equal(t, cfg.ServiceName, cfg.ConsumerGroup)
}

func TestLoadOverrides(t *testing.T) {
	t.Setenv("SERVER_ADDRESS", ":9000")
	t.Setenv("OUTBOX_PARTITIONS", "16")
	t.Setenv("RATE_LIMIT_REQUESTS", "500")
	cfg, err := config.FromEnv()
	require.NoError(t, err)
	assert.Equal(t, ":9000", cfg.HTTPAddress)
	assert.Equal(t, 16, cfg.OutboxPartitions)
	assert.Equal(t, int64(500), cfg.RateLimitRequests)
}

func TestMalformedValuesFailInsteadOfFallingBack(t *testing.T) {
	t.Setenv("DB_MAX_OPEN_CONNS", "abc")
	t.Setenv("READ_TIMEOUT", "10") // missing unit
	_, err := config.FromEnv()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DB_MAX_OPEN_CONNS")
	assert.Contains(t, err.Error(), "READ_TIMEOUT")
}

func TestRedisClusterAndReplicaParsing(t *testing.T) {
	t.Setenv("REDIS_MODE", "cluster")
	t.Setenv("REDIS_ADDRESSES", "redis-0:6379, redis-1:6379")
	t.Setenv("DB_PRIMARY_DSN", "postgres://primary/db")
	t.Setenv("DB_REPLICA_DSNS", "postgres://r0/db, postgres://r1/db")
	cfg, err := config.FromEnv()
	require.NoError(t, err)
	assert.Equal(t, []string{"redis-0:6379", "redis-1:6379"}, cfg.RedisState.Addresses)
	assert.Equal(t, []string{"postgres://r0/db", "postgres://r1/db"}, cfg.DBReplicaDSNs)
}

func TestSingleModeWithManyAddressesIsRejected(t *testing.T) {
	t.Setenv("REDIS_ADDRESSES", "a:6379,b:6379") // mode defaults to single: the classic misconfiguration
	_, err := config.FromEnv()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "REDIS_MODE=cluster")
}

func TestCacheRedisFallsBackToStateUnlessConfigured(t *testing.T) {
	t.Setenv("REDIS_ADDRESSES", "state:6379")
	cfg, err := config.FromEnv()
	require.NoError(t, err)
	assert.Equal(t, cfg.RedisState.Addresses, cfg.RedisCache.Addresses)

	t.Setenv("REDIS_CACHE_ADDRESSES", "cache:6379")
	cfg, err = config.FromEnv()
	require.NoError(t, err)
	assert.Equal(t, []string{"cache:6379"}, cfg.RedisCache.Addresses)
}

func TestInFlightTTLMustCoverRequestTimeout(t *testing.T) {
	t.Setenv("HTTP_REQUEST_TIMEOUT", "30s")
	t.Setenv("WRITE_TIMEOUT", "35s")
	t.Setenv("IDEMPOTENCY_INFLIGHT_TTL", "30s")
	_, err := config.FromEnv()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "IDEMPOTENCY_INFLIGHT_TTL")
}

func TestShutdownBudgetMustFitTerminationGrace(t *testing.T) {
	t.Setenv("SHUTDOWN_TIMEOUT", "30s")
	_, err := config.FromEnv()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TERMINATION_GRACE")
}

func TestProductionRules(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	_, err := config.FromEnv()
	require.Error(t, err)
	for _, want := range []string{"AUTH_MODE", "DB_PRIMARY_DSN"} {
		assert.Contains(t, err.Error(), want)
	}

	t.Setenv("DB_PRIMARY_DSN", "postgres://p/db")
	t.Setenv("AUTH_MODE", "jwt")
	t.Setenv("JWT_HMAC_SECRET", strings.Repeat("k", 32))
	t.Setenv("CORS_ALLOWED_ORIGINS", "*")
	t.Setenv("CORS_ALLOW_CREDENTIALS", "true")
	_, err = config.FromEnv()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CORS_ALLOWED_ORIGINS")

	t.Setenv("CORS_ALLOWED_ORIGINS", "https://app.example.com")
	cfg, err := config.FromEnv()
	require.NoError(t, err)
	assert.False(t, cfg.GRPCReflection, "reflection is off by default in production")
	assert.False(t, cfg.PprofEnabled == true && cfg.AdminAddress != "127.0.0.1:9090")
}

func TestInvalidFailureModeAndAuthMode(t *testing.T) {
	t.Setenv("IDEMPOTENCY_FAILURE_MODE", "maybe")
	t.Setenv("AUTH_MODE", "basic")
	_, err := config.FromEnv()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "IDEMPOTENCY_FAILURE_MODE")
	assert.Contains(t, err.Error(), "AUTH_MODE")
}
