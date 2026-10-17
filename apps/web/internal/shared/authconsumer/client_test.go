package authconsumer_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	authv1 "gogo/contracts/auth/v1"
	"gogo/internal/shared/authconsumer"
	"gogo/internal/shared/breaker"
	"gogo/internal/shared/grpcconsumer"
	"gogo/internal/shared/grpcx"
	"gogo/internal/shared/tenant"
)

// fakeAuth is a contract-only stub of the auth service, so the kit is tested
// against the wire it actually consumes.
type fakeAuth struct {
	authv1.UnimplementedAuthServiceServer
	resp     *authv1.LoginResponse
	validate *authv1.ValidateSessionResponse
	err      error
	logins   int
	tenant   string

	canManage      bool
	canManageCalls int
	merchants      *authv1.ListMyMerchantsResponse
}

func (f *fakeAuth) Login(_ context.Context, req *authv1.LoginRequest) (*authv1.LoginResponse, error) {
	f.logins++
	f.tenant = req.GetTenantId()
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func (f *fakeAuth) Logout(_ context.Context, req *authv1.LogoutRequest) (*authv1.LogoutResponse, error) {
	f.tenant = req.GetSessionId()
	if f.err != nil {
		return nil, f.err
	}
	return &authv1.LogoutResponse{}, nil
}

func (f *fakeAuth) ValidateSession(_ context.Context, _ *authv1.ValidateSessionRequest) (*authv1.ValidateSessionResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.validate, nil
}

func (f *fakeAuth) CanManageProduct(_ context.Context, _ *authv1.CanManageProductRequest) (*authv1.CanManageProductResponse, error) {
	f.canManageCalls++
	if f.err != nil {
		return nil, f.err
	}
	return &authv1.CanManageProductResponse{Allowed: f.canManage}, nil
}

func (f *fakeAuth) ListMyMerchants(_ context.Context, _ *authv1.ListMyMerchantsRequest) (*authv1.ListMyMerchantsResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.merchants, nil
}

func newClient(t *testing.T, fake *fakeAuth, b *breaker.Breaker) *authconsumer.Client {
	t.Helper()
	conn, closeServer, err := grpcx.InProcess(func(s grpc.ServiceRegistrar) {
		authv1.RegisterAuthServiceServer(s, fake)
	})
	if err != nil {
		t.Fatalf("grpcx.InProcess returned error: %v", err)
	}
	t.Cleanup(closeServer)
	return authconsumer.New(conn, b)
}

func TestLoginMapsTheWireOntoTheNeutralOutcome(t *testing.T) {
	t.Parallel()

	created := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	expires := created.Add(8 * time.Hour)
	fake := &fakeAuth{resp: &authv1.LoginResponse{
		Session: &authv1.Session{
			Id: "s-1", TenantId: "acme", EmployeeId: "e-1",
			CreatedAt: timestamppb.New(created),
			ExpiresAt: timestamppb.New(expires),
		},
		Employee: &authv1.Employee{Id: "e-1", Name: "Rina", Email: "rina@acme.test", Role: "seller"},
	}}
	client := newClient(t, fake, breaker.NewDefault())

	got, err := client.Login(context.Background(), authconsumer.Credentials{
		TenantID: "acme", Email: "rina@acme.test", PIN: "123456",
	})
	if err != nil {
		t.Fatalf("Login error: %v", err)
	}

	if got.Session.ID != "s-1" || got.Session.EmployeeID != "e-1" {
		t.Errorf("session = %+v, want the wire session mapped", got.Session)
	}
	if !got.Session.CreatedAt.Equal(created) || !got.Session.ExpiresAt.Equal(expires) {
		t.Errorf("session times = %v/%v, want %v/%v", got.Session.CreatedAt, got.Session.ExpiresAt, created, expires)
	}
	if got.Employee.Name != "Rina" || got.Employee.Role != "seller" {
		t.Errorf("employee = %+v, want the wire employee mapped", got.Employee)
	}
}

func TestLoginKeepsTheResolvedTenantWhenNoneIsGiven(t *testing.T) {
	t.Parallel()

	fake := &fakeAuth{resp: &authv1.LoginResponse{Session: &authv1.Session{Id: "s-1"}}}
	client := newClient(t, fake, breaker.NewDefault())

	ctx := tenant.WithID(context.Background(), "acme")
	if _, err := client.Login(ctx, authconsumer.Credentials{Email: "rina@acme.test", PIN: "123456"}); err != nil {
		t.Fatalf("Login error: %v", err)
	}
	if fake.tenant != "acme" {
		t.Errorf("request tenant = %q, want %q from the context", fake.tenant, "acme")
	}
}

// A refusal is the service working correctly, so it must leave the circuit
// closed and must reach the caller with both the sentinel (to branch on) and the
// service's own reason (to show the user).
func TestARefusalIsMatchableAndStillExplainsItself(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		code codes.Code
		want error
	}{
		{"not found", codes.NotFound, authconsumer.ErrNotFound},
		{"invalid", codes.InvalidArgument, authconsumer.ErrInvalid},
		{"precondition", codes.FailedPrecondition, authconsumer.ErrPrecondition},
		{"unauthenticated", codes.Unauthenticated, authconsumer.ErrUnauthenticated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			b := breaker.NewDefault()
			fake := &fakeAuth{err: status.Error(tc.code, "PIN must be 6 digits")}
			client := newClient(t, fake, b)

			_, err := client.Login(context.Background(), authconsumer.Credentials{PIN: "12"})
			if !errors.Is(err, tc.want) {
				t.Errorf("Login = %v, want %v", err, tc.want)
			}
			if got := grpcconsumer.Detail(err); got != "PIN must be 6 digits" {
				t.Errorf("Detail = %q, want the reason the service gave", got)
			}
			if b.State() != breaker.StateClosed {
				t.Error("breaker opened on a refusal, want a working auth service left in rotation")
			}
		})
	}
}

func TestLookupReportsAnAbsentSessionAsNobodySignedIn(t *testing.T) {
	t.Parallel()

	fake := &fakeAuth{err: status.Error(codes.NotFound, "session not found")}
	client := newClient(t, fake, breaker.NewDefault())

	got, err := client.Lookup(context.Background(), "s-gone")
	if err != nil {
		t.Fatalf("Lookup = %v, want a signed-out visitor to be an answer, not a failure", err)
	}
	if got != (authconsumer.Outcome{}) {
		t.Errorf("outcome = %+v, want the zero outcome", got)
	}
}

func TestLookupStillReportsAnOutage(t *testing.T) {
	t.Parallel()

	// The distinction that matters: a visitor with no session and an auth
	// service that is down must not look alike, or the account page would render
	// "signed out" while the dependency is actually broken.
	fake := &fakeAuth{err: status.Error(codes.Unavailable, "down")}
	client := newClient(t, fake, breaker.NewDefault())

	_, err := client.Lookup(context.Background(), "s-1")
	if !errors.Is(err, authconsumer.ErrAuthUnavailable) {
		t.Errorf("Lookup = %v, want %v", err, authconsumer.ErrAuthUnavailable)
	}
	if got := grpcconsumer.Detail(err); got != "" {
		t.Errorf("Detail = %q, want an outage to have no client-facing reason", got)
	}
}

func TestLookupSkipsTheWireForAnEmptySession(t *testing.T) {
	t.Parallel()

	fake := &fakeAuth{validate: &authv1.ValidateSessionResponse{Session: &authv1.Session{Id: "s-1"}}}
	client := newClient(t, fake, breaker.NewDefault())

	got, err := client.Lookup(context.Background(), "")
	if err != nil {
		t.Fatalf("Lookup error: %v", err)
	}
	if got != (authconsumer.Outcome{}) {
		t.Errorf("outcome = %+v, want nobody signed in without a session", got)
	}
}

func TestLogoutOnAnAlreadyRevokedSessionIsAnsweredNotFailed(t *testing.T) {
	t.Parallel()

	fake := &fakeAuth{err: status.Error(codes.NotFound, "session not found")}
	client := newClient(t, fake, breaker.NewDefault())

	if err := client.Logout(context.Background(), "s-gone"); !errors.Is(err, authconsumer.ErrNotFound) {
		t.Errorf("Logout = %v, want %v", err, authconsumer.ErrNotFound)
	}
}

// TestCanManageProductSurfacesTheDecision pins the merchant-scoped PDP as the
// kit exposes it: the decision crosses the wire unchanged, and a missing session
// or merchant is answered locally as a deny without a round trip — the seller
// console's write gate must never be skippable because a field was empty.
func TestCanManageProductSurfacesTheDecision(t *testing.T) {
	t.Parallel()

	t.Run("a granted decision crosses the wire", func(t *testing.T) {
		t.Parallel()

		fake := &fakeAuth{canManage: true}
		client := newClient(t, fake, breaker.NewDefault())

		allowed, err := client.CanManageProduct(context.Background(), "s-1", "merchant-1")
		if err != nil {
			t.Fatalf("CanManageProduct: %v", err)
		}
		if !allowed {
			t.Error("allowed = false, want the decision the wire returned")
		}
		if fake.canManageCalls != 1 {
			t.Errorf("wire calls = %d, want exactly 1", fake.canManageCalls)
		}
	})

	t.Run("a denied decision is false, not an error", func(t *testing.T) {
		t.Parallel()

		fake := &fakeAuth{canManage: false}
		client := newClient(t, fake, breaker.NewDefault())

		allowed, err := client.CanManageProduct(context.Background(), "s-1", "merchant-1")
		if err != nil {
			t.Fatalf("CanManageProduct: %v", err)
		}
		if allowed {
			t.Error("allowed = true, want a deny")
		}
	})

	t.Run("a missing session or merchant denies without calling", func(t *testing.T) {
		t.Parallel()

		fake := &fakeAuth{canManage: true}
		client := newClient(t, fake, breaker.NewDefault())

		for _, tc := range []struct{ session, merchant string }{
			{"", "merchant-1"},
			{"s-1", ""},
		} {
			allowed, err := client.CanManageProduct(context.Background(), tc.session, tc.merchant)
			if err != nil {
				t.Errorf("CanManageProduct(%q,%q): %v", tc.session, tc.merchant, err)
			}
			if allowed {
				t.Errorf("CanManageProduct(%q,%q) allowed, want a deny", tc.session, tc.merchant)
			}
		}
		if fake.canManageCalls != 0 {
			t.Errorf("wire calls = %d, want 0 for empty inputs", fake.canManageCalls)
		}
	})
}

// TestListMyMerchantsMapsTheWire pins the merchant list mapping and the
// signed-out answer: an absent session is an empty list, not an error, because
// every caller is asking "which shops am I in?".
func TestListMyMerchantsMapsTheWire(t *testing.T) {
	t.Parallel()

	fake := &fakeAuth{merchants: &authv1.ListMyMerchantsResponse{Merchants: []*authv1.Merchant{
		{Id: "merchant-1", OwnerEmployeeId: "emp-9", Name: "Toko Bunda"},
		{Id: "merchant-2", OwnerEmployeeId: "emp-3", Name: "Toko Kecil"},
	}}}
	client := newClient(t, fake, breaker.NewDefault())

	got, err := client.ListMyMerchants(context.Background(), "s-1")
	if err != nil {
		t.Fatalf("ListMyMerchants: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("merchants = %d, want 2", len(got))
	}
	if got[0].ID != "merchant-1" || got[0].OwnerEmployeeID != "emp-9" || got[0].Name != "Toko Bunda" {
		t.Errorf("first merchant = %+v, want merchant-1/emp-9/Toko Bunda", got[0])
	}

	empty, err := client.ListMyMerchants(context.Background(), "")
	if err != nil {
		t.Fatalf("ListMyMerchants with no session: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("merchants = %d, want none without a session", len(empty))
	}
}
