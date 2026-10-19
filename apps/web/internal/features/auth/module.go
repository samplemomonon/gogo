// Package auth is the authentication feature module: it satisfies the
// gateway's module contract (a manifest plus route registration) and owns
// the employee directory and session management.
package auth

import (
	"context"
	"log/slog"

	"github.com/go-chi/chi/v5"
	"google.golang.org/grpc"

	"gogo/internal/features/auth/application"
	"gogo/internal/features/auth/application/ports"
	"gogo/internal/features/auth/presentation/grpcservice"
	"gogo/internal/shared/module"
)

// Module is the auth feature module.
type Module struct {
	svc    ports.AuthService
	logger *slog.Logger
	ping   func(context.Context) error
}

// NewModule wires the auth module around an explicit employee/session store.
// ping backs the health report; pass nil for the in-memory store. merchantStore
// powers the merchant relationship use cases; the tenant-scoped RBAC surface
// works without it.
func NewModule(store ports.EmployeeStore, sessionStore ports.SessionStore, merchantStore ports.MerchantStore, ping func(context.Context) error, logger *slog.Logger, authorization ...ports.AuthorizationStore) *Module {
	return &Module{
		svc:    application.NewService(store, sessionStore, 0, 0, merchantStore, authorization...),
		logger: logger,
		ping:   ping,
	}
}

// RegisterGRPC mounts the auth contract surface on a gRPC server.
func (m *Module) RegisterGRPC(s grpc.ServiceRegistrar) {
	grpcservice.Register(s, m.svc, m.logger)
}

// Manifest is the auth module's remote contract.
func (m *Module) Manifest() module.Descriptor {
	return module.Descriptor{
		Name:        "auth",
		Version:     "1.0.0",
		Entry:       "/login",
		Description: "Employee directory and session management (PIN login, RBAC).",
		Routes: map[string]string{
			"login": "/login",
		},
	}
}

// RegisterRoutes is a no-op: the auth vertical's browsable surface is its login
// page under ui/; this module half mounts nothing on the HTTP router.
func (m *Module) RegisterRoutes(r chi.Router) {}

// Healthy reports auth readiness: ok while the backing database answers.
func (m *Module) Healthy(ctx context.Context) module.Health {
	if m.ping != nil {
		if err := m.ping(ctx); err != nil {
			return module.Health{
				Module:  "auth",
				Status:  module.StatusDown,
				Message: "auth database unavailable",
			}
		}
	}
	return module.Health{Module: "auth", Status: module.StatusOK}
}
