package config_test

import (
	"os"
	"testing"
	"time"

	"backend-go/internal/config"

	"github.com/stretchr/testify/assert"
)

func TestLoadConfig(t *testing.T) {
	os.Setenv("SERVER_ADDRESS", ":9090")
	os.Setenv("APP_ENV", "test")
	os.Setenv("LOG_LEVEL", "debug")
	os.Setenv("OUTBOX_PARTITIONS", "16")
	os.Setenv("RATE_LIMIT_REQUESTS", "500")

	defer func() {
		os.Unsetenv("SERVER_ADDRESS")
		os.Unsetenv("APP_ENV")
		os.Unsetenv("LOG_LEVEL")
		os.Unsetenv("OUTBOX_PARTITIONS")
		os.Unsetenv("RATE_LIMIT_REQUESTS")
	}()

	cfg := config.LoadConfig()
	assert.Equal(t, ":9090", cfg.ServerAddress)
	assert.Equal(t, "test", cfg.AppEnv)
	assert.Equal(t, "debug", cfg.LogLevel)
	assert.Equal(t, 16, cfg.OutboxPartitions)
	assert.Equal(t, int64(500), cfg.RateLimitRequests)
	assert.Equal(t, 10*time.Second, cfg.ReadTimeout)
}
