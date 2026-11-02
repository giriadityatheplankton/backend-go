// Command migrate applies (or reverts) database migrations.
//
//	migrate            apply all pending migrations
//	migrate up         same
//	migrate down <source> [steps]   revert the last steps (default 1) of one source
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"backend-go/internal/config"
	"backend-go/internal/pkg/database"
	"backend-go/internal/pkg/database/migrate"
	"backend-go/migrations"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() { os.Exit(run()) }

func run() int {
	// Only the database settings matter here; do not demand unrelated config.
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if cfg.DBPrimaryDSN == "" {
		slog.Error("DB_PRIMARY_DSN is required to run migrations")
		return 2
	}
	db, err := sql.Open("pgx", cfg.DBPrimaryDSN)
	if err != nil {
		slog.Error("Failed to open PostgreSQL primary", "error", err)
		return 1
	}
	defer db.Close()
	// The advisory-lock connection plus one working connection.
	database.ConfigurePool(db, database.PoolConfig{MaxOpenConns: 2, MaxIdleConns: 1})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	eng := migrate.NewEngine(migrations.Sources()...)

	args := os.Args[1:]
	if len(args) > 0 && args[0] == "down" {
		if len(args) < 2 {
			slog.Error("usage: migrate down <source> [steps]")
			return 2
		}
		steps := 1
		if len(args) > 2 {
			if steps, err = strconv.Atoi(args[2]); err != nil || steps < 1 {
				slog.Error("steps must be a positive integer")
				return 2
			}
		}
		if err := eng.Down(ctx, db, args[1], steps); err != nil {
			slog.Error("Migration down failed", "error", err)
			return 1
		}
		slog.Info("Database migrations reverted", "source", args[1], "steps", steps)
		return 0
	}
	if err := eng.Run(ctx, db); err != nil {
		slog.Error("Database migrations failed", "error", err)
		return 1
	}
	slog.Info("Database migrations completed")
	return 0
}
