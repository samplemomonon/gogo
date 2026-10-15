// The gateway: the gogo application. It serves the home storefront, the seller console,
// billing, the module registry, and health/metrics.
//
// It owns no product data. Product writes go to product-service, and reads come from the
// product-listing and product-search read models — all over their versioned gRPC
// contracts, all in another process (see ARCHITECTURE.md D-018). What this process still
// owns outright is the inventory ledger, the sales history, employees and sessions.
package main

import (
	"context"
	"log/slog"
	"os"

	"gogo/internal/bootstrap"
	"gogo/internal/shared/config"
	"gogo/internal/shared/logging"
)

func main() {
	cfg, err := config.LoadValidated()
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	logger := logging.New(cfg)

	ctx, cancel := bootstrap.SignalContext(context.Background())
	defer cancel()

	// BuildGateway composes the whole gateway and returns one shutdown hook that
	// releases everything the graph acquired: the product-platform connections, the
	// in-process module persistence, and the product change feed.
	app, closeAll, err := bootstrap.BuildGateway(ctx, cfg, logger)
	if err != nil {
		logger.Error("gateway startup failed", "error", err)
		os.Exit(1)
	}
	defer closeAll()

	if err := app.Run(ctx); err != nil {
		logger.Error("server failed", "error", err)
		os.Exit(1)
	}

	logger.Info("server stopped cleanly")
}
