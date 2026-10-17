// Package grpcservice (in-package) covers the auth.v1 adapter: tenant
// resolution, the domain-error → gRPC-status mapping, and the proto mapping
// of employees and sessions. In-package because the adapter's whole surface —
// the unexported service struct and toStatus/toProto* helpers — is what the
// tests drive; a fake ports.AuthService isolates the adapter from the
// application layer.
package grpcservice

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	authv1 "gogo/contracts/auth/v1"
	"gogo/internal/modules/auth/application/ports"
	"gogo/internal/modules/auth/domain"
	"gogo/internal/shared/tenant"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newServiceUnderTest builds the adapter over a fake, the same way Register
// does — minus the registrar.
func newServiceUnderTest(fake ports.AuthService) *service {
	return &service{svc: fake, logger: testLogger()}
}

// fakeService is a ports.AuthService test double. Each field overrides one
// method; an unset method fails loudly if called, so a test only stubs the
// calls it exercises.
type fakeService struct {
	ports.AuthService

	listEmployees   func(ctx context.Context, tenant domain.Tenant, limit int) ([]*domain.Employee, error)
	login           func(ctx context.Context, tenant domain.Tenant, email, pin string) (*domain.Session, *domain.Employee, error)
	extendSession   func(ctx context.Context, sessionID string) (*domain.Session, error)
	validateSession func(ctx context.Context, sessionID string) (*domain.Session, *domain.Employee, error)
	checkPermission func(ctx context.Context, sessionID, permissionKey string) (bool, error)
}

func unexpectedCall(method string) error {
	return fmt.Errorf("fakeService: unexpected %s call", method)
}

func (f *fakeService) ListEmployees(ctx context.Context, tenant domain.Tenant, limit int) ([]*domain.Employee, error) {
	if f.listEmployees == nil {
		return nil, unexpectedCall("ListEmployees")
	}
	return f.listEmployees(ctx, tenant, limit)
}

func (f *fakeService) Login(ctx context.Context, tenant domain.Tenant, email, pin string) (*domain.Session, *domain.Employee, error) {
	if f.login == nil {
		return nil, nil, unexpectedCall("Login")
	}
	return f.login(ctx, tenant, email, pin)
}

func (f *fakeService) ExtendSession(ctx context.Context, sessionID string) (*domain.Session, error) {
	if f.extendSession == nil {
		return nil, unexpectedCall("ExtendSession")
	}
	return f.extendSession(ctx, sessionID)
}

func (f *fakeService) ValidateSession(ctx context.Context, sessionID string) (*domain.Session, *domain.Employee, error) {
	if f.validateSession == nil {
		return nil, nil, unexpectedCall("ValidateSession")
	}
	return f.validateSession(ctx, sessionID)
}

func (f *fakeService) CheckPermission(ctx context.Context, sessionID, permissionKey string) (bool, error) {
	if f.checkPermission == nil {
		return false, unexpectedCall("CheckPermission")
	}
	return f.checkPermission(ctx, sessionID, permissionKey)
}

// failingService answers every method under test with one error, so the
// classification table can drive each domain sentinel through the adapter.
type failingService struct {
	ports.AuthService
	err error
}

func (f failingService) Login(context.Context, domain.Tenant, string, string) (*domain.Session, *domain.Employee, error) {
	return nil, nil, f.err
}

func (f failingService) Logout(context.Context, string) error { return f.err }

func (f failingService) ExtendSession(context.Context, string) (*domain.Session, error) {
	return nil, f.err
}

func (f failingService) ValidateSession(context.Context, string) (*domain.Session, *domain.Employee, error) {
	return nil, nil, f.err
}

// TestTenantResolution pins where the tenant comes from: the adapter resolves
// it from the context (grpcx puts it there from call metadata), never from a
// request field, and a context with no tenant falls back to tenant.Default.
func TestTenantResolution(t *testing.T) {
	t.Parallel()

	captureTenant := func(seen *domain.Tenant) *fakeService {
		return &fakeService{
			login: func(_ context.Context, tenant domain.Tenant, _, _ string) (*domain.Session, *domain.Employee, error) {
				*seen = tenant
				return &domain.Session{}, &domain.Employee{}, nil
			},
		}
	}

	t.Run("the context tenant reaches the service", func(t *testing.T) {
		t.Parallel()

		var seen domain.Tenant
		impl := newServiceUnderTest(captureTenant(&seen))
		ctx := tenant.WithID(context.Background(), "acme")

		if _, err := impl.Login(ctx, &authv1.LoginRequest{Email: "budi@toko.com", Pin: "123456"}); err != nil {
			t.Fatalf("Login: %v", err)
		}
		if seen != "acme" {
			t.Errorf("service saw tenant %q, want acme", seen)
		}
	})

	t.Run("a context without a tenant falls back to default", func(t *testing.T) {
		t.Parallel()

		var seen domain.Tenant
		impl := newServiceUnderTest(captureTenant(&seen))

		if _, err := impl.Login(context.Background(), &authv1.LoginRequest{}); err != nil {
			t.Fatalf("Login: %v", err)
		}
		if seen != domain.Tenant(tenant.Default) {
			t.Errorf("service saw tenant %q, want the default tenant", seen)
		}
	})

	t.Run("the request's tenant_id cannot override the context", func(t *testing.T) {
		t.Parallel()

		var seen domain.Tenant
		impl := newServiceUnderTest(captureTenant(&seen))
		ctx := tenant.WithID(context.Background(), "acme")

		// The contract carries tenant_id on every request, but the adapter
		// ignores it: the tenant is a property of the caller's context
		// (grpcx metadata), so a message field must not be able to
		// contradict it.
		if _, err := impl.Login(ctx, &authv1.LoginRequest{TenantId: "mallory"}); err != nil {
			t.Fatalf("Login: %v", err)
		}
		if seen != "acme" {
			t.Errorf("service saw tenant %q, want the context tenant acme", seen)
		}
	})
}

// TestErrorClassification pins the toStatus switch: which domain error reaches
// the client with which gRPC code and message. The specific sentinels are
// classified before the buckets they wrap, so a malformed PIN and an expired
// session arrive as FailedPrecondition with their own messages, and a
// credential mismatch as Unauthenticated. These tests pin what the adapter
// actually answers.
func TestErrorClassification(t *testing.T) {
	t.Parallel()

	viaLogin := func(ctx context.Context, svc *service) error {
		_, err := svc.Login(ctx, &authv1.LoginRequest{})
		return err
	}
	viaLogout := func(ctx context.Context, svc *service) error {
		_, err := svc.Logout(ctx, &authv1.LogoutRequest{})
		return err
	}
	viaValidateSession := func(ctx context.Context, svc *service) error {
		_, err := svc.ValidateSession(ctx, &authv1.ValidateSessionRequest{})
		return err
	}
	viaExtendSession := func(ctx context.Context, svc *service) error {
		_, err := svc.ExtendSession(ctx, &authv1.ExtendSessionRequest{})
		return err
	}

	tests := []struct {
		name        string
		err         error
		call        func(context.Context, *service) error
		wantCode    codes.Code
		wantMessage string
	}{
		{
			name: "unknown email is not found",
			err:  domain.ErrEmployeeNotFound, call: viaLogin,
			wantCode: codes.NotFound, wantMessage: "employee not found",
		},
		{
			name: "unknown session is not found",
			err:  domain.ErrSessionNotFound, call: viaLogout,
			wantCode: codes.NotFound, wantMessage: "session not found",
		},
		{
			name: "a wrapped not-found keeps its code",
			err:  fmt.Errorf("store: %w", domain.ErrEmployeeNotFound), call: viaValidateSession,
			wantCode: codes.NotFound, wantMessage: "employee not found",
		},
		{
			name: "a malformed employee is invalid argument",
			err:  domain.ErrInvalidEmployee, call: viaLogin,
			wantCode: codes.InvalidArgument, wantMessage: "invalid employee",
		},
		{
			name: "a malformed session is invalid argument",
			err:  domain.ErrInvalidSession, call: viaLogin,
			wantCode: codes.InvalidArgument, wantMessage: "invalid session",
		},
		{
			name: "a mismatched credential is unauthenticated",
			err:  domain.ErrInvalidCredentials, call: viaLogin,
			wantCode: codes.Unauthenticated, wantMessage: "invalid credentials",
		},
		{
			name: "a malformed PIN keeps its own message",
			err:  domain.ErrInvalidPIN, call: viaLogin,
			wantCode: codes.FailedPrecondition, wantMessage: "PIN must be 6 digits",
		},
		{
			name: "an expired session keeps its own message",
			err:  domain.ErrExpired, call: viaExtendSession,
			wantCode: codes.FailedPrecondition, wantMessage: "session has expired",
		},
		{
			name: "an unknown failure is internal",
			err:  errors.New("database down"), call: viaLogin,
			wantCode: codes.Internal, wantMessage: "something went wrong",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			impl := newServiceUnderTest(failingService{err: tc.err})
			err := tc.call(context.Background(), impl)

			if got := status.Code(err); got != tc.wantCode {
				t.Errorf("code = %v, want %v (error %v)", got, tc.wantCode, err)
			}
			if got := status.Convert(err).Message(); got != tc.wantMessage {
				t.Errorf("message = %q, want %q", got, tc.wantMessage)
			}
		})
	}
}

// TestInternalErrorsAreLogged pins that an unclassified error keeps its raw
// form in the server log while the client only receives the stable message:
// the internal case must not leak the underlying failure over the wire.
func TestInternalErrorsAreLogged(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	impl := &service{
		svc:    failingService{err: errors.New("db exploded")},
		logger: slog.New(slog.NewJSONHandler(&logs, nil)),
	}

	_, err := impl.Login(context.Background(), &authv1.LoginRequest{})
	if got := status.Code(err); got != codes.Internal {
		t.Fatalf("code = %v, want Internal", got)
	}
	if got := status.Convert(err).Message(); strings.Contains(got, "db exploded") {
		t.Errorf("client message %q leaks the internal failure", got)
	}
	if !strings.Contains(logs.String(), "auth grpc internal error") {
		t.Errorf("log %q does not name the internal error", logs.String())
	}
	if !strings.Contains(logs.String(), "db exploded") {
		t.Errorf("log %q does not carry the underlying failure", logs.String())
	}
}

// TestProtoEmployeeMapping pins every field of the Employee message,
// including the timestamps and the inactive state (a zero-value bool must
// survive the mapping, not be dropped). Login is the RPC that still returns an
// Employee: the legacy read RPCs are closed.
func TestProtoEmployeeMapping(t *testing.T) {
	t.Parallel()

	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	updated := created.Add(90 * time.Minute)
	stored := &domain.Employee{
		TenantID:  "tenant-a",
		ID:        "emp-1",
		Name:      "Budi",
		Email:     "budi@toko.com",
		Role:      domain.RoleSeller,
		PINHash:   "hash",
		Active:    false,
		CreatedAt: created,
		UpdatedAt: updated,
	}

	impl := newServiceUnderTest(&fakeService{
		login: func(_ context.Context, _ domain.Tenant, _, _ string) (*domain.Session, *domain.Employee, error) {
			return &domain.Session{ID: "sess-1"}, stored, nil
		},
	})

	resp, err := impl.Login(context.Background(), &authv1.LoginRequest{Email: "budi@toko.com", Pin: "123456"})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	got := resp.GetEmployee()
	if got == nil {
		t.Fatal("Login returned no employee")
	}
	if got.GetId() != "emp-1" || got.GetTenantId() != "tenant-a" {
		t.Errorf("id/tenant = %q/%q, want emp-1/tenant-a", got.GetId(), got.GetTenantId())
	}
	if got.GetName() != "Budi" || got.GetEmail() != "budi@toko.com" {
		t.Errorf("name/email = %q/%q, want Budi/budi@toko.com", got.GetName(), got.GetEmail())
	}
	if got.GetRole() != string(domain.RoleSeller) {
		t.Errorf("role = %q, want %q", got.GetRole(), domain.RoleSeller)
	}
	if got.GetActive() {
		t.Error("active = true, want the stored false to survive the mapping")
	}
	if !got.GetCreatedAt().AsTime().Equal(created) {
		t.Errorf("created_at = %v, want %v", got.GetCreatedAt().AsTime(), created)
	}
	if !got.GetUpdatedAt().AsTime().Equal(updated) {
		t.Errorf("updated_at = %v, want %v", got.GetUpdatedAt().AsTime(), updated)
	}
}

// TestProtoSessionMapping pins the Session message, including both shapes of
// revoked_at: unset while the session is active, carried once it is revoked.
func TestProtoSessionMapping(t *testing.T) {
	t.Parallel()

	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	expires := created.Add(24 * time.Hour)
	revoked := created.Add(time.Hour)

	session := func(revokedAt *time.Time) *domain.Session {
		return &domain.Session{
			ID:         "sess-1",
			TenantID:   "tenant-a",
			EmployeeID: "emp-1",
			CreatedAt:  created,
			ExpiresAt:  expires,
			RevokedAt:  revokedAt,
		}
	}

	tests := []struct {
		name      string
		revokedAt *time.Time
	}{
		{name: "an active session leaves revoked_at unset"},
		{name: "a revoked session carries revoked_at", revokedAt: &revoked},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			impl := newServiceUnderTest(&fakeService{
				extendSession: func(_ context.Context, _ string) (*domain.Session, error) {
					return session(tc.revokedAt), nil
				},
			})

			resp, err := impl.ExtendSession(context.Background(), &authv1.ExtendSessionRequest{SessionId: "sess-1"})
			if err != nil {
				t.Fatalf("ExtendSession: %v", err)
			}

			got := resp.GetSession()
			if got == nil {
				t.Fatal("ExtendSession returned no session")
			}
			if got.GetId() != "sess-1" || got.GetTenantId() != "tenant-a" || got.GetEmployeeId() != "emp-1" {
				t.Errorf("session identity = %q/%q/%q, want sess-1/tenant-a/emp-1",
					got.GetId(), got.GetTenantId(), got.GetEmployeeId())
			}
			if !got.GetCreatedAt().AsTime().Equal(created) {
				t.Errorf("created_at = %v, want %v", got.GetCreatedAt().AsTime(), created)
			}
			if !got.GetExpiresAt().AsTime().Equal(expires) {
				t.Errorf("expires_at = %v, want %v", got.GetExpiresAt().AsTime(), expires)
			}
			if tc.revokedAt == nil {
				if got.GetRevokedAt() != nil {
					t.Errorf("revoked_at = %v, want unset", got.GetRevokedAt().AsTime())
				}
				return
			}
			if got.GetRevokedAt() == nil {
				t.Fatal("revoked_at is unset, want the stored time")
			}
			if !got.GetRevokedAt().AsTime().Equal(*tc.revokedAt) {
				t.Errorf("revoked_at = %v, want %v", got.GetRevokedAt().AsTime(), *tc.revokedAt)
			}
		})
	}
}

// TestNilAggregatesMapToNilProto pins the nil guards: a nil session or
// employee maps to an unset proto field rather than a panic, so a service
// implementation returning (nil, nil) cannot crash the server.
func TestNilAggregatesMapToNilProto(t *testing.T) {
	t.Parallel()

	impl := &service{svc: &fakeService{}, logger: testLogger()}

	if got := impl.toProtoEmployee(nil); got != nil {
		t.Errorf("toProtoEmployee(nil) = %v, want nil", got)
	}
	if got := impl.toProtoSession(nil); got != nil {
		t.Errorf("toProtoSession(nil) = %v, want nil", got)
	}
}

// TestListEmployeesMapsEach pins the list adapter: the tenant comes from the
// validated session (never from the caller's context), the permission check is
// asked for the employee-read key, every stored employee is converted in
// order, and the request limit is passed through as an int.
func TestListEmployeesMapsEach(t *testing.T) {
	t.Parallel()

	var (
		seenTenant domain.Tenant
		seenLimit  int
		seenPerm   string
	)
	impl := newServiceUnderTest(&fakeService{
		validateSession: func(_ context.Context, sessionID string) (*domain.Session, *domain.Employee, error) {
			if sessionID != "sess-1" {
				t.Errorf("service saw session %q, want sess-1", sessionID)
			}
			return &domain.Session{ID: "sess-1", TenantID: "tenant-a", EmployeeID: "emp-9"}, &domain.Employee{ID: "emp-9"}, nil
		},
		checkPermission: func(_ context.Context, _, permissionKey string) (bool, error) {
			seenPerm = permissionKey
			return true, nil
		},
		listEmployees: func(_ context.Context, tenant domain.Tenant, limit int) ([]*domain.Employee, error) {
			seenTenant, seenLimit = tenant, limit
			return []*domain.Employee{
				{ID: "emp-1", TenantID: "tenant-a", Name: "Budi", Email: "budi@toko.com", Role: domain.RoleSeller},
				{ID: "emp-2", TenantID: "tenant-a", Name: "Siti", Email: "siti@toko.com", Role: domain.RoleViewer},
			}, nil
		},
	})

	// The context names a different tenant: a message field or metadata
	// tenant must not be able to widen the session's scope.
	ctx := tenant.WithID(context.Background(), "mallory")
	resp, err := impl.ListEmployees(ctx, &authv1.ListEmployeesRequest{SessionId: "sess-1", Limit: 25})
	if err != nil {
		t.Fatalf("ListEmployees: %v", err)
	}
	if seenTenant != "tenant-a" {
		t.Errorf("service saw tenant %q, want the session tenant tenant-a", seenTenant)
	}
	if seenLimit != 25 {
		t.Errorf("service saw limit %d, want 25", seenLimit)
	}
	if seenPerm != "authz.employee.read" {
		t.Errorf("permission checked = %q, want authz.employee.read", seenPerm)
	}
	if len(resp.GetEmployees()) != 2 {
		t.Fatalf("listed %d employees, want 2", len(resp.GetEmployees()))
	}
	if resp.GetEmployees()[0].GetId() != "emp-1" || resp.GetEmployees()[1].GetId() != "emp-2" {
		t.Errorf("list order = %q, %q, want emp-1, emp-2",
			resp.GetEmployees()[0].GetId(), resp.GetEmployees()[1].GetId())
	}
}

// TestSessionAuthorizationGate pins authorizeSession's refusals: a session-
// authorized RPC must be refused before the service is asked for anything when
// the session is missing, unknown, or lacks the permission.
func TestSessionAuthorizationGate(t *testing.T) {
	t.Parallel()

	t.Run("a missing session is unauthenticated", func(t *testing.T) {
		t.Parallel()

		impl := newServiceUnderTest(&fakeService{})

		_, err := impl.ListEmployees(context.Background(), &authv1.ListEmployeesRequest{})
		if got := status.Code(err); got != codes.Unauthenticated {
			t.Errorf("code = %v, want Unauthenticated (error %v)", got, err)
		}
		if got := status.Convert(err).Message(); got != "session required" {
			t.Errorf("message = %q, want %q", got, "session required")
		}
	})

	t.Run("an unknown session is not found", func(t *testing.T) {
		t.Parallel()

		impl := newServiceUnderTest(&fakeService{
			validateSession: func(_ context.Context, _ string) (*domain.Session, *domain.Employee, error) {
				return nil, nil, domain.ErrSessionNotFound
			},
		})

		_, err := impl.ListEmployees(context.Background(), &authv1.ListEmployeesRequest{SessionId: "sess-9"})
		if got := status.Code(err); got != codes.NotFound {
			t.Errorf("code = %v, want NotFound (error %v)", got, err)
		}
		if got := status.Convert(err).Message(); got != "session not found" {
			t.Errorf("message = %q, want %q", got, "session not found")
		}
	})

	t.Run("a denied permission is forbidden", func(t *testing.T) {
		t.Parallel()

		listed := false
		impl := newServiceUnderTest(&fakeService{
			validateSession: func(_ context.Context, _ string) (*domain.Session, *domain.Employee, error) {
				return &domain.Session{ID: "sess-1", TenantID: "tenant-a", EmployeeID: "emp-9"}, &domain.Employee{ID: "emp-9"}, nil
			},
			checkPermission: func(_ context.Context, _, _ string) (bool, error) {
				return false, nil
			},
			listEmployees: func(_ context.Context, _ domain.Tenant, _ int) ([]*domain.Employee, error) {
				listed = true
				return nil, nil
			},
		})

		_, err := impl.ListEmployees(context.Background(), &authv1.ListEmployeesRequest{SessionId: "sess-1"})
		if got := status.Code(err); got != codes.PermissionDenied {
			t.Errorf("code = %v, want PermissionDenied (error %v)", got, err)
		}
		if got := status.Convert(err).Message(); got != "permission denied" {
			t.Errorf("message = %q, want %q", got, "permission denied")
		}
		if listed {
			t.Error("the service was asked to list despite the denial")
		}
	})
}

// TestLegacyEmployeeAdministrationIsClosed pins that the tenant-and-id
// employee RPCs are closed at the adapter: none of them carries a session or a
// permission, so delegating them would bypass the dynamic authorization model.
// They answer Unimplemented and never reach the application service.
func TestLegacyEmployeeAdministrationIsClosed(t *testing.T) {
	t.Parallel()

	calls := []struct {
		name string
		call func(context.Context, *service) error
	}{
		{"RegisterEmployee", func(ctx context.Context, svc *service) error {
			_, err := svc.RegisterEmployee(ctx, &authv1.RegisterEmployeeRequest{})
			return err
		}},
		{"GetEmployee", func(ctx context.Context, svc *service) error {
			_, err := svc.GetEmployee(ctx, &authv1.GetEmployeeRequest{})
			return err
		}},
		{"GetEmployeeByEmail", func(ctx context.Context, svc *service) error {
			_, err := svc.GetEmployeeByEmail(ctx, &authv1.GetEmployeeByEmailRequest{})
			return err
		}},
		{"UpdateEmployee", func(ctx context.Context, svc *service) error {
			_, err := svc.UpdateEmployee(ctx, &authv1.UpdateEmployeeRequest{})
			return err
		}},
		{"SetEmployeePIN", func(ctx context.Context, svc *service) error {
			_, err := svc.SetEmployeePIN(ctx, &authv1.SetEmployeePINRequest{})
			return err
		}},
		{"ActivateEmployee", func(ctx context.Context, svc *service) error {
			_, err := svc.ActivateEmployee(ctx, &authv1.ActivateEmployeeRequest{})
			return err
		}},
		{"DeactivateEmployee", func(ctx context.Context, svc *service) error {
			_, err := svc.DeactivateEmployee(ctx, &authv1.DeactivateEmployeeRequest{})
			return err
		}},
	}

	for _, tc := range calls {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			impl := newServiceUnderTest(&fakeService{})
			err := tc.call(context.Background(), impl)

			if got := status.Code(err); got != codes.Unimplemented {
				t.Errorf("code = %v, want Unimplemented (error %v)", got, err)
			}
			if got := status.Convert(err).Message(); got != "employee administration requires a session-authorized API" {
				t.Errorf("message = %q, want the closed-administration message", got)
			}
		})
	}
}
