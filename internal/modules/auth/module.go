// Package auth is the authentication feature module: it satisfies the
// gateway's module contract (a manifest plus route registration) and owns
// the employee directory and session management.
package auth

import (
	"context"
	"log/slog"

	"github.com/go-chi/chi/v5"
	"google.golang.org/grpc"

	"gogo/internal/modules/auth/application"
	"gogo/internal/modules/auth/application/ports"
	"gogo/internal/modules/auth/presentation/grpcservice"
	"gogo/internal/shared/module"
)

// Module is the auth feature module.
type Module struct {
	svc    ports.AuthService
	logger *slog.Logger
	ping   func(context.Context) error
}

// NewModule wires the auth module around an explicit employee/session store.
// ping backs the health report; pass nil for the in-memory store.
func NewModule(store ports.EmployeeStore, sessionStore ports.SessionStore, ping func(context.Context) error, logger *slog.Logger) *Module {
	return &Module{
		svc:    application.NewService(store, sessionStore, 0, 0),
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

// RegisterRoutes is a no-op: the auth module has no browsable HTTP surface of its own.
// The login page lives in internal/pages/auth.
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
