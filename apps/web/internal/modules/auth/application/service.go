// Package application holds the auth facade: it composes the employee and session stores.
package application

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"gogo/internal/modules/auth/application/ports"
	"gogo/internal/modules/auth/domain"
)

var _ ports.AuthService = (*Service)(nil)

// invitationTTL is how long a merchant invitation stays valid before a
// karyawan must re-request one.
const invitationTTL = 7 * 24 * time.Hour

// grantableMerchantPermissions is the code-owned allowlist of capabilities a
// merchant owner may delegate to a karyawan. It is deliberately a subset of the
// tenant permission catalog: a karyawan is scoped to one merchant, not to the
// whole tenant, and is never granted administrative authority.
var grantableMerchantPermissions = map[string]bool{
	"seller.catalog.manage":   true,
	"seller.inventory.manage": true,
	"seller.category.manage":  true,
	"seller.sales.checkout":   true,
	"seller.sales.void":       true,
}

// Service implements ports.AuthService.
type Service struct {
	employees     ports.EmployeeStore
	sessions      ports.SessionStore
	authorization ports.AuthorizationStore
	merchants     ports.MerchantStore
	pinCost       int
	ttl           time.Duration
}

// NewService creates a new auth service. merchants is the merchant relationship
// store; pass nil when merchant use cases are not wired (the tenant-scoped
// RBAC surface still works without it).
func NewService(employees ports.EmployeeStore, sessions ports.SessionStore, pinCost int, ttl time.Duration, merchants ports.MerchantStore, authorization ...ports.AuthorizationStore) *Service {
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
	return &Service{employees: employees, sessions: sessions, authorization: authz, merchants: merchants, pinCost: pinCost, ttl: ttl}
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

	session, err := domain.NewSession(tenant, uuid.NewString(), employee.ID, s.ttl)
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

// ---- merchant relationship use cases --------------------------------------

// RegisterAccount creates a pembeli account: a customer who can browse and buy
// but owns no merchant. The account is the default reserved role "pembeli"; it
// becomes a penjual when it opens a merchant and a karyawan when an owner
// accepts it into theirs.
func (s *Service) RegisterAccount(ctx context.Context, tenant domain.Tenant, name, email, pin string) (*domain.Employee, error) {
	return s.RegisterEmployee(ctx, tenant, uuid.NewString(), name, email, pin, domain.RoleViewer)
}

// merchantAdministrator resolves the session to an employee and verifies they
// own the merchant. It is the owner-only guard for merchant administration.
func (s *Service) merchantAdministrator(ctx context.Context, sessionID, merchantID string) (*domain.Session, *domain.Employee, *domain.Membership, error) {
	if s.merchants == nil {
		return nil, nil, nil, domain.ErrMerchantPermissionDenied
	}
	session, employee, err := s.ValidateSession(ctx, sessionID)
	if err != nil {
		return nil, nil, nil, err
	}
	membership, err := s.merchants.GetMembership(ctx, domain.Tenant(session.TenantID), merchantID, employee.ID)
	if err != nil {
		// A stranger or non-member must not learn their membership status: the
		// only answer is that the operation is forbidden.
		if errors.Is(err, domain.ErrMembershipNotFound) {
			return nil, nil, nil, domain.ErrMerchantPermissionDenied
		}
		return nil, nil, nil, err
	}
	if !membership.IsOwner() {
		return nil, nil, nil, domain.ErrMerchantPermissionDenied
	}
	return session, employee, membership, nil
}

// OpenMerchant creates a merchant owned by the session employee, making them a
// penjual. An owner membership is created in the same operation.
func (s *Service) OpenMerchant(ctx context.Context, sessionID, name string) (*domain.Merchant, error) {
	if s.merchants == nil {
		return nil, domain.ErrMerchantPermissionDenied
	}
	session, employee, err := s.ValidateSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	tenant := domain.Tenant(session.TenantID)
	merchant, err := domain.NewMerchant(tenant, uuid.NewString(), employee.ID, name)
	if err != nil {
		return nil, err
	}
	membership, err := domain.NewMembership(tenant, merchant.ID, employee.ID, employee.ID, domain.MerchantRoleOwner)
	if err != nil {
		return nil, err
	}
	if err := s.merchants.CreateMerchant(ctx, merchant); err != nil {
		return nil, err
	}
	if err := s.merchants.CreateMembership(ctx, membership); err != nil {
		return nil, err
	}
	return merchant, nil
}

// ListMyMerchants returns the merchants the session employee is a member of
// (owned as a penjual, or joined as a karyawan).
func (s *Service) ListMyMerchants(ctx context.Context, sessionID string) ([]*domain.Merchant, error) {
	if s.merchants == nil {
		return nil, domain.ErrMerchantPermissionDenied
	}
	session, _, err := s.ValidateSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	return s.merchants.ListMerchantsForEmployee(ctx, domain.Tenant(session.TenantID), session.EmployeeID)
}

// GetMerchant returns a merchant the session employee is a member of.
func (s *Service) GetMerchant(ctx context.Context, sessionID, merchantID string) (*domain.Merchant, error) {
	if s.merchants == nil {
		return nil, domain.ErrMerchantPermissionDenied
	}
	session, _, err := s.ValidateSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	tenant := domain.Tenant(session.TenantID)
	if _, err := s.merchants.GetMembership(ctx, tenant, merchantID, session.EmployeeID); err != nil {
		return nil, err
	}
	return s.merchants.GetMerchant(ctx, tenant, merchantID)
}

// ListMembers returns a merchant's roster. Only the owner may read it.
func (s *Service) ListMembers(ctx context.Context, sessionID, merchantID string) ([]*domain.Membership, error) {
	session, _, _, err := s.merchantAdministrator(ctx, sessionID, merchantID)
	if err != nil {
		return nil, err
	}
	return s.merchants.ListMembers(ctx, domain.Tenant(session.TenantID), merchantID)
}

// InviteMember creates an invitation for a karyawan. Only the merchant owner
// (penjual) may invite, and an owner cannot invite a second owner.
func (s *Service) InviteMember(ctx context.Context, sessionID, merchantID, email string, role domain.MerchantRole) (*domain.Invitation, error) {
	session, _, _, err := s.merchantAdministrator(ctx, sessionID, merchantID)
	if err != nil {
		return nil, err
	}
	if role == domain.MerchantRoleOwner {
		return nil, domain.ErrInvalidMerchant
	}
	tenant := domain.Tenant(session.TenantID)
	invitation, err := domain.NewInvitation(tenant, uuid.NewString(), merchantID, email, role, uuid.NewString(), invitationTTL, session.EmployeeID)
	if err != nil {
		return nil, err
	}
	if err := s.merchants.CreateInvitation(ctx, invitation); err != nil {
		return nil, err
	}
	return invitation, nil
}

// AcceptInvitation accepts a pending invitation on behalf of the session
// employee, creating a karyawan membership. The accepting employee's email
// must match the invited email, so a token cannot be redeemed by a stranger.
func (s *Service) AcceptInvitation(ctx context.Context, sessionID, token string) (*domain.Membership, error) {
	if s.merchants == nil {
		return nil, domain.ErrMerchantPermissionDenied
	}
	session, employee, err := s.ValidateSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	tenant := domain.Tenant(session.TenantID)
	invitation, err := s.merchants.GetInvitationByToken(ctx, tenant, strings.TrimSpace(token))
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(strings.TrimSpace(invitation.Email), strings.TrimSpace(employee.Email)) {
		return nil, domain.ErrInvalidMerchant
	}
	if err := invitation.Accept(); err != nil {
		return nil, err
	}
	if err := s.merchants.UpdateInvitation(ctx, invitation); err != nil {
		return nil, err
	}
	membership, err := domain.NewMembership(tenant, invitation.MerchantID, employee.ID, invitation.InvitedBy, invitation.Role)
	if err != nil {
		return nil, err
	}
	if err := s.merchants.CreateMembership(ctx, membership); err != nil {
		return nil, err
	}
	return membership, nil
}

// RevokeInvitation withdraws a pending invitation. Only the merchant owner may.
func (s *Service) RevokeInvitation(ctx context.Context, sessionID, invitationID string) error {
	if s.merchants == nil {
		return domain.ErrMerchantPermissionDenied
	}
	session, _, err := s.ValidateSession(ctx, sessionID)
	if err != nil {
		return err
	}
	invitation, err := s.merchants.GetInvitation(ctx, domain.Tenant(session.TenantID), invitationID)
	if err != nil {
		return err
	}
	if _, _, _, err := s.merchantAdministrator(ctx, sessionID, invitation.MerchantID); err != nil {
		return err
	}
	if err := invitation.Revoke(); err != nil {
		return err
	}
	return s.merchants.UpdateInvitation(ctx, invitation)
}

// GrantMerchantPermission delegates a supported capability to a karyawan
// member of a merchant. Only the merchant owner may grant.
func (s *Service) GrantMerchantPermission(ctx context.Context, sessionID, merchantID, employeeID, permissionKey string) error {
	session, _, _, err := s.merchantAdministrator(ctx, sessionID, merchantID)
	if err != nil {
		return err
	}
	if !grantableMerchantPermissions[permissionKey] {
		return domain.ErrInvalidMerchant
	}
	tenant := domain.Tenant(session.TenantID)
	if _, err := s.merchants.GetMembership(ctx, tenant, merchantID, employeeID); err != nil {
		return err
	}
	permission, err := domain.NewMerchantPermission(tenant, merchantID, employeeID, permissionKey, session.EmployeeID)
	if err != nil {
		return err
	}
	return s.merchants.GrantMerchantPermission(ctx, permission)
}

// RevokeMerchantPermission withdraws a karyawan's merchant-scoped capability.
func (s *Service) RevokeMerchantPermission(ctx context.Context, sessionID, merchantID, employeeID, permissionKey string) error {
	session, _, _, err := s.merchantAdministrator(ctx, sessionID, merchantID)
	if err != nil {
		return err
	}
	return s.merchants.RevokeMerchantPermission(ctx, domain.Tenant(session.TenantID), merchantID, employeeID, permissionKey)
}

// ListMerchantPermissions returns a member's granted merchant-scoped
// capabilities. Only the merchant owner may read another member's grants.
func (s *Service) ListMerchantPermissions(ctx context.Context, sessionID, merchantID, employeeID string) ([]string, error) {
	session, _, _, err := s.merchantAdministrator(ctx, sessionID, merchantID)
	if err != nil {
		return nil, err
	}
	return s.merchants.ListMerchantPermissions(ctx, domain.Tenant(session.TenantID), merchantID, employeeID)
}

// CanManageProduct is the merchant-scoped PDP: may the employee holding session
// modify products in merchantID? The merchant owner (penjual) always may; a
// karyawan may only with an explicit seller.catalog.manage grant from the owner.
// An absent, expired or unauthorized session is a deny, never an error.
func (s *Service) CanManageProduct(ctx context.Context, sessionID, merchantID string) (bool, error) {
	if s.merchants == nil {
		return false, nil
	}
	session, employee, err := s.ValidateSession(ctx, sessionID)
	if err != nil {
		return false, nil
	}
	tenant := domain.Tenant(session.TenantID)
	membership, err := s.merchants.GetMembership(ctx, tenant, merchantID, employee.ID)
	if err != nil {
		if errors.Is(err, domain.ErrMembershipNotFound) {
			return false, nil
		}
		return false, err
	}
	if membership.IsOwner() {
		return true, nil
	}
	return s.merchants.HasMerchantPermission(ctx, tenant, merchantID, employee.ID, "seller.catalog.manage")
}
