// Package database opens the PostgreSQL connection pool and applies schema
// migrations. The pool is returned to the caller rather than stored in a
// package-level variable, which keeps the dependency explicit and makes the
// storage layer injectable in tests.
package database

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strings"
	"time"

	_ "github.com/lib/pq" // register the "postgres" driver
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

const migrationsDir = "migrations"

// Connect opens the connection pool and verifies it is usable.
func Connect(ctx context.Context, dsn string, opts Options) (*sql.DB, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	db.SetMaxOpenConns(opts.MaxOpenConns)
	db.SetMaxIdleConns(opts.MaxIdleConns)
	db.SetConnMaxLifetime(opts.ConnMaxLifetime)

	if err := db.PingContext(ctx); err != nil {
		// Release the pool we just created before reporting the failure.
		_ = db.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return db, nil
}

// Options carries the connection pool tuning knobs.
type Options struct {
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

// Migrate applies every embedded migration in lexical order. Migrations are
// recorded in schema_migrations so re-running is a no-op, which makes the
// function safe to call on every boot including concurrent replicas.
func Migrate(ctx context.Context, db *sql.DB, log *slog.Logger) error {
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	entries, err := fs.ReadDir(migrationsFS, migrationsDir)
	if err != nil {
		return fmt.Errorf("read embedded migrations: %w", err)
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)

	applied, err := appliedVersions(ctx, db)
	if err != nil {
		return err
	}

	for _, name := range names {
		version, ok := strings.CutSuffix(name, ".sql")
		if !ok {
			return fmt.Errorf("migration %q does not have a .sql suffix", name)
		}
		if applied[version] {
			continue
		}

		body, err := migrationsFS.ReadFile(migrationsDir + "/" + name)
		if err != nil {
			return fmt.Errorf("read migration %q: %w", name, err)
		}

		// Each migration runs in its own transaction together with the
		// bookkeeping insert, so a failure cannot leave a half-applied schema.
		if err := applyMigration(ctx, db, version, string(body)); err != nil {
			return fmt.Errorf("apply migration %q: %w", name, err)
		}
		log.Info("migration applied", "version", version)
	}
	return nil
}

func applyMigration(ctx context.Context, db *sql.DB, version, body string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, body); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version) VALUES ($1)`, version,
	); err != nil {
		return err
	}
	return tx.Commit()
}

func appliedVersions(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("query applied migrations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	applied := make(map[string]bool)
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			return nil, fmt.Errorf("scan applied migration: %w", err)
		}
		applied[version] = true
	}
	// rows.Err reports iteration failures that happen after the last Next.
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate applied migrations: %w", err)
	}
	return applied, nil
}

// Ping verifies the database is reachable, used by the readiness probe.
func Ping(ctx context.Context, db *sql.DB) error {
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}
	return nil
}
