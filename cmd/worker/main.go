// Command worker runs the outbox relay, stream consumers and background jobs
// without serving API traffic, so it can be scaled and deployed independently.
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

	a, err := app.New(context.Background(), cfg, app.RoleWorker)
	if err != nil {
		slog.Error("Startup failed", "error", err)
		return 1
	}
	if err := a.Run(context.Background()); err != nil {
		slog.Error("Worker stopped with error", "error", err)
		return 1
	}
	return 0
}
