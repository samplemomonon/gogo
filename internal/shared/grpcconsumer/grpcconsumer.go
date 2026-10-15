// Package grpcconsumer owns the calling convention that every outbound gRPC
// call in this process follows, so that no adapter has to remember it.
//
// A remote call has four obligations that are identical for every upstream and
// every contract:
//
//  1. The request must carry the tenant. A call that forgets the tenant stamp
//     silently reads the wrong tenant's rows, so the stamp belongs to the
//     convention rather than to each adapter's memory.
//  2. The call must run under a circuit breaker, so a failing dependency stops
//     being dialed and callers fail fast instead of piling up.
//  3. Only a *transport* failure may count against the breaker. A dependency
//     answering "not found" or "invalid argument" is healthy and working, so
//     treating a business rejection as a failure would open the circuit on a
//     service that is doing its job.
//  4. The outcome must be projected into a small, transport-independent error
//     vocabulary the consuming surface can branch on — one sentinel per kind
//     of answer, plus one for "the dependency is unreachable" that stays
//     detectable through errors.Is even when the cause is wrapped.
//
// Doing those four things by hand is what produced six near-identical
// eight-line blocks across catalogconsumer, listingconsumer, the seller's
// inventory and sales clients and the two auth clients — six chances to get
// the open-circuit case wrong. This package makes the convention the single
// place it is written down.
//
// It deliberately owns no wire types. Each contract's kit (catalogconsumer,
// listingconsumer, authconsumer) keeps the wire↔core mapping for its own
// independently versioned contract and calls Do/DoUnit here for the mechanics;
// see ARCHITECTURE.md D-016.
package grpcconsumer

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"gogo/internal/shared/breaker"
	"gogo/internal/shared/tenant"
)

// Vocabulary is one upstream's error vocabulary: the sentinels a caller may
// branch on. A nil entry means "this upstream has no such answer", in which
// case a status of that kind falls through to Unavailable rather than inventing
// a sentinel nobody can compare against.
//
// Every vocabulary must set Unavailable: it is the one sentinel the convention
// guarantees, and it is what lets a surface degrade (503, a cached fragment, a
// signed-out view) instead of failing as an unknown error.
type Vocabulary struct {
	// NotFound is a clean "no such row" answer.
	NotFound error
	// Invalid is a rejection of the request itself.
	Invalid error
	// Conflict is a rejection because the write would duplicate an existing row.
	Conflict error
	// Precondition is a rejection because the domain rule refuses the write
	// (e.g. an adjustment that would drive stock below zero).
	Precondition error
	// Unauthenticated is a rejection because the caller presented no valid
	// session.
	Unauthenticated error
	// Unavailable is "this dependency cannot be reached", whether because the
	// circuit is open, the transport is failing, or the upstream returned a
	// status this vocabulary does not name.
	Unavailable error
}

// Caller runs the outbound calls of a single upstream under that upstream's
// vocabulary. It is safe for concurrent use and holds no per-call state, so one
// Caller serves every method of a contract client.
type Caller struct {
	breaker *breaker.Breaker
	vocab   Vocabulary
}

// New returns a Caller that runs calls under b and projects their outcomes into
// vocab. b must be non-nil: an upstream without a breaker is an upstream that
// cannot fail fast.
func New(b *breaker.Breaker, vocab Vocabulary) *Caller {
	return &Caller{breaker: b, vocab: vocab}
}

// Breaker returns the circuit guarding this upstream, so the gateway's metrics
// and readiness probe can report it alongside the client's own state.
func (c *Caller) Breaker() *breaker.Breaker { return c.breaker }

// WithTenant returns a copy of ctx stamped with the resolved tenant, or with id
// when id is non-empty. A blank id never overwrites an already-resolved tenant:
// that is the failure mode where a call that forgot to thread the tenant
// through its query struct would otherwise be sent with an empty one and read
// nothing, or worse, read another tenant's default rows.
func WithTenant(ctx context.Context, id string) context.Context {
	if id == "" {
		return tenant.WithID(ctx, tenant.FromContextOrDefault(ctx))
	}
	return tenant.WithID(ctx, tenant.ID(id))
}

// Do runs one call that answers with a value, under the tenant stamp and the
// circuit breaker, and returns either the value or a classified error. The call
// runs exactly once unless the circuit is open, in which case it is not run at
// all and Unavailable is returned immediately.
//
// op names the operation ("list products") and is preserved on the returned
// error so a log line names the call that failed.
func Do[T any](c *Caller, ctx context.Context, op string, call func(context.Context) (T, error)) (T, error) {
	ctx = WithTenant(ctx, "")

	var (
		resp   T
		rpcErr error
	)
	// The breaker sees only a transport failure. A business answer — not found,
	// invalid, conflict, precondition, unauthenticated — is the upstream
	// working correctly and must leave the circuit closed.
	err := c.breaker.Execute(func() error {
		r, e := call(ctx)
		resp, rpcErr = r, e
		return transportFailure(e)
	})

	switch {
	case err != nil:
		// The circuit rejected the call, or the transport failed. Either way
		// the dependency is unreachable, and the sentinel must survive
		// wrapping so errors.Is(err, vocab.Unavailable) holds.
		return resp, c.unavailable(op, err)
	case rpcErr != nil:
		return resp, c.classify(op, rpcErr)
	default:
		return resp, nil
	}
}

// DoUnit is Do for a call that answers with no value (a delete, a logout).
func DoUnit(c *Caller, ctx context.Context, op string, call func(context.Context) error) error {
	_, err := Do(c, ctx, op, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, call(ctx)
	})
	return err
}

// unavailable projects a circuit-open rejection or a transport failure onto the
// unavailable sentinel, keeping the cause and the operation name for the log.
func (c *Caller) unavailable(op string, cause error) error {
	if errors.Is(cause, breaker.ErrOpen) {
		// The call never ran, so there is no upstream error to preserve; the
		// operation name is the only diagnostic worth carrying.
		return fmt.Errorf("%w: %s", c.vocab.Unavailable, op)
	}
	return fmt.Errorf("%w: %s: %w", c.vocab.Unavailable, op, cause)
}

// classify projects a gRPC status onto the vocabulary. A status the vocabulary
// does not name is an outage as far as the caller is concerned — an unexpected
// code is not an answer anyone can act on.
func (c *Caller) classify(op string, err error) error {
	if err == nil {
		return nil
	}
	if sentinel := c.vocab.sentinelFor(status.Code(err)); sentinel != nil {
		return &businessError{op: op, sentinel: sentinel, detail: status.Convert(err).Message()}
	}
	return fmt.Errorf("%w: %s: %w", c.vocab.Unavailable, op, err)
}

// businessError is a refusal, not an outage: the upstream answered, and the
// answer was "no". It carries two things at once, because a consumer needs
// both. The sentinel lets the caller branch without parsing prose
// (errors.Is(err, authconsumer.ErrInvalidPIN)), and the detail is the upstream's
// own client-facing reason, which is often the whole point of the call — a login
// page that answered only "invalid" would tell the user nothing about whether
// the email, the PIN or the tenant was the problem. The detail is safe to show
// because it is the message the owning module's error table already publishes
// for exactly this code; internal failures never reach here.
type businessError struct {
	op       string
	sentinel error
	detail   string
}

func (e *businessError) Error() string {
	if e.detail == "" {
		return fmt.Sprintf("%s: %v", e.op, e.sentinel)
	}
	return fmt.Sprintf("%s: %s", e.op, e.detail)
}

// Unwrap exposes the sentinel, so errors.Is matches this refusal exactly as it
// would match a bare sentinel.
func (e *businessError) Unwrap() error { return e.sentinel }

// Detail returns the reason the upstream gave for a refusal, and "" for
// anything else — including an outage, which has no client-facing reason and
// must degrade rather than explain itself.
func Detail(err error) string {
	var refusal *businessError
	if errors.As(err, &refusal) {
		return refusal.detail
	}
	return ""
}

// sentinelFor returns the vocabulary entry for a business status, or nil when
// the code is not a business answer or the vocabulary does not name it.
func (v Vocabulary) sentinelFor(code codes.Code) error {
	switch code {
	case codes.NotFound:
		return v.NotFound
	case codes.InvalidArgument:
		return v.Invalid
	case codes.AlreadyExists:
		return v.Conflict
	case codes.FailedPrecondition:
		return v.Precondition
	case codes.Unauthenticated:
		return v.Unauthenticated
	default:
		return nil
	}
}

// transportFailure reports a gRPC error as a circuit failure only when it means
// the dependency could not answer, as opposed to answering that this request is
// not allowed. A business answer leaves the circuit closed: a service correctly
// rejecting bad input is not a service that needs failing over.
func transportFailure(err error) error {
	if err == nil {
		return nil
	}
	switch status.Code(err) {
	case codes.NotFound, codes.InvalidArgument, codes.AlreadyExists,
		codes.FailedPrecondition, codes.Unauthenticated:
		return nil
	default:
		return err
	}
}
