// Package compose wires the auth module for the process that hosts it. Postgres is
// the production backend; without a database the module runs in memory.
package compose

import (
	"context"
	"log/slog"
	"time"

	"gogo/internal/modules/auth"
	"gogo/internal/modules/auth/infrastructure"
	authpostgres "gogo/internal/modules/auth/infrastructure/postgres"
	"gogo/internal/shared/config"
)

// Build wires the auth module's persistence around the store the
// composition root supplies. Without a database it runs on the in-memory store.
func Build(
	parent context.Context,
	cfg *config.AppConfig,
	logger *slog.Logger,
) (*auth.Module, func(), error) {
	if cfg.DatabaseURL == "" {
		store := infrastructure.NewStore()
		return auth.NewModule(store, store, nil, logger), func() {}, nil
	}

	ctx, cancel := context.WithTimeout(parent, cfg.DBPingTimeout+10*time.Second)
	defer cancel()

	pool, err := authpostgres.Connect(ctx, cfg.DatabaseURL, cfg.DBMinConns, cfg.DBMaxConns)
	if err != nil {
		return nil, nil, err
	}
	if err := authpostgres.Migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, nil, err
	}

	employeeStore := authpostgres.NewEmployeeStore(pool)
	sessionStore := authpostgres.NewSessionStore(pool)
	logger.Info("auth backed by postgres")
	return auth.NewModule(employeeStore, sessionStore, pool.Ping, logger), func() { pool.Close() }, nil
}
