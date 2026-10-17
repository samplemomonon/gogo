// Package auth_test covers the auth module's gateway contract: the manifest it
// federates, the health report the ops probe aggregates, and the auth.v1 surface
// it mounts. The gRPC smoke tests run over bufconn — a real server/client
// pairing without a socket — so what they pin is the wire contract, not a
// hand-wired handler.
package auth_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	authv1 "gogo/contracts/auth/v1"
	"gogo/internal/modules/auth"
	"gogo/internal/modules/auth/domain"
	"gogo/internal/modules/auth/infrastructure"
	"gogo/internal/shared/module"
)

// The gateway registers modules through module.Interface and collects health
// through module.HealthReporter. These assertions fail the test build if auth
// stops satisfying either — the same checked-contract pattern billing uses
// (billing/module.go:141), applied from the outside so the production file
// stays untouched.
var (
	_ module.Interface      = (*auth.Module)(nil)
	_ module.HealthReporter = (*auth.Module)(nil)
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newModule builds the module over the in-memory store, the same pairing
// compose.Build uses when no database is configured.
func newModule(ping func(context.Context) error) *auth.Module {
	store := infrastructure.NewStore()
	return auth.NewModule(store, store, ping, testLogger())
}

// TestManifest pins the module's federated identity: the shell routes to Entry
// and API clients discover the rest through Routes, so a rename here is a
// breaking contract change, not a cosmetic one.
func TestManifest(t *testing.T) {
	t.Parallel()

	manifest := newModule(nil).Manifest()

	if manifest.Name != "auth" {
		t.Errorf("Name = %q, want auth", manifest.Name)
	}
	if manifest.Version != "1.0.0" {
		t.Errorf("Version = %q, want 1.0.0", manifest.Version)
	}
	if manifest.Entry != "/login" {
		t.Errorf("Entry = %q, want /login", manifest.Entry)
	}
	if got := manifest.Routes["login"]; got != "/login" {
		t.Errorf("Routes[login] = %q, want /login", got)
	}
}

// TestHealthyWithoutAPingIsOK pins the in-memory deployment's readiness: no
// ping means nothing to prove, so the module reports ok — it must not report
// down just because no database is wired.
func TestHealthyWithoutAPingIsOK(t *testing.T) {
	t.Parallel()

	report := newModule(nil).Healthy(context.Background())

	if report.Module != "auth" {
		t.Errorf("Module = %q, want auth", report.Module)
	}
	if report.Status != module.StatusOK {
		t.Errorf("Status = %q, want ok", report.Status)
	}
	if report.Message != "" {
		t.Errorf("Message = %q, want empty for a healthy report", report.Message)
	}
	if !report.Ready() {
		t.Error("an ok report must be ready")
	}
}

// TestHealthyReportsDownWhenThePingFails pins the postgres deployment's
// readiness: a failing ping must surface as down with the module's stable
// message, and the ping must receive the caller's context.
func TestHealthyReportsDownWhenThePingFails(t *testing.T) {
	t.Parallel()

	pingErr := errors.New("connection refused")
	pinged := false
	report := newModule(func(ctx context.Context) error {
		pinged = true
		if ctx == nil {
			t.Error("ping called with a nil context")
		}
		return pingErr
	}).Healthy(context.Background())

	if !pinged {
		t.Error("Healthy did not call the ping")
	}
	if report.Status != module.StatusDown {
		t.Errorf("Status = %q, want down", report.Status)
	}
	if report.Message != "auth database unavailable" {
		t.Errorf("Message = %q, want %q", report.Message, "auth database unavailable")
	}
	if report.Ready() {
		t.Error("a down report must not be ready")
	}
}

// TestHealthyReportsOKWhenThePingSucceeds pins that a live database is not
// reported as degraded: any nil error means the module serves.
func TestHealthyReportsOKWhenThePingSucceeds(t *testing.T) {
	t.Parallel()

	report := newModule(func(context.Context) error { return nil }).Healthy(context.Background())

	if report.Status != module.StatusOK {
		t.Errorf("Status = %q, want ok", report.Status)
	}
}

// TestRegisterRoutesIsANoOp pins the documented no-op (module.go:54-56): the
// login page lives in internal/pages/auth, so the module must mount nothing —
// calling it at all must not panic.
func TestRegisterRoutesIsANoOp(t *testing.T) {
	t.Parallel()

	router := chi.NewRouter()
	newModule(nil).RegisterRoutes(router)

	var routes int
	if err := chi.Walk(router, func(string, string, http.Handler, ...func(http.Handler) http.Handler) error {
		routes++
		return nil
	}); err != nil {
		t.Fatalf("chi.Walk: %v", err)
	}
	if routes != 0 {
		t.Errorf("RegisterRoutes mounted %d routes, want none", routes)
	}
}

// dialAuth registers the module on a real grpc.NewServer behind a bufconn
// listener and returns a client for the auth.v1 contract. It mirrors
// grpcx.InProcess without the tenant interceptors, so the module's own wiring
// is what is under test.
func dialAuth(t *testing.T, mod *auth.Module) authv1.AuthServiceClient {
	t.Helper()

	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	mod.RegisterGRPC(server)
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(server.Stop)

	conn, err := grpc.NewClient(
		"passthrough:///bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return authv1.NewAuthServiceClient(conn)
}

// TestRegisterGRPCServesTheWireContract pins that RegisterGRPC mounts a
// callable auth.v1 surface: an unknown email is answered with NotFound, the
// code the domain's ErrEmployeeNotFound classification maps to.
func TestRegisterGRPCServesTheWireContract(t *testing.T) {
	t.Parallel()

	client := dialAuth(t, newModule(nil))

	_, err := client.Login(context.Background(), &authv1.LoginRequest{
		Email: "nobody@example.com",
		Pin:   "123456",
	})
	if got := status.Code(err); got != codes.NotFound {
		t.Fatalf("Login with an unknown email = %v (code %v), want NotFound", err, got)
	}
}

// TestRegisterGRPCRoundTripsAnEmployee pins the contract end to end: a seeded
// employee logs in over the wire and the response carries the stored session
// and employee. Registration is closed on the wire — the legacy RPCs carry no
// session or permission — so the seed goes straight into the store, the row
// the postgres deployment would hold, and login proves the hash it reads.
func TestRegisterGRPCRoundTripsAnEmployee(t *testing.T) {
	t.Parallel()

	store := infrastructure.NewStore()
	hash, err := bcrypt.GenerateFromPassword([]byte("123456"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("GenerateFromPassword: %v", err)
	}
	employee, err := domain.NewEmployee("default", "emp-1", "Budi", "budi@toko.com", string(hash), domain.RoleSeller)
	if err != nil {
		t.Fatalf("NewEmployee: %v", err)
	}
	if err := store.Create(context.Background(), "default", employee); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	client := dialAuth(t, auth.NewModule(store, store, nil, testLogger()))

	login, err := client.Login(context.Background(), &authv1.LoginRequest{Email: "budi@toko.com", Pin: "123456"})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if got := login.GetSession().GetId(); got == "" {
		t.Error("Login returned no session id")
	}
	if got := login.GetSession().GetEmployeeId(); got != "emp-1" {
		t.Errorf("session EmployeeId = %q, want emp-1", got)
	}
	if got := login.GetEmployee().GetId(); got != "emp-1" {
		t.Errorf("Login employee Id = %q, want emp-1", got)
	}
	if got := login.GetEmployee().GetEmail(); got != "budi@toko.com" {
		t.Errorf("Login employee Email = %q, want the domain-normalised budi@toko.com", got)
	}
	if got := login.GetEmployee().GetRole(); got != string(domain.RoleSeller) {
		t.Errorf("Login employee Role = %q, want %q", got, domain.RoleSeller)
	}
}

// TestRegisterGRPCClosesLegacyEmployeeAdministration pins that the mounted
// wire surface refuses the legacy employee RPCs: they carry no session or
// permission, so delegating them would bypass the dynamic authorization model.
func TestRegisterGRPCClosesLegacyEmployeeAdministration(t *testing.T) {
	t.Parallel()

	client := dialAuth(t, newModule(nil))
	ctx := context.Background()

	if _, err := client.RegisterEmployee(ctx, &authv1.RegisterEmployeeRequest{Id: "emp-1"}); status.Code(err) != codes.Unimplemented {
		t.Errorf("RegisterEmployee = %v (code %v), want Unimplemented", err, status.Code(err))
	}
	if _, err := client.GetEmployeeByEmail(ctx, &authv1.GetEmployeeByEmailRequest{Email: "budi@toko.com"}); status.Code(err) != codes.Unimplemented {
		t.Errorf("GetEmployeeByEmail = %v (code %v), want Unimplemented", err, status.Code(err))
	}
	if _, err := client.ListEmployees(ctx, &authv1.ListEmployeesRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Errorf("ListEmployees without a session = %v (code %v), want Unauthenticated", err, status.Code(err))
	}
}
