// Package compose wires the sales module for the process that hosts it. Postgres is
// the production backend; without a database the module runs in memory.
package compose

import (
	"context"
	"log/slog"
	"time"

	sales "gogo/internal/modules/sales"
	"gogo/internal/modules/sales/application/ports"
	"gogo/internal/modules/sales/infrastructure"
	salespostgres "gogo/internal/modules/sales/infrastructure/postgres"
	"gogo/internal/shared/config"
)

// Build wires the sales module's persistence around the stock adjuster the
// composition root supplies. Without a database it runs on the in-memory store.
func Build(
	parent context.Context,
	cfg *config.AppConfig,
	stock ports.StockAdjuster,
	logger *slog.Logger,
) (*sales.Module, func(), error) {
	if cfg.DatabaseURL == "" {
		return sales.NewModule(infrastructure.NewStore(), stock, nil, logger), func() {}, nil
	}

	ctx, cancel := context.WithTimeout(parent, cfg.DBPingTimeout+10*time.Second)
	defer cancel()

	pool, err := salespostgres.Connect(ctx, cfg.DatabaseURL, cfg.DBMinConns, cfg.DBMaxConns)
	if err != nil {
		return nil, nil, err
	}
	if err := salespostgres.Migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, nil, err
	}

	store := salespostgres.NewStore(pool)
	logger.Info("sales backed by postgres")
	return sales.NewModule(store, stock, store.Ping, logger), func() { pool.Close() }, nil
}
