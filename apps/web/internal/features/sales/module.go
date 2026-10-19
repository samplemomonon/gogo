// Package sales is the point-of-sale source of truth: it owns completed
// transactions and their line items, and composes the inventory service's stock
// adjustment for a sale's stock effect. It carries no HTTP routes of its own — the
// seller console consumes it as a remote peer.
package sales

import (
	"context"
	"log/slog"

	"github.com/go-chi/chi/v5"
	"google.golang.org/grpc"

	"gogo/internal/features/sales/application"
	"gogo/internal/features/sales/application/ports"
	grpcservice "gogo/internal/features/sales/presentation/grpcservice"
	"gogo/internal/shared/module"
)

// Module is the sales feature module.
type Module struct {
	service    ports.SalesService
	logger     *slog.Logger
	ping       func(context.Context) error
	authorizer grpcservice.Authorizer
}

// NewModule wires the sales module around an explicit transaction store and stock
// adjuster. ping backs the health report; pass nil for the in-memory store.
func NewModule(store ports.Store, stock ports.StockAdjuster, ping func(context.Context) error, logger *slog.Logger, authorizers ...grpcservice.Authorizer) *Module {
	var authorizer grpcservice.Authorizer
	if len(authorizers) > 0 {
		authorizer = authorizers[0]
	}
	return &Module{service: application.NewService(store, stock), logger: logger, ping: ping, authorizer: authorizer}
}

// RegisterGRPC mounts the sales contract surface on a gRPC server.
func (m *Module) RegisterGRPC(s grpc.ServiceRegistrar) {
	grpcservice.Register(s, m.service, m.logger, m.ping, m.authorizer)
}

// Manifest is the sales module's remote contract.
func (m *Module) Manifest() module.Descriptor {
	return module.Descriptor{
		Name:        "sales",
		Version:     "1.0.0",
		Entry:       "/seller/kasir",
		Description: "Point-of-sale source of truth: transactions and line items, composing the inventory.v1 stock adjustment for a sale's stock effect.",
		Routes: map[string]string{
			"pos":     "/seller/kasir",
			"history": "/seller/transaksi",
		},
	}
}

// RegisterRoutes is a no-op: the sales module has no browsable HTTP surface.
func (m *Module) RegisterRoutes(r chi.Router) {}

// Healthy reports sales readiness: ok while the backing database answers.
func (m *Module) Healthy(ctx context.Context) module.Health {
	if m.ping != nil {
		if err := m.ping(ctx); err != nil {
			return module.Health{
				Module:  "sales",
				Status:  module.StatusDown,
				Message: "sales database unavailable",
			}
		}
	}
	return module.Health{Module: "sales", Status: module.StatusOK}
}
