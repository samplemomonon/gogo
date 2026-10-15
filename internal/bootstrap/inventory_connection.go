// Inventory connection wiring for the gateway.
package bootstrap

import (
	"context"
	"log/slog"

	"google.golang.org/grpc"

	inventorycompose "gogo/internal/modules/inventory/compose"
	"gogo/internal/shared/config"
	"gogo/internal/shared/grpcx"
	"gogo/internal/shared/module"
)

// connectInventory builds the inventory module in-process and exposes it over an
// in-process bufconn gRPC server, registering the module with the registry. The
// seller console reaches it through the same inventory.v1 contract a split deployment
// would use, so the two shapes cannot drift.
func connectInventory(
	parent context.Context,
	cfg *config.AppConfig,
	logger *slog.Logger,
	registry *module.Registry,
) (conn *grpc.ClientConn, closeConn func(), err error) {
	module, closeModule, err := inventorycompose.Build(parent, cfg, logger)
	if err != nil {
		return nil, nil, err
	}
	registry.AddAll(module)

	conn, closeServer, err := grpcx.InProcess(module.RegisterGRPC)
	if err != nil {
		closeModule()
		return nil, nil, err
	}
	return conn, func() { closeServer(); closeModule() }, nil
}
