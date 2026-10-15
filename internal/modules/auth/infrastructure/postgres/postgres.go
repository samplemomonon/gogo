// Package postgres provides the PostgreSQL implementation of the auth ports.
package postgres

import (
	"context"
	"embed"

	"github.com/jackc/pgx/v5/pgxpool"

	"gogo/internal/shared/pgxkit"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Connect opens the auth pool. See pgxkit.Connect.
func Connect(ctx context.Context, dsn string, min, max int) (*pgxpool.Pool, error) {
	return pgxkit.Connect(ctx, dsn, min, max)
}

// Migrate applies the auth module's embedded migrations. See pgxkit.Migrate.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	return pgxkit.Migrate(ctx, pool, migrationsFS, "migrations")
}
