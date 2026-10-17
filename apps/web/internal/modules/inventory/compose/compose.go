// Package compose wires the inventory module for the process that hosts it. Postgres
// is the production backend; without a database the module runs in memory.
package compose

import (
	"context"
	"log/slog"
	"time"

	inventory "gogo/internal/modules/inventory"
	"gogo/internal/modules/inventory/infrastructure"
	inventorypostgres "gogo/internal/modules/inventory/infrastructure/postgres"
	"gogo/internal/shared/config"
)

// Build wires the inventory module's persistence. Without a database it runs on the
// in-memory store. With cfg.DatabaseURL set it connects a Postgres pool and applies
// migrations; the returned close function releases the pool.
func Build(
	parent context.Context,
	cfg *config.AppConfig,
	logger *slog.Logger,
	security ...inventory.Security,
) (*inventory.Module, func(), error) {
	if cfg.DatabaseURL == "" {
		return inventory.NewModuleWithDeps(infrastructure.NewStockRepository(), nil, logger, security...), func() {}, nil
	}

	ctx, cancel := context.WithTimeout(parent, cfg.DBPingTimeout+10*time.Second)
	defer cancel()

	pool, err := inventorypostgres.Connect(ctx, cfg.DatabaseURL, cfg.DBMinConns, cfg.DBMaxConns)
	if err != nil {
		return nil, nil, err
	}
	if err := inventorypostgres.Migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, nil, err
	}

	repo := inventorypostgres.NewStockRepository(pool)
	logger.Info("inventory backed by postgres")
	return inventory.NewModuleWithDeps(repo, repo.Ping, logger, security...), func() { pool.Close() }, nil
}
