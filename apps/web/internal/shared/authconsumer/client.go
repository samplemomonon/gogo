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

// vocabulary is the auth contract's error vocabulary. It names no Conflict: the
// contract has no write that can duplicate, and inventing a sentinel for an
// answer it never produces would be a branch callers can never take.
var vocabulary = grpcconsumer.Vocabulary{
	NotFound:        ErrNotFound,
	Invalid:         ErrInvalid,
	Precondition:    ErrPrecondition,
	Unauthenticated: ErrUnauthenticated,
	Unavailable:     ErrAuthUnavailable,
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
		if isSignedOut(err) {
			return Outcome{}, nil
		}
		return Outcome{}, err
	}
	return Outcome{Session: toSession(resp.GetSession()), Employee: toEmployee(resp.GetEmployee())}, nil
}

// isSignedOut reports whether err is the contract's answer that there is no such
// usable session, rather than a failure to reach the service.
func isSignedOut(err error) bool {
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
