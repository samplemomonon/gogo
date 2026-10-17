// Package application holds the auth facade: it composes the employee and session stores.
package application

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"gogo/internal/modules/auth/application/ports"
	"gogo/internal/modules/auth/domain"
)

var _ ports.AuthService = (*Service)(nil)

// Service implements ports.AuthService.
type Service struct {
	employees     ports.EmployeeStore
	sessions      ports.SessionStore
	authorization ports.AuthorizationStore
	pinCost       int
	ttl           time.Duration
}

// NewService creates a new auth service.
func NewService(employees ports.EmployeeStore, sessions ports.SessionStore, pinCost int, ttl time.Duration, authorization ...ports.AuthorizationStore) *Service {
	if pinCost <= 0 {
		pinCost = bcrypt.DefaultCost
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	var authz ports.AuthorizationStore
	if len(authorization) > 0 {
		authz = authorization[0]
	}
	return &Service{employees: employees, sessions: sessions, authorization: authz, pinCost: pinCost, ttl: ttl}
}

// generateID generates a random ID.
func generateID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// hashPIN hashes a PIN using bcrypt.
func (s *Service) hashPIN(pin string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(pin), s.pinCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// checkPIN verifies a PIN against a bcrypt hash.
func (s *Service) checkPIN(hash, pin string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pin))
}

// RegisterEmployee registers a new employee.
func (s *Service) RegisterEmployee(ctx context.Context, tenant domain.Tenant, id, name, email, pin string, role domain.Role) (*domain.Employee, error) {
	// Check for duplicate email
	existing, err := s.employees.GetByEmail(ctx, tenant, email)
	if err == nil && existing != nil {
		return nil, domain.ErrDuplicateEmail
	}

	if err := domain.ValidatePIN(pin); err != nil {
		return nil, err
	}

	hash, err := s.hashPIN(pin)
	if err != nil {
		return nil, err
	}

	employee, err := domain.NewEmployee(tenant, id, name, email, hash, role)
	if err != nil {
		return nil, err
	}

	if err := s.employees.Create(ctx, tenant, employee); err != nil {
		return nil, err
	}
	return employee, nil
}

// GetEmployee returns an employee by ID.
func (s *Service) GetEmployee(ctx context.Context, tenant domain.Tenant, id string) (*domain.Employee, error) {
	return s.employees.Get(ctx, tenant, id)
}

// GetEmployeeByEmail returns an employee by email.
func (s *Service) GetEmployeeByEmail(ctx context.Context, tenant domain.Tenant, email string) (*domain.Employee, error) {
	return s.employees.GetByEmail(ctx, tenant, email)
}

// UpdateEmployee updates an employee.
func (s *Service) UpdateEmployee(ctx context.Context, tenant domain.Tenant, id, name, email string, role domain.Role) (*domain.Employee, error) {
	employee, err := s.employees.Get(ctx, tenant, id)
	if err != nil {
		return nil, err
	}
	if err := employee.Update(name, email, role); err != nil {
		return nil, err
	}
	if err := s.employees.Update(ctx, tenant, employee); err != nil {
		return nil, err
	}
	return employee, nil
}

// ListEmployees lists employees.
func (s *Service) ListEmployees(ctx context.Context, tenant domain.Tenant, limit int) ([]*domain.Employee, error) {
	return s.employees.List(ctx, tenant, limit)
}

// SetEmployeePIN updates an employee's PIN.
func (s *Service) SetEmployeePIN(ctx context.Context, tenant domain.Tenant, id, pin string) error {
	employee, err := s.employees.Get(ctx, tenant, id)
	if err != nil {
		return err
	}
	if err := domain.ValidatePIN(pin); err != nil {
		return err
	}
	hash, err := s.hashPIN(pin)
	if err != nil {
		return err
	}
	if err := employee.SetPIN(hash); err != nil {
		return err
	}
	return s.employees.Update(ctx, tenant, employee)
}

// ActivateEmployee activates an employee.
func (s *Service) ActivateEmployee(ctx context.Context, tenant domain.Tenant, id string) error {
	employee, err := s.employees.Get(ctx, tenant, id)
	if err != nil {
		return err
	}
	employee.Activate()
	return s.employees.Update(ctx, tenant, employee)
}

// DeactivateEmployee deactivates an employee.
func (s *Service) DeactivateEmployee(ctx context.Context, tenant domain.Tenant, id string) error {
	employee, err := s.employees.Get(ctx, tenant, id)
	if err != nil {
		return err
	}
	employee.Deactivate()
	return s.employees.Update(ctx, tenant, employee)
}

// Login authenticates an employee and creates a session.
func (s *Service) Login(ctx context.Context, tenant domain.Tenant, email, pin string) (*domain.Session, *domain.Employee, error) {
	employee, err := s.employees.GetByEmail(ctx, tenant, email)
	if err != nil {
		return nil, nil, err
	}
	if !employee.Active {
		return nil, nil, domain.ErrInvalidEmployee
	}
	if err := s.checkPIN(employee.PINHash, pin); err != nil {
		return nil, nil, domain.ErrInvalidCredentials
	}

	session, err := domain.NewSession(tenant, generateID(), employee.ID, s.ttl)
	if err != nil {
		return nil, nil, err
	}
	if err := s.sessions.CreateSession(ctx, session); err != nil {
		return nil, nil, err
	}
	return session, employee, nil
}

// Logout revokes a session.
func (s *Service) Logout(ctx context.Context, sessionID string) error {
	return s.sessions.RevokeSession(ctx, sessionID)
}

// ValidateSession validates a session and returns it with the employee.
func (s *Service) ValidateSession(ctx context.Context, sessionID string) (*domain.Session, *domain.Employee, error) {
	session, err := s.sessions.GetSession(ctx, sessionID)
	if err != nil {
		return nil, nil, err
	}
	if !session.IsValid() {
		return nil, nil, domain.ErrExpired
	}
	employee, err := s.employees.Get(ctx, domain.Tenant(session.TenantID), session.EmployeeID)
	if err != nil {
		return nil, nil, err
	}
	if !employee.Active {
		return nil, nil, domain.ErrInvalidEmployee
	}
	return session, employee, nil
}

// ExtendSession extends a session's TTL.
func (s *Service) ExtendSession(ctx context.Context, sessionID string) (*domain.Session, error) {
	session, err := s.sessions.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if !session.IsValid() {
		return nil, domain.ErrExpired
	}
	if err := session.Extend(s.ttl); err != nil {
		return nil, err
	}
	if err := s.sessions.ExtendSession(ctx, sessionID, s.ttl); err != nil {
		return nil, err
	}
	return session, nil
}

// CheckPermission evaluates a permission for the principal resolved from a
// valid session. Caller-supplied employee or tenant values are never trusted.
func (s *Service) CheckPermission(ctx context.Context, sessionID, permissionKey string) (bool, error) {
	if s.authorization == nil || permissionKey == "" {
		return false, nil
	}
	session, employee, err := s.ValidateSession(ctx, sessionID)
	if err != nil {
		return false, err
	}
	return s.authorization.HasPermission(ctx, session.TenantID, employee.ID, permissionKey)
}

func (s *Service) policyAdministrator(ctx context.Context, sessionID, permissionKey string) (*domain.Session, *domain.Employee, error) {
	if s.authorization == nil {
		return nil, nil, domain.ErrPolicyDenied
	}
	session, employee, err := s.ValidateSession(ctx, sessionID)
	if err != nil {
		return nil, nil, err
	}
	allowed, err := s.authorization.HasPermission(ctx, session.TenantID, employee.ID, permissionKey)
	if err != nil {
		return nil, nil, err
	}
	if !allowed {
		return nil, nil, domain.ErrPolicyDenied
	}
	return session, employee, nil
}

func validChange(reason, changeReference string) bool {
	return strings.TrimSpace(reason) != "" && len(reason) <= 1000 &&
		strings.TrimSpace(changeReference) != "" && len(changeReference) <= 200
}

// CreateRole creates a tenant-local role. A role has no authority until known
// permissions are explicitly granted to it.
func (s *Service) CreateRole(ctx context.Context, sessionID, name, description, changeReference string) (string, error) {
	session, actor, err := s.policyAdministrator(ctx, sessionID, "authz.role.manage")
	if err != nil {
		return "", err
	}
	name = strings.TrimSpace(name)
	description = strings.TrimSpace(description)
	changeReference = strings.TrimSpace(changeReference)
	if name == "" || len(name) > 120 || len(description) > 1000 || changeReference == "" || len(changeReference) > 200 {
		return "", domain.ErrInvalidPolicyChange
	}
	roleID := uuid.NewString()
	if err := s.authorization.CreateRole(ctx, session.TenantID, actor.ID, roleID, name, description, changeReference); err != nil {
		return "", err
	}
	return roleID, nil
}

// GrantPermission composes one supported capability into a tenant role.
func (s *Service) GrantPermission(ctx context.Context, sessionID, roleID, permissionKey string, expiresAt *time.Time, reason, changeReference string) (string, error) {
	session, actor, err := s.policyAdministrator(ctx, sessionID, "authz.permission.manage")
	if err != nil {
		return "", err
	}
	roleID, permissionKey = strings.TrimSpace(roleID), strings.TrimSpace(permissionKey)
	reason, changeReference = strings.TrimSpace(reason), strings.TrimSpace(changeReference)
	if roleID == "" || permissionKey == "" || !validChange(reason, changeReference) || (expiresAt != nil && !expiresAt.After(time.Now())) {
		return "", domain.ErrInvalidPolicyChange
	}
	return s.authorization.GrantPermission(ctx, session.TenantID, actor.ID, roleID, permissionKey, expiresAt, reason, changeReference)
}

func (s *Service) RevokePermission(ctx context.Context, sessionID, grantID, reason, changeReference string) error {
	session, actor, err := s.policyAdministrator(ctx, sessionID, "authz.permission.manage")
	if err != nil {
		return err
	}
	grantID, reason, changeReference = strings.TrimSpace(grantID), strings.TrimSpace(reason), strings.TrimSpace(changeReference)
	if grantID == "" || !validChange(reason, changeReference) {
		return domain.ErrInvalidPolicyChange
	}
	return s.authorization.RevokePermission(ctx, session.TenantID, actor.ID, grantID, reason, changeReference)
}

func (s *Service) AssignRole(ctx context.Context, sessionID, employeeID, roleID string, validFrom, expiresAt *time.Time, reason, changeReference string) (string, error) {
	session, actor, err := s.policyAdministrator(ctx, sessionID, "authz.assignment.manage")
	if err != nil {
		return "", err
	}
	employeeID, roleID = strings.TrimSpace(employeeID), strings.TrimSpace(roleID)
	reason, changeReference = strings.TrimSpace(reason), strings.TrimSpace(changeReference)
	start := time.Now()
	if validFrom != nil {
		start = *validFrom
	}
	if employeeID == "" || roleID == "" || !validChange(reason, changeReference) || (expiresAt != nil && !expiresAt.After(start)) {
		return "", domain.ErrInvalidPolicyChange
	}
	return s.authorization.AssignRole(ctx, session.TenantID, actor.ID, employeeID, roleID, validFrom, expiresAt, reason, changeReference)
}

func (s *Service) RevokeRoleAssignment(ctx context.Context, sessionID, assignmentID, reason, changeReference string) error {
	session, actor, err := s.policyAdministrator(ctx, sessionID, "authz.assignment.manage")
	if err != nil {
		return err
	}
	assignmentID, reason, changeReference = strings.TrimSpace(assignmentID), strings.TrimSpace(reason), strings.TrimSpace(changeReference)
	if assignmentID == "" || !validChange(reason, changeReference) {
		return domain.ErrInvalidPolicyChange
	}
	return s.authorization.RevokeRoleAssignment(ctx, session.TenantID, actor.ID, assignmentID, reason, changeReference)
}

func (s *Service) ListAuthorizationRoles(ctx context.Context, sessionID string) ([]ports.AuthorizationRole, error) {
	session, _, err := s.policyAdministrator(ctx, sessionID, "authz.role.manage")
	if err != nil {
		return nil, err
	}
	return s.authorization.ListRoles(ctx, session.TenantID)
}

func (s *Service) ListRoleAssignments(ctx context.Context, sessionID, employeeID string, includeHistory bool) ([]ports.RoleAssignment, error) {
	session, _, err := s.policyAdministrator(ctx, sessionID, "authz.assignment.manage")
	if err != nil {
		return nil, err
	}
	employeeID = strings.TrimSpace(employeeID)
	if employeeID == "" {
		return nil, domain.ErrInvalidPolicyChange
	}
	return s.authorization.ListAssignments(ctx, session.TenantID, employeeID, includeHistory)
}

type assignmentCursor struct {
	AssignedAt time.Time `json:"a"`
	ID         string    `json:"i"`
}

func (s *Service) ListTenantRoleAssignments(ctx context.Context, sessionID string, pageSize int, pageToken string) ([]ports.RoleAssignment, string, error) {
	session, _, err := s.policyAdministrator(ctx, sessionID, "authz.assignment.review")
	if err != nil {
		return nil, "", err
	}
	if pageSize <= 0 {
		pageSize = 100
	}
	if pageSize > 500 {
		pageSize = 500
	}
	var cursor assignmentCursor
	if pageToken != "" {
		payload, decodeErr := base64.RawURLEncoding.DecodeString(pageToken)
		if decodeErr != nil || json.Unmarshal(payload, &cursor) != nil || cursor.ID == "" || cursor.AssignedAt.IsZero() {
			return nil, "", domain.ErrInvalidPolicyChange
		}
	}
	items, more, err := s.authorization.ListTenantAssignments(ctx, session.TenantID, pageSize+1, optionalCursorTime(cursor), cursor.ID)
	if err != nil {
		return nil, "", err
	}
	if !more {
		return items, "", nil
	}
	items = items[:pageSize]
	last := items[len(items)-1]
	payload, err := json.Marshal(assignmentCursor{AssignedAt: last.AssignedAt, ID: last.ID})
	if err != nil {
		return nil, "", err
	}
	return items, base64.RawURLEncoding.EncodeToString(payload), nil
}

func optionalCursorTime(cursor assignmentCursor) *time.Time {
	if cursor.ID == "" {
		return nil
	}
	return &cursor.AssignedAt
}

type adminEventCursor struct {
	OccurredAt time.Time `json:"t"`
	ID         string    `json:"i"`
}

func (s *Service) ListAuthorizationAdminEvents(ctx context.Context, sessionID string, pageSize int, pageToken string) ([]ports.AuthorizationAdminEvent, string, error) {
	session, _, err := s.policyAdministrator(ctx, sessionID, "authz.audit.read")
	if err != nil {
		return nil, "", err
	}
	if pageSize <= 0 {
		pageSize = 100
	}
	if pageSize > 500 {
		pageSize = 500
	}
	var cursor adminEventCursor
	if pageToken != "" {
		payload, decodeErr := base64.RawURLEncoding.DecodeString(pageToken)
		if decodeErr != nil || json.Unmarshal(payload, &cursor) != nil || cursor.ID == "" || cursor.OccurredAt.IsZero() {
			return nil, "", domain.ErrInvalidPolicyChange
		}
	}
	events, more, err := s.authorization.ListAdminEvents(ctx, session.TenantID, pageSize+1, optionalAdminEventTime(cursor), cursor.ID)
	if err != nil {
		return nil, "", err
	}
	if !more {
		return events, "", nil
	}
	events = events[:pageSize]
	last := events[len(events)-1]
	payload, err := json.Marshal(adminEventCursor{OccurredAt: last.OccurredAt, ID: last.ID})
	if err != nil {
		return nil, "", err
	}
	return events, base64.RawURLEncoding.EncodeToString(payload), nil
}

func optionalAdminEventTime(cursor adminEventCursor) *time.Time {
	if cursor.ID == "" {
		return nil
	}
	return &cursor.OccurredAt
}
