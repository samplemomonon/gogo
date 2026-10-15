package grpcconsumer_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"gogo/internal/shared/breaker"
	"gogo/internal/shared/grpcconsumer"
	"gogo/internal/shared/tenant"
)

const breakerTestCooldown = time.Minute

var (
	errNotFound = errors.New("test: not found")
	errInvalid  = errors.New("test: invalid")
	errConflict = errors.New("test: conflict")
	errPrecond  = errors.New("test: precondition")
	errNoAuth   = errors.New("test: unauthenticated")
	errDown     = errors.New("test: unavailable")
)

func testVocabulary() grpcconsumer.Vocabulary {
	return grpcconsumer.Vocabulary{
		NotFound:        errNotFound,
		Invalid:         errInvalid,
		Conflict:        errConflict,
		Precondition:    errPrecond,
		Unauthenticated: errNoAuth,
		Unavailable:     errDown,
	}
}

func TestDoReturnsTheResponseOnSuccess(t *testing.T) {
	t.Parallel()

	caller := grpcconsumer.New(breaker.NewDefault(), testVocabulary())
	got, err := grpcconsumer.Do(caller, context.Background(), "get thing", func(context.Context) (string, error) {
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("Do = %v, want nil", err)
	}
	if got != "ok" {
		t.Errorf("Do = %q, want %q", got, "ok")
	}
}

func TestDoProjectsBusinessAnswersOntoTheVocabulary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		code codes.Code
		want error
	}{
		{"not found", codes.NotFound, errNotFound},
		{"invalid", codes.InvalidArgument, errInvalid},
		{"conflict", codes.AlreadyExists, errConflict},
		{"precondition", codes.FailedPrecondition, errPrecond},
		{"unauthenticated", codes.Unauthenticated, errNoAuth},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			b := breaker.NewDefault()
			caller := grpcconsumer.New(b, testVocabulary())
			_, err := grpcconsumer.Do(caller, context.Background(), "op", func(context.Context) (string, error) {
				return "", status.Error(tc.code, tc.name)
			})
			if !errors.Is(err, tc.want) {
				t.Errorf("Do = %v, want %v", err, tc.want)
			}
			// A dependency answering a business question is healthy: its
			// circuit must stay closed, or the next real caller is denied
			// service by a healthy upstream.
			if b.State() != breaker.StateClosed {
				t.Errorf("breaker = %v, want closed after a business answer", b.State())
			}
		})
	}
}

func TestDoReportsATransportFailureAsUnavailable(t *testing.T) {
	t.Parallel()

	caller := grpcconsumer.New(breaker.NewDefault(), testVocabulary())
	down := status.Error(codes.Unavailable, "down")
	_, err := grpcconsumer.Do(caller, context.Background(), "list things", func(context.Context) (string, error) {
		return "", down
	})
	if !errors.Is(err, errDown) {
		t.Fatalf("Do = %v, want %v", err, errDown)
	}
	if !errors.Is(err, down) {
		t.Errorf("Do = %v, want the upstream status preserved for the log", err)
	}
	if !strings.Contains(err.Error(), "list things") {
		t.Errorf("Do = %v, want the operation name in the message", err)
	}
}

func TestDoDegradesToUnavailableWhenTheCircuitIsOpen(t *testing.T) {
	t.Parallel()

	// One failed probe, then the circuit trips and the next call must not
	// reach the dependency at all.
	b := breaker.New(1, breakerTestCooldown, 1)
	caller := grpcconsumer.New(b, testVocabulary())

	calls := 0
	fail := func(context.Context) (string, error) {
		calls++
		return "", status.Error(codes.Unavailable, "down")
	}
	if _, err := grpcconsumer.Do(caller, context.Background(), "op", fail); !errors.Is(err, errDown) {
		t.Fatalf("first Do = %v, want %v", err, errDown)
	}
	if b.State() != breaker.StateOpen {
		t.Fatalf("breaker = %v, want open after one failure", b.State())
	}

	_, err := grpcconsumer.Do(caller, context.Background(), "op", fail)
	if !errors.Is(err, errDown) {
		t.Errorf("Do with an open circuit = %v, want %v", err, errDown)
	}
	if calls != 1 {
		t.Errorf("upstream called %d times, want 1: an open circuit must not dial", calls)
	}
}

func TestDoDoesNotDialAnOpenCircuit(t *testing.T) {
	t.Parallel()

	b := breaker.New(1, breakerTestCooldown, 1)
	caller := grpcconsumer.New(b, testVocabulary())
	_, _ = grpcconsumer.Do(caller, context.Background(), "op", func(context.Context) (string, error) {
		return "", status.Error(codes.Unavailable, "down")
	})

	dialed := false
	_, err := grpcconsumer.Do(caller, context.Background(), "op", func(context.Context) (string, error) {
		dialed = true
		return "should not happen", nil
	})
	if dialed {
		t.Error("the call ran while the circuit was open")
	}
	if !errors.Is(err, errDown) {
		t.Errorf("Do = %v, want %v", err, errDown)
	}
}

func TestDoUnitReportsFailure(t *testing.T) {
	t.Parallel()

	caller := grpcconsumer.New(breaker.NewDefault(), testVocabulary())
	err := grpcconsumer.DoUnit(caller, context.Background(), "delete thing", func(context.Context) error {
		return status.Error(codes.NotFound, "gone")
	})
	if !errors.Is(err, errNotFound) {
		t.Errorf("DoUnit = %v, want %v", err, errNotFound)
	}
}

func TestDoStampsTheResolvedTenant(t *testing.T) {
	t.Parallel()

	caller := grpcconsumer.New(breaker.NewDefault(), testVocabulary())
	var seen tenant.ID
	_, err := grpcconsumer.Do(caller, tenant.WithID(context.Background(), "acme"), "op", func(ctx context.Context) (string, error) {
		seen = tenant.FromContextOrDefault(ctx)
		return "", nil
	})
	if err != nil {
		t.Fatalf("Do = %v, want nil", err)
	}
	if seen != "acme" {
		t.Errorf("call saw tenant %q, want %q", seen, "acme")
	}
}

func TestWithTenantKeepsTheResolvedTenantWhenNoIDIsGiven(t *testing.T) {
	t.Parallel()

	// A blank id must never overwrite an already-resolved tenant: an adapter
	// that leaves a query struct's tenant empty would otherwise send an empty
	// tenant and read the wrong rows.
	ctx := grpcconsumer.WithTenant(tenant.WithID(context.Background(), "acme"), "")
	if got := tenant.FromContextOrDefault(ctx); got != "acme" {
		t.Errorf("tenant = %q, want %q preserved", got, "acme")
	}

	ctx = grpcconsumer.WithTenant(context.Background(), "other")
	if got := tenant.FromContextOrDefault(ctx); got != "other" {
		t.Errorf("tenant = %q, want %q", got, "other")
	}
}

func TestARefusalKeepsTheReasonsTheUpstreamGave(t *testing.T) {
	t.Parallel()

	caller := grpcconsumer.New(breaker.NewDefault(), testVocabulary())
	_, err := grpcconsumer.Do(caller, context.Background(), "log in", func(context.Context) (string, error) {
		return "", status.Error(codes.FailedPrecondition, "PIN must be 6 digits")
	})

	// Branching stays programmatic: the sentinel still matches through the wrap.
	if !errors.Is(err, errPrecond) {
		t.Errorf("Do = %v, want %v", err, errPrecond)
	}
	// And the reason survives, because a login page that could only say
	// "invalid" would not tell the user what to fix.
	if got := grpcconsumer.Detail(err); got != "PIN must be 6 digits" {
		t.Errorf("Detail = %q, want the upstream's own message", got)
	}
}

func TestAnOutageHasNoClientFacingReason(t *testing.T) {
	t.Parallel()

	caller := grpcconsumer.New(breaker.NewDefault(), testVocabulary())
	_, err := grpcconsumer.Do(caller, context.Background(), "log in", func(context.Context) (string, error) {
		return "", status.Error(codes.Unavailable, "connection refused: 10.0.3.7:9090")
	})
	if !errors.Is(err, errDown) {
		t.Fatalf("Do = %v, want %v", err, errDown)
	}
	// An outage must degrade, never explain itself: the transport's own text is
	// for the log, not for a page.
	if got := grpcconsumer.Detail(err); got != "" {
		t.Errorf("Detail = %q, want no client-facing reason for an outage", got)
	}
}

func TestAnUnnamedStatusIsAnOutageNotAnAnswer(t *testing.T) {
	t.Parallel()

	caller := grpcconsumer.New(breaker.NewDefault(), testVocabulary())
	_, err := grpcconsumer.Do(caller, context.Background(), "op", func(context.Context) (string, error) {
		return "", status.Error(codes.PermissionDenied, "nope")
	})
	if !errors.Is(err, errDown) {
		t.Errorf("Do = %v, want %v for a status the vocabulary does not name", err, errDown)
	}
}

func TestAnUnnamedAnswerDoesNotMasqueradeAsAnOutage(t *testing.T) {
	t.Parallel()

	// A vocabulary that deliberately names only what its contract can answer:
	// a conflict it never produces must not be invented, and must not hide a
	// real outage either.
	vocab := grpcconsumer.Vocabulary{Unavailable: errDown}
	caller := grpcconsumer.New(breaker.NewDefault(), vocab)
	_, err := grpcconsumer.Do(caller, context.Background(), "op", func(context.Context) (string, error) {
		return "", status.Error(codes.NotFound, "gone")
	})
	if !errors.Is(err, errDown) {
		t.Errorf("Do = %v, want %v for an unnamed answer", err, errDown)
	}
}
