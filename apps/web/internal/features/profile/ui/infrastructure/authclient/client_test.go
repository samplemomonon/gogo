// Package authclient_test covers the profile page's gRPC adapter: it projects
// the auth.v1 validate-session wire answer into the page's own read model,
// treats a refusal as "nobody signed in" while an outage stays an error, and
// runs each call under the shared tenant stamp and circuit breaker. The stub is
// contract-only — the page must depend on gogo/contracts/auth/v1, never on the
// auth module's runtime code.
package authclient_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	authv1 "gogo/contracts/auth/v1"
	"gogo/internal/features/profile/ui/application/results"
	"gogo/internal/features/profile/ui/infrastructure/authclient"
	"gogo/internal/shared/authconsumer"
	"gogo/internal/shared/breaker"
	"gogo/internal/shared/grpcx"
	"gogo/internal/shared/tenant"
)

// stubAuth is a contract-only ValidateSession server that records what the
// adapter sent.
type stubAuth struct {
	authv1.UnimplementedAuthServiceServer

	resp *authv1.ValidateSessionResponse
	err  error

	calls      int
	gotSession string
	gotTenant  string
}

func (s *stubAuth) ValidateSession(ctx context.Context, req *authv1.ValidateSessionRequest) (*authv1.ValidateSessionResponse, error) {
	s.calls++
	s.gotSession = req.GetSessionId()
	if id, ok := tenant.FromContext(ctx); ok {
		s.gotTenant = string(id)
	}
	if s.err != nil {
		return nil, s.err
	}
	return s.resp, nil
}

// newClient pairs the adapter with the stub over grpcx.InProcess — the repo's
// bufconn transport, which also installs the tenant interceptors, so the tenant
// assertions below exercise the real calling convention rather than a
// simulation of it.
func newClient(t *testing.T, stub *stubAuth, b *breaker.Breaker) *authclient.Client {
	t.Helper()

	conn, closeServer, err := grpcx.InProcess(func(s grpc.ServiceRegistrar) {
		authv1.RegisterAuthServiceServer(s, stub)
	})
	if err != nil {
		t.Fatalf("grpcx.InProcess: %v", err)
	}
	t.Cleanup(closeServer)
	return authclient.New(authconsumer.New(conn, b))
}

// TestCurrentMapsTheWireOntoThePageReadModel pins the adapter's whole job: the
// employee on the wire lands in the page's own Account, and the session ID the
// caller asked about is the one the request carried.
func TestCurrentMapsTheWireOntoThePageReadModel(t *testing.T) {
	t.Parallel()

	stub := &stubAuth{resp: &authv1.ValidateSessionResponse{
		Session:  &authv1.Session{Id: "s-1", TenantId: "acme", EmployeeId: "e-1"},
		Employee: &authv1.Employee{Id: "e-1", Name: "Rina", Email: "rina@acme.test", Role: "seller"},
	}}
	client := newClient(t, stub, breaker.NewDefault())

	got, err := client.Current(context.Background(), "s-1")
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	want := &results.Account{ID: "e-1", Name: "Rina", Email: "rina@acme.test", Role: "seller"}
	if got == nil || *got != *want {
		t.Errorf("Current = %+v, want %+v (the wire employee projected into the page read model)", got, want)
	}
	if stub.gotSession != "s-1" {
		t.Errorf("request carried session %q, want %q", stub.gotSession, "s-1")
	}
}

// TestCurrentAnswersAnEmptyEmployeeAsSignedOut pins the adapter's own branch:
// the service answered successfully but holds no employee for the session, so
// the page gets a nil account rather than an account with an empty ID.
func TestCurrentAnswersAnEmptyEmployeeAsSignedOut(t *testing.T) {
	t.Parallel()

	stub := &stubAuth{resp: &authv1.ValidateSessionResponse{
		Session: &authv1.Session{Id: "s-1"},
	}}
	client := newClient(t, stub, breaker.NewDefault())

	got, err := client.Current(context.Background(), "s-1")
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if got != nil {
		t.Errorf("Current = %+v, want nil: a session with no employee is nobody signed in", got)
	}
}

// TestCurrentTreatsTheContractsRefusalAsSignedOut pins the refusal/outage
// split at the page boundary: every business answer meaning "this session is
// not usable" becomes a nil account, never an error the page would log as a
// broken dependency.
func TestCurrentTreatsTheContractsRefusalAsSignedOut(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		code codes.Code
	}{
		{"not found", codes.NotFound},
		{"invalid", codes.InvalidArgument},
		{"precondition", codes.FailedPrecondition},
		{"unauthenticated", codes.Unauthenticated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			stub := &stubAuth{err: status.Error(tc.code, "session is not usable")}
			client := newClient(t, stub, breaker.NewDefault())

			got, err := client.Current(context.Background(), "s-1")
			if err != nil {
				t.Fatalf("Current = %v, want a refusal to be an answer, not a failure", err)
			}
			if got != nil {
				t.Errorf("Current = %+v, want nil for a refused session", got)
			}
			if stub.calls != 1 {
				t.Errorf("the service was called %d times, want 1", stub.calls)
			}
		})
	}
}

// TestCurrentReportsAnOutageDistinguishablyFromSignedOut pins the other half
// of the split: an unreachable auth service is an error carrying the outage
// sentinel, so the handler can degrade instead of pretending nobody signed in.
func TestCurrentReportsAnOutageDistinguishablyFromSignedOut(t *testing.T) {
	t.Parallel()

	stub := &stubAuth{err: status.Error(codes.Unavailable, "auth is down")}
	client := newClient(t, stub, breaker.NewDefault())

	got, err := client.Current(context.Background(), "s-1")
	if err == nil {
		t.Fatal("Current = nil, want an error when the auth service is unreachable")
	}
	if !errors.Is(err, authconsumer.ErrAuthUnavailable) {
		t.Errorf("Current = %v, want %v", err, authconsumer.ErrAuthUnavailable)
	}
	if got != nil {
		t.Errorf("Current account = %+v, want nil alongside the outage", got)
	}
}

// TestCurrentSkipsTheWireForAnEmptySession pins the cost contract: a visitor
// with no session cookie must not produce a remote call at all.
func TestCurrentSkipsTheWireForAnEmptySession(t *testing.T) {
	t.Parallel()

	stub := &stubAuth{}
	client := newClient(t, stub, breaker.NewDefault())

	got, err := client.Current(context.Background(), "")
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if got != nil {
		t.Errorf("Current = %+v, want nil", got)
	}
	if stub.calls != 0 {
		t.Errorf("the service was called %d times for an empty session, want 0", stub.calls)
	}
}

// TestCurrentStampsTheResolvedTenantOnTheCall pins the calling convention: the
// tenant resolved for the request reaches the remote auth service, and a
// request with no resolved tenant carries the default rather than an empty
// stamp, so a lookup can never read another site's rows.
func TestCurrentStampsTheResolvedTenantOnTheCall(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		ctx  context.Context
		want string
	}{
		{"resolved tenant", tenant.WithID(context.Background(), "acme"), "acme"},
		{"no tenant resolved falls back to the default", context.Background(), string(tenant.Default)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			stub := &stubAuth{resp: &authv1.ValidateSessionResponse{
				Employee: &authv1.Employee{Id: "e-1"},
			}}
			client := newClient(t, stub, breaker.NewDefault())

			if _, err := client.Current(tc.ctx, "s-1"); err != nil {
				t.Fatalf("Current: %v", err)
			}
			if stub.gotTenant != tc.want {
				t.Errorf("call carried tenant %q, want %q", stub.gotTenant, tc.want)
			}
		})
	}
}

// TestCurrentRunsUnderTheBreaker pins that the adapter's calls are guarded: on
// a failing dependency the circuit opens and the next call is answered without
// dialing the service at all. The long cooldown keeps the assertion
// deterministic — no sleeps, no timing.
func TestCurrentRunsUnderTheBreaker(t *testing.T) {
	t.Parallel()

	stub := &stubAuth{err: status.Error(codes.Unavailable, "auth is down")}
	circuit := breaker.New(1, time.Hour, 1)
	client := newClient(t, stub, circuit)

	if _, err := client.Current(context.Background(), "s-1"); !errors.Is(err, authconsumer.ErrAuthUnavailable) {
		t.Fatalf("first call = %v, want the outage sentinel", err)
	}
	if circuit.State() != breaker.StateOpen {
		t.Fatalf("breaker state = %v after the failure, want open", circuit.State())
	}

	if _, err := client.Current(context.Background(), "s-1"); !errors.Is(err, authconsumer.ErrAuthUnavailable) {
		t.Errorf("second call = %v, want the outage sentinel from the open circuit", err)
	}
	if stub.calls != 1 {
		t.Errorf("the service was called %d times, want 1: the open circuit must not dial again", stub.calls)
	}
}
