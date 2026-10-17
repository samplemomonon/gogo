// Package compose wires the sales module for the process that hosts it. Postgres is
// the production backend; without a database the module runs in memory.
package compose

import (
	"context"
	"log/slog"
	"time"

	"google.golang.org/grpc"

	sales "gogo/internal/modules/sales"
	"gogo/internal/modules/sales/application/ports"
	"gogo/internal/modules/sales/infrastructure"
	"gogo/internal/modules/sales/infrastructure/inventoryclient"
	salespostgres "gogo/internal/modules/sales/infrastructure/postgres"
	"gogo/internal/modules/sales/presentation/grpcservice"
	"gogo/internal/shared/authn"
	"gogo/internal/shared/breaker"
	"gogo/internal/shared/config"
)

// NewInventoryAdjuster adapts an inventory.v1 connection to the
// ports.StockAdjuster the sales module composes for a sale's stock effect.
//
// It lives in the module's composition root on purpose: the composition root
// wires the sales module with the raw connection, its breaker and the
// capability sales presents, and never imports the module's infrastructure
// package. That is what the bootstrapping rule in
// internal/architecture/layers_test.go requires ("bootstrap reaches modules
// only through their composition roots"), and it is why the one exception that
// rule used to carry — bootstrap importing inventoryclient directly — could be
// retired.
func NewInventoryAdjuster(
	conn grpc.ClientConnInterface,
	b *breaker.Breaker,
	capabilities ...authn.Capability,
) ports.StockAdjuster {
	return inventoryclient.New(conn, b, capabilities...)
}

// Build wires the sales module's persistence around the stock adjuster the
// composition root supplies. Without a database it runs on the in-memory store.
func Build(
	parent context.Context,
	cfg *config.AppConfig,
	stock ports.StockAdjuster,
	logger *slog.Logger,
	authorizers ...grpcservice.Authorizer,
) (*sales.Module, func(), error) {
	if cfg.DatabaseURL == "" {
		return sales.NewModule(infrastructure.NewStore(), stock, nil, logger, authorizers...), func() {}, nil
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
	return sales.NewModule(store, stock, store.Ping, logger, authorizers...), func() { pool.Close() }, nil
}
