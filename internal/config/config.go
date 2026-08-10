package config

import (
	"log/slog"
	"os"
	"time"

	"github.com/joho/godotenv"
)

// Config holds all configuration values for the application.
type Config struct {
	ServerAddress   string        // HTTP server bind address (e.g., "127.0.0.1:8080")
	AppEnv          string        // Application environment ("development", "staging", "production")
	RedisAddress    string        // Redis server address (e.g., "127.0.0.1:6379")
	NatsAddress     string        // NATS server address/URL (e.g., "nats://127.0.0.1:4222")
	ShutdownTimeout time.Duration // Graceful shutdown timeout
	CacheTTL        time.Duration // Cache time-to-live
	ReadTimeout     time.Duration // HTTP Server Read Timeout
	WriteTimeout    time.Duration // HTTP Server Write Timeout
}

// LoadConfig loads configuration from environment variables or .env file.
func LoadConfig() *Config {
	if err := godotenv.Load(); err != nil {
		slog.Info(".env file not found, fallback to system environment variables")
	}

	return &Config{
		ServerAddress:   getEnv("SERVER_ADDRESS", "127.0.0.1:8080"),
		AppEnv:          getEnv("APP_ENV", "development"),
		RedisAddress:    getEnv("REDIS_ADDRESS", "127.0.0.1:6379"),
		NatsAddress:     getEnv("NATS_ADDRESS", "nats://127.0.0.1:4222"),
		ShutdownTimeout: getEnvAsDuration("SHUTDOWN_TIMEOUT", 10*time.Second),
		CacheTTL:        getEnvAsDuration("CACHE_TTL", 5*time.Minute),
		ReadTimeout:     getEnvAsDuration("READ_TIMEOUT", 10*time.Second),
		WriteTimeout:    getEnvAsDuration("WRITE_TIMEOUT", 10*time.Second),
	}
}

func getEnv(key, fallback string) string {
	if val, ok := os.LookupEnv(key); ok && val != "" {
		return val
	}
	return fallback
}

func getEnvAsDuration(key string, fallback time.Duration) time.Duration {
	valStr := os.Getenv(key)
	if valStr == "" {
		return fallback
	}
	d, err := time.ParseDuration(valStr)
	if err != nil {
		slog.Warn("Failed to parse duration config", "key", key, "value", valStr, "fallback", fallback)
		return fallback
	}
	return d
}

