// Command api runs the HTTP and gRPC servers. It is intentionally tiny: all
// wiring lives in internal/app.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	_ "go.uber.org/automaxprocs"

	"backend-go/internal/app"
	"backend-go/internal/config"
)

func main() { os.Exit(run()) }

func run() int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	app.SetupLogger(cfg)

	role := app.RoleAPI
	if cfg.EmbedWorker {
		role = app.RoleAll
	}
	a, err := app.New(context.Background(), cfg, role)
	if err != nil {
		slog.Error("Startup failed", "error", err)
		return 1
	}
	if err := a.Run(context.Background()); err != nil {
		slog.Error("Service stopped with error", "error", err)
		return 1
	}
	return 0
}
