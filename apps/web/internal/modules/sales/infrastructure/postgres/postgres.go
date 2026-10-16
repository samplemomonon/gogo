package postgres

import (
	"context"
	"embed"

	"github.com/jackc/pgx/v5/pgxpool"

	"gogo/internal/shared/pgxkit"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Connect opens the sales pool. See pgxkit.Connect.
func Connect(ctx context.Context, dsn string, minConns, maxConns int) (*pgxpool.Pool, error) {
	return pgxkit.Connect(ctx, dsn, minConns, maxConns)
}

// Migrate applies the sales module's embedded migrations. See pgxkit.Migrate.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	return pgxkit.Migrate(ctx, pool, migrationsFS, "migrations")
}
