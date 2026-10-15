// Package inventory is the stock source of truth: it owns every product's sellable
// quantity and the append-only movement ledger behind it, and exposes them over the
// inventory.v1 gRPC contract. It carries no HTTP routes of its own — the seller
// console consumes it as a remote peer.
package inventory

import (
	"context"
	"log/slog"

	"github.com/go-chi/chi/v5"
	"google.golang.org/grpc"

	"gogo/internal/modules/inventory/application/ports"
	"gogo/internal/modules/inventory/infrastructure"
	grpcservice "gogo/internal/modules/inventory/presentation/grpcservice"
	"gogo/internal/shared/module"
)

type Module struct {
	service ports.StockService
	logger  *slog.Logger
	ping    func(context.Context) error
}

// NewModule wires the in-memory store — the development default that needs no
// database.
func NewModule(logger *slog.Logger) *Module {
	return NewModuleWithDeps(infrastructure.NewStockRepository(), nil, logger)
}

// NewModuleWithDeps wires the module around explicit persistence. ping backs the
// health report; pass nil for the in-memory store.
func NewModuleWithDeps(service ports.StockService, ping func(context.Context) error, logger *slog.Logger) *Module {
	return &Module{service: service, logger: logger, ping: ping}
}

// RegisterGRPC mounts the inventory contract surface on a gRPC server.
func (m *Module) RegisterGRPC(s grpc.ServiceRegistrar) {
	grpcservice.Register(s, m.service, m.logger, m.ping)
}

// Manifest is the inventory module's remote contract.
func (m *Module) Manifest() module.Descriptor {
	return module.Descriptor{
		Name:        "inventory",
		Version:     "1.0.0",
		Entry:       "/seller/stok",
		Description: "Stock source of truth: product stock levels and the append-only movement ledger, consumed over the inventory.v1 gRPC contract.",
		Routes: map[string]string{
			"stock": "/seller/stok",
		},
	}
}

// RegisterRoutes is a no-op: the inventory module has no browsable HTTP surface.
func (m *Module) RegisterRoutes(r chi.Router) {}

// Healthy reports inventory readiness: ok while the backing database answers.
func (m *Module) Healthy(ctx context.Context) module.Health {
	if m.ping != nil {
		if err := m.ping(ctx); err != nil {
			return module.Health{
				Module:  "inventory",
				Status:  module.StatusDown,
				Message: "inventory database unavailable",
			}
		}
	}
	return module.Health{Module: "inventory", Status: module.StatusOK}
}
