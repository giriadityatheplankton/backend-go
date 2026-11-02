// Package migrate applies embedded SQL migrations.
//
// Features: multiple independent sources (platform tables vs. each module's
// tables, each with its own version space), a cluster-wide Postgres advisory
// lock so concurrent Jobs/pods cannot collide, SHA-256 checksums that detect
// edits to already-applied files, opt-out of the wrapping transaction for
// statements such as CREATE INDEX CONCURRENTLY, and optional .down.sql files.
//
// File naming: <version>_<name>.up.sql and <version>_<name>.down.sql.
// Put "-- +migrate notx" on the first line of an .up.sql to run it outside a transaction.
package migrate

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"sort"
	"strconv"
	"strings"
)

// advisoryLockKey is an arbitrary constant shared by every migrator instance.
const advisoryLockKey int64 = 0x6d69677261746531 // "migrate1"

const noTxMarker = "-- +migrate notx"

// Source is an independent, ordered set of migrations.
type Source struct {
	Name string // e.g. "platform", "order"; part of the history key
	FS   fs.FS
	Dir  string // directory inside FS ("." for root)
}

// Migration is one parsed migration.
type Migration struct {
	Source   string
	Version  int
	Name     string
	Up       string
	Down     string
	Checksum string
	NoTx     bool
}

// Engine runs migrations for a fixed list of sources.
type Engine struct {
	sources []Source
}

// NewEngine creates an Engine. Sources run in the order given.
func NewEngine(sources ...Source) *Engine { return &Engine{sources: sources} }

// Run applies all pending up migrations of every source.
func (e *Engine) Run(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return errors.New("database connection is nil")
	}
	// Everything runs on ONE connection: advisory locks are per session.
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, advisoryLockKey); err != nil {
		return fmt.Errorf("acquire migration advisory lock: %w", err)
	}
	defer func() {
		unlockCtx := context.WithoutCancel(ctx)
		if _, err := conn.ExecContext(unlockCtx, `SELECT pg_advisory_unlock($1)`, advisoryLockKey); err != nil {
			slog.Warn("Failed to release migration advisory lock", "error", err)
		}
	}()

	if err := initSchema(ctx, conn); err != nil {
		return fmt.Errorf("init schema_history: %w", err)
	}
	for _, src := range e.sources {
		migrations, err := load(src)
		if err != nil {
			return fmt.Errorf("load migrations for %q: %w", src.Name, err)
		}
		applied, err := appliedChecksums(ctx, conn, src.Name)
		if err != nil {
			return fmt.Errorf("read applied migrations for %q: %w", src.Name, err)
		}
		for _, m := range migrations {
			if sum, ok := applied[m.Version]; ok {
				if sum != m.Checksum {
					return fmt.Errorf("migration %s/%06d_%s was modified after being applied (checksum mismatch); create a new migration instead", m.Source, m.Version, m.Name)
				}
				continue
			}
			if err := apply(ctx, conn, m); err != nil {
				return err
			}
		}
	}
	return nil
}

// Down reverts the last steps applied migrations of one source (newest first).
// It requires a matching .down.sql for each reverted migration.
func (e *Engine) Down(ctx context.Context, db *sql.DB, source string, steps int) error {
	if db == nil {
		return errors.New("database connection is nil")
	}
	var src *Source
	for i := range e.sources {
		if e.sources[i].Name == source {
			src = &e.sources[i]
		}
	}
	if src == nil {
		return fmt.Errorf("unknown migration source %q", source)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, advisoryLockKey); err != nil {
		return err
	}
	defer func() {
		_, _ = conn.ExecContext(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, advisoryLockKey)
	}()

	if err := initSchema(ctx, conn); err != nil {
		return err
	}
	migrations, err := load(*src)
	if err != nil {
		return err
	}
	applied, err := appliedChecksums(ctx, conn, source)
	if err != nil {
		return err
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].Version > migrations[j].Version })
	for _, m := range migrations {
		if steps <= 0 {
			break
		}
		if _, ok := applied[m.Version]; !ok {
			continue
		}
		if m.Down == "" {
			return fmt.Errorf("migration %s/%06d_%s has no .down.sql", m.Source, m.Version, m.Name)
		}
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, m.Down); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("revert %s/%06d_%s: %w", m.Source, m.Version, m.Name, err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM schema_history WHERE source=$1 AND version=$2`, m.Source, m.Version); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		slog.InfoContext(ctx, "Database migration reverted", "source", m.Source, "version", m.Version)
		steps--
	}
	return nil
}

func apply(ctx context.Context, conn *sql.Conn, m Migration) error {
	slog.InfoContext(ctx, "Applying database migration", "source", m.Source, "version", m.Version, "name", m.Name, "transactional", !m.NoTx)
	record := `INSERT INTO schema_history (source, version, name, checksum) VALUES ($1,$2,$3,$4)`

	if m.NoTx {
		if _, err := conn.ExecContext(ctx, m.Up); err != nil {
			return fmt.Errorf("execute migration %s/%06d_%s: %w", m.Source, m.Version, m.Name, err)
		}
		if _, err := conn.ExecContext(ctx, record, m.Source, m.Version, m.Name, m.Checksum); err != nil {
			return fmt.Errorf("record migration %s/%06d: %w", m.Source, m.Version, err)
		}
		return nil
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s/%06d: %w", m.Source, m.Version, err)
	}
	if _, err := tx.ExecContext(ctx, m.Up); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("execute migration %s/%06d_%s: %w", m.Source, m.Version, m.Name, err)
	}
	if _, err := tx.ExecContext(ctx, record, m.Source, m.Version, m.Name, m.Checksum); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("record migration %s/%06d: %w", m.Source, m.Version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s/%06d: %w", m.Source, m.Version, err)
	}
	return nil
}

func initSchema(ctx context.Context, conn *sql.Conn) error {
	_, err := conn.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_history (
			source     TEXT        NOT NULL,
			version    INT         NOT NULL,
			name       TEXT        NOT NULL,
			checksum   CHAR(64)    NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (source, version)
		)`)
	return err
}

func appliedChecksums(ctx context.Context, conn *sql.Conn, source string) (_ map[int]string, retErr error) {
	rows, err := conn.QueryContext(ctx, `SELECT version, checksum FROM schema_history WHERE source = $1`, source)
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			retErr = errors.Join(retErr, cerr)
		}
	}()
	out := map[int]string{}
	for rows.Next() {
		var v int
		var sum string
		if err := rows.Scan(&v, &sum); err != nil {
			return nil, err
		}
		out[v] = strings.TrimSpace(sum)
	}
	return out, rows.Err()
}

func load(src Source) ([]Migration, error) {
	dir := src.Dir
	if dir == "" {
		dir = "."
	}
	entries, err := fs.ReadDir(src.FS, dir)
	if err != nil {
		return nil, err
	}
	byVersion := map[int]*Migration{}
	for _, entry := range entries {
		name := entry.Name()
		var kind string
		switch {
		case entry.IsDir():
			continue
		case strings.HasSuffix(name, ".up.sql"):
			kind = "up"
		case strings.HasSuffix(name, ".down.sql"):
			kind = "down"
		default:
			continue
		}
		base := strings.TrimSuffix(strings.TrimSuffix(name, ".up.sql"), ".down.sql")
		parts := strings.SplitN(base, "_", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("migration file %q must be named <version>_<name>.%s.sql", name, kind)
		}
		version, err := strconv.Atoi(parts[0])
		if err != nil {
			return nil, fmt.Errorf("migration file %q has a non-numeric version", name)
		}
		content, err := fs.ReadFile(src.FS, path.Join(dir, name))
		if err != nil {
			return nil, err
		}
		m := byVersion[version]
		if m == nil {
			m = &Migration{Source: src.Name, Version: version, Name: parts[1]}
			byVersion[version] = m
		}
		if kind == "up" {
			sum := sha256.Sum256(content)
			m.Up, m.Checksum = string(content), hex.EncodeToString(sum[:])
			m.NoTx = strings.HasPrefix(strings.TrimSpace(m.Up), noTxMarker)
		} else {
			m.Down = string(content)
		}
	}
	out := make([]Migration, 0, len(byVersion))
	for _, m := range byVersion {
		if m.Up == "" {
			return nil, fmt.Errorf("migration %s/%06d_%s has a .down.sql but no .up.sql", m.Source, m.Version, m.Name)
		}
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}
