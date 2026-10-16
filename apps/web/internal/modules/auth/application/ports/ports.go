// Package ports defines the auth module's persistence and application surface.
package ports

import (
	"context"
	"time"

	"gogo/internal/modules/auth/domain"
)

// EmployeeStore is the employee persistence surface.
type EmployeeStore interface {
	Create(ctx context.Context, tenant domain.Tenant, employee *domain.Employee) error
	Get(ctx context.Context, tenant domain.Tenant, id string) (*domain.Employee, error)
	GetByEmail(ctx context.Context, tenant domain.Tenant, email string) (*domain.Employee, error)
	Update(ctx context.Context, tenant domain.Tenant, employee *domain.Employee) error
	List(ctx context.Context, tenant domain.Tenant, limit int) ([]*domain.Employee, error)
}

// SessionStore is the session persistence surface.
type SessionStore interface {
	CreateSession(ctx context.Context, session *domain.Session) error
	GetSession(ctx context.Context, id string) (*domain.Session, error)
	RevokeSession(ctx context.Context, id string) error
	ExtendSession(ctx context.Context, id string, ttl time.Duration) error
}

// AuthService is the auth application surface. The gRPC adapter and pages depend on it.
type AuthService interface {
	RegisterEmployee(ctx context.Context, tenant domain.Tenant, id, name, email, pin string, role domain.Role) (*domain.Employee, error)
	GetEmployee(ctx context.Context, tenant domain.Tenant, id string) (*domain.Employee, error)
	GetEmployeeByEmail(ctx context.Context, tenant domain.Tenant, email string) (*domain.Employee, error)
	UpdateEmployee(ctx context.Context, tenant domain.Tenant, id, name, email string, role domain.Role) (*domain.Employee, error)
	ListEmployees(ctx context.Context, tenant domain.Tenant, limit int) ([]*domain.Employee, error)
	SetEmployeePIN(ctx context.Context, tenant domain.Tenant, id, pin string) error
	ActivateEmployee(ctx context.Context, tenant domain.Tenant, id string) error
	DeactivateEmployee(ctx context.Context, tenant domain.Tenant, id string) error

	Login(ctx context.Context, tenant domain.Tenant, email, pin string) (*domain.Session, *domain.Employee, error)
	Logout(ctx context.Context, sessionID string) error
	ValidateSession(ctx context.Context, sessionID string) (*domain.Session, *domain.Employee, error)
	ExtendSession(ctx context.Context, sessionID string) (*domain.Session, error)
}
