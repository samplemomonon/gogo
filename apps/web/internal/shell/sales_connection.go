// Sales connection wiring for the gateway.
package shell

import (
	"context"
	"log/slog"

	"google.golang.org/grpc"

	salescompose "gogo/internal/features/sales/compose"
	"gogo/internal/shared/breaker"
	"gogo/internal/shared/config"
	"gogo/internal/shared/grpcx"
	"gogo/internal/shared/module"
)

// connectSales builds the sales module in-process and exposes it over an in-process
// bufconn gRPC server, registering the module with the registry. The sales module
// applies a sale's stock effect through the inventory connection, so the seller
// console can compose a checkout across both contracts.
func connectSales(
	parent context.Context,
	cfg *config.AppConfig,
	inventoryConn *grpc.ClientConn,
	inventoryBreaker *breaker.Breaker,
	logger *slog.Logger,
	registry *module.Registry,
	security ...moduleSecurity,
) (conn *grpc.ClientConn, closeConn func(), err error) {
	var moduleCfg moduleSecurity
	if len(security) > 0 {
		moduleCfg = security[0]
	}
	stock := salescompose.NewInventoryAdjuster(inventoryConn, inventoryBreaker, moduleCfg.capability)
	module, closeModule, err := salescompose.Build(parent, cfg, stock, logger, moduleCfg.authorizer)
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
