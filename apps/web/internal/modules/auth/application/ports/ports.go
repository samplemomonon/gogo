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

// AuthorizationStore evaluates a supported permission against current,
// tenant-scoped role assignments. Unknown permissions must return false.
type AuthorizationStore interface {
	HasPermission(ctx context.Context, tenantID, employeeID, permissionKey string) (bool, error)
	CreateRole(ctx context.Context, tenantID, actorID, roleID, name, description, changeReference string) error
	GrantPermission(ctx context.Context, tenantID, actorID, roleID, permissionKey string, expiresAt *time.Time, reason, changeReference string) (string, error)
	RevokePermission(ctx context.Context, tenantID, actorID, grantID, reason, changeReference string) error
	AssignRole(ctx context.Context, tenantID, actorID, employeeID, roleID string, validFrom, expiresAt *time.Time, reason, changeReference string) (string, error)
	RevokeRoleAssignment(ctx context.Context, tenantID, actorID, assignmentID, reason, changeReference string) error
	ListRoles(ctx context.Context, tenantID string) ([]AuthorizationRole, error)
	ListAssignments(ctx context.Context, tenantID, employeeID string, includeHistory bool) ([]RoleAssignment, error)
	ListTenantAssignments(ctx context.Context, tenantID string, limit int, afterAssignedAt *time.Time, afterID string) ([]RoleAssignment, bool, error)
	ListAdminEvents(ctx context.Context, tenantID string, limit int, afterTime *time.Time, afterID string) ([]AuthorizationAdminEvent, bool, error)
}

type AuthorizationRole struct {
	ID, Name, Description string
	Permissions           []string
}

type RoleAssignment struct {
	ID, EmployeeID, RoleID, Reason string
	AssignedBy, Source             string
	AssignedAt, ValidFrom          time.Time
	ExpiresAt, RevokedAt           *time.Time
}

type AuthorizationAdminEvent struct {
	ID, Action, SubjectID, RoleID                       string
	ActorEmployeeID, ActorDatabaseUser, ChangeReference string
	Reason                                              string
	OccurredAt                                          time.Time
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
	CheckPermission(ctx context.Context, sessionID, permissionKey string) (bool, error)
	CreateRole(ctx context.Context, sessionID, name, description, changeReference string) (string, error)
	GrantPermission(ctx context.Context, sessionID, roleID, permissionKey string, expiresAt *time.Time, reason, changeReference string) (string, error)
	RevokePermission(ctx context.Context, sessionID, grantID, reason, changeReference string) error
	AssignRole(ctx context.Context, sessionID, employeeID, roleID string, validFrom, expiresAt *time.Time, reason, changeReference string) (string, error)
	RevokeRoleAssignment(ctx context.Context, sessionID, assignmentID, reason, changeReference string) error
	ListAuthorizationRoles(ctx context.Context, sessionID string) ([]AuthorizationRole, error)
	ListRoleAssignments(ctx context.Context, sessionID, employeeID string, includeHistory bool) ([]RoleAssignment, error)
	ListTenantRoleAssignments(ctx context.Context, sessionID string, pageSize int, pageToken string) ([]RoleAssignment, string, error)
	ListAuthorizationAdminEvents(ctx context.Context, sessionID string, pageSize int, pageToken string) ([]AuthorizationAdminEvent, string, error)
}
