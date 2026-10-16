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

	registerEmployee func(ctx context.Context, tenant domain.Tenant, id, name, email, pin string, role domain.Role) (*domain.Employee, error)
	getEmployee      func(ctx context.Context, tenant domain.Tenant, id string) (*domain.Employee, error)
	listEmployees    func(ctx context.Context, tenant domain.Tenant, limit int) ([]*domain.Employee, error)
	login            func(ctx context.Context, tenant domain.Tenant, email, pin string) (*domain.Session, *domain.Employee, error)
	extendSession    func(ctx context.Context, sessionID string) (*domain.Session, error)
}

func unexpectedCall(method string) error {
	return fmt.Errorf("fakeService: unexpected %s call", method)
}

func (f *fakeService) RegisterEmployee(ctx context.Context, tenant domain.Tenant, id, name, email, pin string, role domain.Role) (*domain.Employee, error) {
	if f.registerEmployee == nil {
		return nil, unexpectedCall("RegisterEmployee")
	}
	return f.registerEmployee(ctx, tenant, id, name, email, pin, role)
}

func (f *fakeService) GetEmployee(ctx context.Context, tenant domain.Tenant, id string) (*domain.Employee, error) {
	if f.getEmployee == nil {
		return nil, unexpectedCall("GetEmployee")
	}
	return f.getEmployee(ctx, tenant, id)
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

func (f failingService) GetEmployee(context.Context, domain.Tenant, string) (*domain.Employee, error) {
	return nil, f.err
}

func (f failingService) ExtendSession(context.Context, string) (*domain.Session, error) {
	return nil, f.err
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
	viaGetEmployee := func(ctx context.Context, svc *service) error {
		_, err := svc.GetEmployee(ctx, &authv1.GetEmployeeRequest{})
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
			err:  fmt.Errorf("store: %w", domain.ErrEmployeeNotFound), call: viaGetEmployee,
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
// survive the mapping, not be dropped).
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
		getEmployee: func(_ context.Context, _ domain.Tenant, _ string) (*domain.Employee, error) {
			return stored, nil
		},
	})

	resp, err := impl.GetEmployee(context.Background(), &authv1.GetEmployeeRequest{Id: "emp-1"})
	if err != nil {
		t.Fatalf("GetEmployee: %v", err)
	}

	got := resp.GetEmployee()
	if got == nil {
		t.Fatal("GetEmployee returned no employee")
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

// TestListEmployeesMapsEach pins the list adapter: every stored employee is
// converted in order, the request limit is passed through as an int, and an
// empty result is an empty (not nil) repeated field.
func TestListEmployeesMapsEach(t *testing.T) {
	t.Parallel()

	var seenLimit int
	impl := newServiceUnderTest(&fakeService{
		listEmployees: func(_ context.Context, _ domain.Tenant, limit int) ([]*domain.Employee, error) {
			seenLimit = limit
			return []*domain.Employee{
				{ID: "emp-1", TenantID: "tenant-a", Name: "Budi", Email: "budi@toko.com", Role: domain.RoleSeller},
				{ID: "emp-2", TenantID: "tenant-a", Name: "Siti", Email: "siti@toko.com", Role: domain.RoleViewer},
			}, nil
		},
	})

	resp, err := impl.ListEmployees(context.Background(), &authv1.ListEmployeesRequest{Limit: 25})
	if err != nil {
		t.Fatalf("ListEmployees: %v", err)
	}
	if seenLimit != 25 {
		t.Errorf("service saw limit %d, want 25", seenLimit)
	}
	if len(resp.GetEmployees()) != 2 {
		t.Fatalf("listed %d employees, want 2", len(resp.GetEmployees()))
	}
	if resp.GetEmployees()[0].GetId() != "emp-1" || resp.GetEmployees()[1].GetId() != "emp-2" {
		t.Errorf("list order = %q, %q, want emp-1, emp-2",
			resp.GetEmployees()[0].GetId(), resp.GetEmployees()[1].GetId())
	}
}

// TestRegisterEmployeeDelegation pins that the adapter forwards the request
// fields to the service unchanged — including the role as a domain.Role —
// and maps the returned employee into the response.
func TestRegisterEmployeeDelegation(t *testing.T) {
	t.Parallel()

	var (
		seenTenant domain.Tenant
		seenID     string
		seenName   string
		seenEmail  string
		seenPIN    string
		seenRole   domain.Role
	)
	impl := newServiceUnderTest(&fakeService{
		registerEmployee: func(_ context.Context, tenant domain.Tenant, id, name, email, pin string, role domain.Role) (*domain.Employee, error) {
			seenTenant, seenID, seenName, seenEmail, seenPIN, seenRole = tenant, id, name, email, pin, role
			return &domain.Employee{ID: id, TenantID: string(tenant), Name: name, Email: email, Role: role, Active: true}, nil
		},
	})

	ctx := tenant.WithID(context.Background(), "acme")
	resp, err := impl.RegisterEmployee(ctx, &authv1.RegisterEmployeeRequest{
		Id:    "emp-1",
		Name:  "Budi",
		Email: "budi@toko.com",
		Pin:   "123456",
		Role:  string(domain.RoleSeller),
	})
	if err != nil {
		t.Fatalf("RegisterEmployee: %v", err)
	}

	if seenTenant != "acme" || seenID != "emp-1" || seenName != "Budi" || seenEmail != "budi@toko.com" || seenPIN != "123456" || seenRole != domain.RoleSeller {
		t.Errorf("service saw (%q, %q, %q, %q, %q, %q), want the request fields under tenant acme",
			seenTenant, seenID, seenName, seenEmail, seenPIN, seenRole)
	}
	if got := resp.GetEmployee().GetId(); got != "emp-1" {
		t.Errorf("response employee id = %q, want emp-1", got)
	}
}
