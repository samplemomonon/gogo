// Package authconsumer is the shared remote-client kit for surfaces that consume
// the auth.v1 contract: the login page and the storefront account page.
//
// The auth contract is the one dependency two top-level pages share, and each
// page originally carried its own copy of the same generated client, breaker
// and status-to-error switch. That produced two vocabularies for one contract —
// so the same "session not found" answer meant different things to the two
// pages that could both receive it — and a second place for the tenant stamp and
// the breaker to be forgotten. The shared kit is where the contract is described
// once: the wire-to-core mapping, the vocabulary, and the call mechanics (which
// live in shared/grpcconsumer).
//
// What stays with each consumer: its own read model and its own port. The login
// page maps a session into its own signed-in state; the account page maps an
// employee into its own Account. Neither imports the auth service's wire
// contracts, and neither imports the other.
package authconsumer

import (
	"context"
	"errors"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	authv1 "gogo/contracts/auth/v1"
	"gogo/internal/shared/breaker"
	"gogo/internal/shared/grpcconsumer"
	"gogo/internal/shared/tenant"
)

// Session is the shared, neutral session representation. Consumers receive this
// instead of the wire Session, so the contract's timestamps and revocation
// fields are translated once rather than in every page that holds a cookie.
type Session struct {
	ID         string
	TenantID   string
	EmployeeID string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	RevokedAt  time.Time
}

// Employee is the shared, neutral employee representation.
type Employee struct {
	ID    string
	Name  string
	Email string
	Role  string
}

type AuthorizationRole struct {
	ID, Name, Description string
	PermissionKeys        []string
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

// Credentials is a login attempt.
type Credentials struct {
	TenantID string
	Email    string
	PIN      string
}

// Outcome is what a successful authentication returns: who signed in, and the
// session that now represents that sign-in.
type Outcome struct {
	Session  Session
	Employee Employee
}

// Principal is the session-derived identity supplied to module PEPs.
type Principal struct {
	EmployeeID string
	Name       string
}

// vocabulary is the auth contract's error vocabulary. It names no Conflict: the
// contract has no write that can duplicate, and inventing a sentinel for an
// answer it never produces would be a branch callers can never take.
var vocabulary = grpcconsumer.Vocabulary{
	NotFound:        ErrNotFound,
	Invalid:         ErrInvalid,
	Precondition:    ErrPrecondition,
	Unauthenticated: ErrUnauthenticated,
	Forbidden:       ErrForbidden,
	Unavailable:     ErrAuthUnavailable,
	Conflict:        ErrPolicyConflict,
}

// Client wraps the generated auth.v1 client and the shared call convention.
type Client struct {
	auth   authv1.AuthServiceClient
	caller *grpcconsumer.Caller
}

// New returns a client over conn, guarded by b.
func New(conn grpc.ClientConnInterface, b *breaker.Breaker) *Client {
	return &Client{
		auth:   authv1.NewAuthServiceClient(conn),
		caller: grpcconsumer.New(b, vocabulary),
	}
}

// Breaker returns the circuit guarding the auth service, for the gateway's
// metrics and readiness probe.
func (c *Client) Breaker() *breaker.Breaker { return c.caller.Breaker() }

// Login authenticates and returns the new session together with the employee it
// belongs to. A blank creds.TenantID keeps the tenant already resolved on ctx.
func (c *Client) Login(ctx context.Context, creds Credentials) (Outcome, error) {
	ctx = grpcconsumer.WithTenant(ctx, creds.TenantID)
	// The tenant travels on both channels on purpose. The service reads it from
	// the context, but the request carries a tenant_id field too, and leaving
	// that one empty would make the same call correct today and wrong the day the
	// contract starts honouring it.
	resolved := tenant.FromContextOrDefault(ctx).String()
	resp, err := grpcconsumer.Do(c.caller, ctx, "log in", func(ctx context.Context) (*authv1.LoginResponse, error) {
		return c.auth.Login(ctx, &authv1.LoginRequest{
			TenantId: resolved,
			Email:    creds.Email,
			Pin:      creds.PIN,
		})
	})
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{Session: toSession(resp.GetSession()), Employee: toEmployee(resp.GetEmployee())}, nil
}

// Logout revokes a session. A session that is already gone is not an error the
// caller can act on, so it surfaces as the contract's not-found answer rather
// than as a failure.
func (c *Client) Logout(ctx context.Context, sessionID string) error {
	return grpcconsumer.DoUnit(c.caller, ctx, "log out", func(ctx context.Context) error {
		_, err := c.auth.Logout(ctx, &authv1.LogoutRequest{SessionId: sessionID})
		return err
	})
}

// Lookup resolves a session to the employee holding it.
//
// A session that is absent, expired or revoked is deliberately NOT an error: it
// yields a zero Outcome and a nil error, because every caller of this method is
// answering "who is signed in?" and the honest answer to that is "nobody". An
// outage still returns ErrAuthUnavailable, so a caller can tell a signed-out
// visitor from an unreachable auth service — the difference between rendering a
// signed-out page and pretending the visitor is anonymous because the
// dependency is down.
func (c *Client) Lookup(ctx context.Context, sessionID string) (Outcome, error) {
	if sessionID == "" {
		return Outcome{}, nil
	}
	resp, err := grpcconsumer.Do(c.caller, ctx, "validate session", func(ctx context.Context) (*authv1.ValidateSessionResponse, error) {
		return c.auth.ValidateSession(ctx, &authv1.ValidateSessionRequest{SessionId: sessionID})
	})
	if err != nil {
		if IsSignedOut(err) {
			return Outcome{}, nil
		}
		return Outcome{}, err
	}
	return Outcome{Session: toSession(resp.GetSession()), Employee: toEmployee(resp.GetEmployee())}, nil
}

// CheckPermission asks the auth PDP to evaluate a permission against the
// principal resolved from the supplied live session.
func (c *Client) CheckPermission(ctx context.Context, sessionID, permissionKey string) (bool, error) {
	resp, err := grpcconsumer.Do(c.caller, ctx, "check permission", func(ctx context.Context) (*authv1.CheckPermissionResponse, error) {
		return c.auth.CheckPermission(ctx, &authv1.CheckPermissionRequest{
			SessionId: sessionID, PermissionKey: permissionKey,
		})
	})
	if err != nil {
		return false, err
	}
	return resp.GetAllowed(), nil
}

// Authorize resolves a live session and evaluates a permission. A denied or
// absent session returns allowed=false; infrastructure failures remain errors.
func (c *Client) Authorize(ctx context.Context, sessionID, permissionKey string) (Principal, bool, error) {
	if sessionID == "" {
		return Principal{}, false, nil
	}
	outcome, err := c.Lookup(ctx, sessionID)
	if err != nil {
		return Principal{}, false, err
	}
	if outcome.Employee.ID == "" || outcome.Session.ID == "" {
		return Principal{}, false, nil
	}
	allowed, err := c.CheckPermission(ctx, sessionID, permissionKey)
	principal := Principal{EmployeeID: outcome.Employee.ID, Name: outcome.Employee.Name}
	if err != nil {
		return Principal{}, false, err
	}
	return principal, allowed, nil
}

// ListEmployees returns a bounded tenant employee roster for authorized access
// review. The tenant is derived by auth from sessionID.
func (c *Client) ListEmployees(ctx context.Context, sessionID string, limit int) ([]Employee, error) {
	resp, err := grpcconsumer.Do(c.caller, ctx, "list employees", func(ctx context.Context) (*authv1.ListEmployeesResponse, error) {
		return c.auth.ListEmployees(ctx, &authv1.ListEmployeesRequest{SessionId: sessionID, Limit: int32(limit)})
	})
	if err != nil {
		return nil, err
	}
	out := make([]Employee, 0, len(resp.GetEmployees()))
	for _, employee := range resp.GetEmployees() {
		out = append(out, toEmployee(employee))
	}
	return out, nil
}

// CreateRole creates a tenant-local role through the auth PAP.
func (c *Client) CreateRole(ctx context.Context, sessionID, name, description, changeReference string) (string, error) {
	resp, err := grpcconsumer.Do(c.caller, ctx, "create authorization role", func(ctx context.Context) (*authv1.CreateRoleResponse, error) {
		return c.auth.CreateRole(ctx, &authv1.CreateRoleRequest{SessionId: sessionID, Name: name, Description: description, ChangeReference: changeReference})
	})
	if err != nil {
		return "", err
	}
	return resp.GetRoleId(), nil
}

func (c *Client) GrantPermission(ctx context.Context, sessionID, roleID, permissionKey string, expiresAt *time.Time, reason, changeReference string) (string, error) {
	var expires *timestamppb.Timestamp
	if expiresAt != nil {
		expires = timestamppb.New(*expiresAt)
	}
	resp, err := grpcconsumer.Do(c.caller, ctx, "grant authorization permission", func(ctx context.Context) (*authv1.GrantPermissionResponse, error) {
		return c.auth.GrantPermission(ctx, &authv1.GrantPermissionRequest{SessionId: sessionID, RoleId: roleID, PermissionKey: permissionKey, ExpiresAt: expires, Reason: reason, ChangeReference: changeReference})
	})
	if err != nil {
		return "", err
	}
	return resp.GetGrantId(), nil
}

func (c *Client) RevokePermission(ctx context.Context, sessionID, grantID, reason, changeReference string) error {
	return grpcconsumer.DoUnit(c.caller, ctx, "revoke authorization permission", func(ctx context.Context) error {
		_, err := c.auth.RevokePermission(ctx, &authv1.RevokePermissionRequest{SessionId: sessionID, GrantId: grantID, Reason: reason, ChangeReference: changeReference})
		return err
	})
}

func (c *Client) AssignRole(ctx context.Context, sessionID, employeeID, roleID string, validFrom, expiresAt *time.Time, reason, changeReference string) (string, error) {
	var from, expires *timestamppb.Timestamp
	if validFrom != nil {
		from = timestamppb.New(*validFrom)
	}
	if expiresAt != nil {
		expires = timestamppb.New(*expiresAt)
	}
	resp, err := grpcconsumer.Do(c.caller, ctx, "assign authorization role", func(ctx context.Context) (*authv1.AssignRoleResponse, error) {
		return c.auth.AssignRole(ctx, &authv1.AssignRoleRequest{SessionId: sessionID, EmployeeId: employeeID, RoleId: roleID, ValidFrom: from, ExpiresAt: expires, Reason: reason, ChangeReference: changeReference})
	})
	if err != nil {
		return "", err
	}
	return resp.GetAssignmentId(), nil
}

func (c *Client) RevokeRoleAssignment(ctx context.Context, sessionID, assignmentID, reason, changeReference string) error {
	return grpcconsumer.DoUnit(c.caller, ctx, "revoke authorization role assignment", func(ctx context.Context) error {
		_, err := c.auth.RevokeRoleAssignment(ctx, &authv1.RevokeRoleAssignmentRequest{SessionId: sessionID, AssignmentId: assignmentID, Reason: reason, ChangeReference: changeReference})
		return err
	})
}

func (c *Client) ListAuthorizationRoles(ctx context.Context, sessionID string) ([]AuthorizationRole, error) {
	resp, err := grpcconsumer.Do(c.caller, ctx, "list authorization roles", func(ctx context.Context) (*authv1.ListAuthorizationRolesResponse, error) {
		return c.auth.ListAuthorizationRoles(ctx, &authv1.ListAuthorizationRolesRequest{SessionId: sessionID})
	})
	if err != nil {
		return nil, err
	}
	roles := make([]AuthorizationRole, 0, len(resp.GetRoles()))
	for _, role := range resp.GetRoles() {
		roles = append(roles, AuthorizationRole{ID: role.GetId(), Name: role.GetName(), Description: role.GetDescription(), PermissionKeys: role.GetActivePermissionKeys()})
	}
	return roles, nil
}

func (c *Client) ListRoleAssignments(ctx context.Context, sessionID, employeeID string, includeHistory bool) ([]RoleAssignment, error) {
	resp, err := grpcconsumer.Do(c.caller, ctx, "list role assignments", func(ctx context.Context) (*authv1.ListRoleAssignmentsResponse, error) {
		return c.auth.ListRoleAssignments(ctx, &authv1.ListRoleAssignmentsRequest{SessionId: sessionID, EmployeeId: employeeID, IncludeHistory: includeHistory})
	})
	if err != nil {
		return nil, err
	}
	assignments := make([]RoleAssignment, 0, len(resp.GetAssignments()))
	for _, item := range resp.GetAssignments() {
		assignment := RoleAssignment{ID: item.GetId(), EmployeeID: item.GetEmployeeId(), RoleID: item.GetRoleId(), Reason: item.GetReason(), AssignedBy: item.GetAssignedBy(), Source: item.GetSource(), AssignedAt: asTime(item.GetAssignedAt()), ValidFrom: asTime(item.GetValidFrom())}
		if item.GetExpiresAt() != nil {
			value := item.GetExpiresAt().AsTime()
			assignment.ExpiresAt = &value
		}
		if item.GetRevokedAt() != nil {
			value := item.GetRevokedAt().AsTime()
			assignment.RevokedAt = &value
		}
		assignments = append(assignments, assignment)
	}
	return assignments, nil
}

// ListTenantRoleAssignments retrieves one bounded page of tenant-wide assignment history.
func (c *Client) ListTenantRoleAssignments(ctx context.Context, sessionID string, pageSize uint32, pageToken string) ([]RoleAssignment, string, error) {
	resp, err := grpcconsumer.Do(c.caller, ctx, "review tenant role assignments", func(ctx context.Context) (*authv1.ListTenantRoleAssignmentsResponse, error) {
		return c.auth.ListTenantRoleAssignments(ctx, &authv1.ListTenantRoleAssignmentsRequest{SessionId: sessionID, PageSize: pageSize, PageToken: pageToken})
	})
	if err != nil {
		return nil, "", err
	}
	assignments := make([]RoleAssignment, 0, len(resp.GetAssignments()))
	for _, item := range resp.GetAssignments() {
		assignment := RoleAssignment{ID: item.GetId(), EmployeeID: item.GetEmployeeId(), RoleID: item.GetRoleId(), Reason: item.GetReason(), AssignedBy: item.GetAssignedBy(), Source: item.GetSource(), AssignedAt: asTime(item.GetAssignedAt()), ValidFrom: asTime(item.GetValidFrom())}
		if item.GetExpiresAt() != nil {
			value := item.GetExpiresAt().AsTime()
			assignment.ExpiresAt = &value
		}
		if item.GetRevokedAt() != nil {
			value := item.GetRevokedAt().AsTime()
			assignment.RevokedAt = &value
		}
		assignments = append(assignments, assignment)
	}
	return assignments, resp.GetNextPageToken(), nil
}

// ListAuthorizationAdminEvents retrieves one bounded page of tenant policy administration audit history.
func (c *Client) ListAuthorizationAdminEvents(ctx context.Context, sessionID string, pageSize uint32, pageToken string) ([]AuthorizationAdminEvent, string, error) {
	resp, err := grpcconsumer.Do(c.caller, ctx, "review authorization administration events", func(ctx context.Context) (*authv1.ListAuthorizationAdminEventsResponse, error) {
		return c.auth.ListAuthorizationAdminEvents(ctx, &authv1.ListAuthorizationAdminEventsRequest{SessionId: sessionID, PageSize: pageSize, PageToken: pageToken})
	})
	if err != nil {
		return nil, "", err
	}
	events := make([]AuthorizationAdminEvent, 0, len(resp.GetEvents()))
	for _, item := range resp.GetEvents() {
		events = append(events, AuthorizationAdminEvent{ID: item.GetId(), Action: item.GetAction(), SubjectID: item.GetSubjectId(), RoleID: item.GetRoleId(), ActorEmployeeID: item.GetActorEmployeeId(), ActorDatabaseUser: item.GetActorDatabaseUser(), ChangeReference: item.GetChangeReference(), Reason: item.GetReason(), OccurredAt: asTime(item.GetOccurredAt())})
	}
	return events, resp.GetNextPageToken(), nil
}

// isSignedOut reports whether err is the contract's answer that there is no such
// usable session, rather than a failure to reach the service.
// IsSignedOut reports whether the auth contract says the supplied session is
// absent or no longer usable.
func IsSignedOut(err error) bool {
	return errors.Is(err, ErrNotFound) ||
		errors.Is(err, ErrInvalid) ||
		errors.Is(err, ErrPrecondition) ||
		errors.Is(err, ErrUnauthenticated)
}

func toSession(s *authv1.Session) Session {
	if s == nil {
		return Session{}
	}
	return Session{
		ID:         s.GetId(),
		TenantID:   s.GetTenantId(),
		EmployeeID: s.GetEmployeeId(),
		CreatedAt:  asTime(s.GetCreatedAt()),
		ExpiresAt:  asTime(s.GetExpiresAt()),
		RevokedAt:  asTime(s.GetRevokedAt()),
	}
}

func toEmployee(e *authv1.Employee) Employee {
	if e == nil {
		return Employee{}
	}
	return Employee{
		ID:    e.GetId(),
		Name:  e.GetName(),
		Email: e.GetEmail(),
		Role:  e.GetRole(),
	}
}

func asTime(ts *timestamppb.Timestamp) time.Time {
	if ts == nil {
		return time.Time{}
	}
	return ts.AsTime()
}
