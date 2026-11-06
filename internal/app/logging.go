package app

import (
	"log/slog"
	"os"

	"backend-go/internal/config"
	"backend-go/internal/pkg/observability/telemetry"
)

// SetupLogger installs the process-wide JSON logger with trace/request IDs and
// fixed service attributes on every line.
func SetupLogger(cfg *config.Config) {
	level := slog.LevelInfo
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	host, _ := os.Hostname()
	base := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	logger := slog.New(telemetry.NewTraceHandler(base)).With(
		"service", cfg.ServiceName, "version", cfg.ServiceVersion, "env", cfg.AppEnv, "pod", host,
	)
	slog.SetDefault(logger)
}
