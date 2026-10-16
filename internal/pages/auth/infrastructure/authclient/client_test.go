// Package authclient_test covers the login page's final hop: the adapter that
// turns the shared authconsumer kit into the page's own port. It is tested
// against the auth.v1 wire contract over an in-process bufconn server, with the
// production tenant interceptors (grpcx.InProcess) active, so the tests pin the
// wire→read-model mapping, the tenant the outgoing call carries, and the two
// error shapes the page branches on: a refusal that explains itself, and an
// outage that does not.
package authclient_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	authv1 "gogo/contracts/auth/v1"
	"gogo/internal/pages/auth/infrastructure/authclient"
	"gogo/internal/shared/authconsumer"
	"gogo/internal/shared/breaker"
	"gogo/internal/shared/grpcconsumer"
	"gogo/internal/shared/grpcx"
	"gogo/internal/shared/tenant"
)

// fakeAuth is a contract-only stub of the auth service: it proves the adapter
// depends on the wire contract alone and lets a test drive the answers.
type fakeAuth struct {
	authv1.UnimplementedAuthServiceServer

	mu        sync.Mutex
	loginResp *authv1.LoginResponse
	loginErr  error
	logoutErr error

	loginCalls  int
	logoutCalls int

	tenantInContext string
	loginRequest    *authv1.LoginRequest
	logoutRequest   *authv1.LogoutRequest
}

func (f *fakeAuth) Login(ctx context.Context, req *authv1.LoginRequest) (*authv1.LoginResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.loginCalls++
	// Read on the server side, so this is the tenant the metadata interceptor
	// put on the context — not the one the test passed in by hand.
	f.tenantInContext = tenant.FromContextOrDefault(ctx).String()
	f.loginRequest = req
	if f.loginErr != nil {
		return nil, f.loginErr
	}
	return f.loginResp, nil
}

func (f *fakeAuth) Logout(_ context.Context, req *authv1.LogoutRequest) (*authv1.LogoutResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.logoutCalls++
	f.logoutRequest = req
	if f.logoutErr != nil {
		return nil, f.logoutErr
	}
	return &authv1.LogoutResponse{}, nil
}

func (f *fakeAuth) loginState() (int, string, *authv1.LoginRequest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.loginCalls, f.tenantInContext, f.loginRequest
}

func (f *fakeAuth) logoutState() (int, *authv1.LogoutRequest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.logoutCalls, f.logoutRequest
}

// newClient wires the adapter over the stub exactly as the composition root
// would, tenant interceptors included.
func newClient(t *testing.T, fake *fakeAuth, b *breaker.Breaker) *authclient.Client {
	t.Helper()

	conn, closeServer, err := grpcx.InProcess(func(s grpc.ServiceRegistrar) {
		authv1.RegisterAuthServiceServer(s, fake)
	})
	if err != nil {
		t.Fatalf("grpcx.InProcess: %v", err)
	}
	t.Cleanup(closeServer)
	return authclient.New(authconsumer.New(conn, b))
}

// TestLoginMapsTheWireIntoThePagesReadModel pins the adapter's whole job: the
// wire session and employee become the page's own results.Session, with the
// window carried as times and nothing the page does not render carried at all.
func TestLoginMapsTheWireIntoThePagesReadModel(t *testing.T) {
	t.Parallel()

	created := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	expires := created.Add(8 * time.Hour)
	fake := &fakeAuth{loginResp: &authv1.LoginResponse{
		Session: &authv1.Session{
			Id: "sess-1", TenantId: "acme", EmployeeId: "emp-1",
			CreatedAt: timestamppb.New(created),
			ExpiresAt: timestamppb.New(expires),
		},
		Employee: &authv1.Employee{Id: "emp-1", Name: "Budi Santoso", Email: "budi@toko.com", Role: "seller"},
	}}
	client := newClient(t, fake, breaker.NewDefault())

	got, err := client.Login(context.Background(), "acme", "budi@toko.com", "123456")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if got == nil {
		t.Fatal("Login returned a nil session with no error")
	}
	if got.ID != "sess-1" {
		t.Errorf("Session.ID = %q, want the wire session's id", got.ID)
	}
	if !got.CreatedAt.Equal(created) || !got.ExpiresAt.Equal(expires) {
		t.Errorf("session window = %v..%v, want %v..%v", got.CreatedAt, got.ExpiresAt, created, expires)
	}
	want := struct{ id, name, email, role string }{"emp-1", "Budi Santoso", "budi@toko.com", "seller"}
	emp := got.Employee
	if emp.ID != want.id || emp.Name != want.name || emp.Email != want.email || emp.Role != want.role {
		t.Errorf("Employee = %+v, want %+v", emp, want)
	}
}

// TestLoginCarriesTheTenantIntoTheCall pins tenant resolution end to end: the
// tenant the caller passes wins, a blank one keeps the tenant already on the
// context, and a call with neither still names the default tenant rather than
// an empty one. The tenant travels both in the outgoing metadata (the stub
// reads it off the server context) and in the request field.
func TestLoginCarriesTheTenantIntoTheCall(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		ctx      context.Context
		tenantID string
		want     string
	}{
		{name: "explicit tenant wins", ctx: context.Background(), tenantID: "acme", want: "acme"},
		{name: "blank keeps the context tenant", ctx: tenant.WithID(context.Background(), "acme"), tenantID: "", want: "acme"},
		{name: "no tenant resolves to the default", ctx: context.Background(), tenantID: "", want: "default"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fake := &fakeAuth{loginResp: &authv1.LoginResponse{Session: &authv1.Session{Id: "sess-1"}}}
			client := newClient(t, fake, breaker.NewDefault())

			if _, err := client.Login(tt.ctx, tt.tenantID, "budi@toko.com", "123456"); err != nil {
				t.Fatalf("Login: %v", err)
			}
			_, ctxTenant, req := fake.loginState()
			if ctxTenant != tt.want {
				t.Errorf("tenant on the incoming call's context = %q, want %q", ctxTenant, tt.want)
			}
			if got := req.GetTenantId(); got != tt.want {
				t.Errorf("request tenant_id = %q, want %q", got, tt.want)
			}
		})
	}
}

// A refused sign-in is the auth service working correctly: the error stays
// matchable as a refusal, keeps the service's own client-facing reason for the
// page to show, returns no session, and leaves the breaker closed so a wrong
// PIN never takes sign-in out of rotation.
func TestLoginRefusalKeepsItsReasonAndTheBreakerClosed(t *testing.T) {
	t.Parallel()

	b := breaker.NewDefault()
	fake := &fakeAuth{loginErr: status.Error(codes.Unauthenticated, "PIN yang Anda masukkan salah")}
	client := newClient(t, fake, b)

	got, err := client.Login(context.Background(), "acme", "budi@toko.com", "000000")
	if got != nil {
		t.Errorf("a refused sign-in returned a session: %+v", got)
	}
	if !errors.Is(err, authconsumer.ErrUnauthenticated) {
		t.Errorf("Login = %v, want it matchable as %v", err, authconsumer.ErrUnauthenticated)
	}
	if detail := grpcconsumer.Detail(err); detail != "PIN yang Anda masukkan salah" {
		t.Errorf("Detail = %q, want the reason the service gave", detail)
	}
	if b.State() != breaker.StateClosed {
		t.Error("a refusal tripped the breaker; a working auth service must stay in rotation")
	}
}

// An outage is not a refusal: it matches the unavailable sentinel and carries
// no client-facing reason, which is exactly how the handler tells "try again"
// from "your PIN was wrong".
func TestLoginOutageIsAnOutageWithoutAReason(t *testing.T) {
	t.Parallel()

	fake := &fakeAuth{loginErr: status.Error(codes.Unavailable, "connection refused")}
	client := newClient(t, fake, breaker.NewDefault())

	got, err := client.Login(context.Background(), "acme", "budi@toko.com", "123456")
	if got != nil {
		t.Errorf("an outage returned a session: %+v", got)
	}
	if !errors.Is(err, authconsumer.ErrAuthUnavailable) {
		t.Errorf("Login = %v, want it matchable as %v", err, authconsumer.ErrAuthUnavailable)
	}
	if detail := grpcconsumer.Detail(err); detail != "" {
		t.Errorf("Detail = %q, want an outage to have no client-facing reason", detail)
	}
}

// The breaker the page injects is the one the calls actually run under: after
// the configured failures the next sign-in attempt fails fast as unavailable
// without reaching the auth service again.
func TestAnOutageTripsTheBreakerSoTheNextCallFailsFast(t *testing.T) {
	t.Parallel()

	b := breaker.New(1, time.Minute, 1)
	fake := &fakeAuth{loginErr: status.Error(codes.Unavailable, "down")}
	client := newClient(t, fake, b)

	if _, err := client.Login(context.Background(), "acme", "budi@toko.com", "123456"); !errors.Is(err, authconsumer.ErrAuthUnavailable) {
		t.Fatalf("first Login = %v, want the outage sentinel", err)
	}
	if _, err := client.Login(context.Background(), "acme", "budi@toko.com", "123456"); !errors.Is(err, authconsumer.ErrAuthUnavailable) {
		t.Fatalf("second Login = %v, want the fast-fail outage sentinel", err)
	}

	calls, _, _ := fake.loginState()
	if calls != 1 {
		t.Errorf("auth service calls = %d, want 1: the open circuit should have answered the second attempt", calls)
	}
	if b.State() != breaker.StateOpen {
		t.Errorf("breaker state = %v, want open after the configured failures", b.State())
	}
}

// Logout passes the session to the contract unchanged: the page's port is
// narrow, and widening it must not quietly rename or drop the ID.
func TestLogoutPassesTheSessionID(t *testing.T) {
	t.Parallel()

	fake := &fakeAuth{}
	client := newClient(t, fake, breaker.NewDefault())

	if err := client.Logout(context.Background(), "sess-1"); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	calls, req := fake.logoutState()
	if calls != 1 {
		t.Fatalf("logout calls = %d, want 1", calls)
	}
	if got := req.GetSessionId(); got != "sess-1" {
		t.Errorf("session_id sent = %q, want sess-1", got)
	}
}

// A logout the service refuses (the session is already gone) reaches the page
// as the contract's not-found sentinel, not as a transport failure.
func TestLogoutPropagatesARefusal(t *testing.T) {
	t.Parallel()

	fake := &fakeAuth{logoutErr: status.Error(codes.NotFound, "session not found")}
	client := newClient(t, fake, breaker.NewDefault())

	if err := client.Logout(context.Background(), "sess-gone"); !errors.Is(err, authconsumer.ErrNotFound) {
		t.Errorf("Logout = %v, want it matchable as %v", err, authconsumer.ErrNotFound)
	}
}
