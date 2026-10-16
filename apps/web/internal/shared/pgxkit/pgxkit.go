// Package pgxkit is the shared Postgres bootstrap — one implementation of
// "open a pool, prove it with a ping, apply embedded migrations", used by
// every module that owns a Postgres schema.
package pgxkit

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Connect opens a pgx pool and verifies it with a ping, so a bad DSN surfaces
// as a clear startup error instead of the first request's 500. minConns and
// maxConns tune the pool; zero values keep pgx defaults.
func Connect(ctx context.Context, dsn string, minConns, maxConns int) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: parse DSN: %w", err)
	}
	if minConns > 0 {
		cfg.MinConns = int32(minConns)
	}
	if maxConns > 0 {
		cfg.MaxConns = int32(maxConns)
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("postgres: pool: %w", err)
	}

	probeCtx, cancelProbe := context.WithTimeout(ctx, 5*time.Second)
	defer cancelProbe()
	if err := pool.Ping(probeCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: ping: %w", err)
	}

	return pool, nil
}

// Migrate applies every *.sql file under dir in filename order, tracking
// applied versions in schema_migrations so a restart is a no-op. Each
// migration runs in its own transaction; a failure aborts that migration and
// surfaces the error for the composition root to crash on.
func Migrate(ctx context.Context, pool *pgxpool.Pool, migrations embed.FS, dir string) error {
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version     TEXT PRIMARY KEY,
			applied_at  TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("postgres: ensure schema_migrations: %w", err)
	}

	entries, err := fs.Glob(migrations, dir+"/*.sql")
	if err != nil {
		return fmt.Errorf("postgres: list migrations: %w", err)
	}
	sort.Strings(entries)

	for _, entry := range entries {
		version := strings.TrimSuffix(path.Base(entry), ".sql")

		var applied bool
		if err := pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`,
			version,
		).Scan(&applied); err != nil {
			return fmt.Errorf("postgres: check migration %s: %w", version, err)
		}
		if applied {
			continue
		}

		body, err := migrations.ReadFile(entry)
		if err != nil {
			return fmt.Errorf("postgres: read migration %s: %w", version, err)
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("postgres: begin migration %s: %w", version, err)
		}
		if _, err := tx.Exec(ctx, string(body)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("postgres: apply migration %s: %w", version, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO schema_migrations (version) VALUES ($1)`, version,
		); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("postgres: record migration %s: %w", version, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("postgres: commit migration %s: %w", version, err)
		}
	}

	return nil
}
