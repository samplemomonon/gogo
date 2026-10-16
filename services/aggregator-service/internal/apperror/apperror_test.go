package apperror

import (
	"errors"
	"fmt"
	"testing"
)

// The sentinels are load-bearing across the whole service: the error presenter maps them
// to GraphQL error codes, the retry policy decides which are worth another attempt, and
// the breaker counts only the transient ones. A test that only checks they exist would
// not catch a sentinel that stops being distinguishable, which is the failure that
// matters here.

func TestEachSentinelIsDistinguishable(t *testing.T) {
	// The presenter branches on identity, not on text. If two sentinels compared equal,
	// one branch would silently swallow the other and a client would be told "invalid
	// input" for a dependency outage.
	sentinels := map[string]error{
		"not found":         ErrNotFound,
		"invalid input":     ErrInvalidInput,
		"conflict":          ErrConflict,
		"unavailable":       ErrUnavailable,
		"rejected":          ErrRejected,
		"wrapped not found": fmt.Errorf("category lookup: %w", ErrNotFound),
		"wrapped conflict":  fmt.Errorf("slug taken: %w", ErrConflict),
		"unrelated":         errors.New("something else entirely"),
	}

	for nameA, a := range sentinels {
		for nameB, b := range sentinels {
			if nameA == nameB {
				continue
			}
			if errors.Is(a, b) && errors.Is(b, a) && a.Error() == b.Error() {
				t.Errorf("%q and %q are indistinguishable by errors.Is", nameA, nameB)
			}
		}
	}
}

func TestIsNotFoundMatchesWrappedAndBareForms(t *testing.T) {
	// Both forms matter. The transports wrap the sentinel with context so the log says
	// which dependency failed, and a batch caller has to recognise that wrapper.
	for name, err := range map[string]error{
		"bare":     ErrNotFound,
		"wrapped":  fmt.Errorf("media-service: %w", ErrNotFound),
		"doubly":   fmt.Errorf("product %s: %w", "42", fmt.Errorf("listing: %w", ErrNotFound)),
		"joined":   errors.Join(ErrNotFound, errors.New("category deleted")),
		"with nil": ErrNotFound,
	} {
		if !IsNotFound(err) {
			t.Errorf("%s: IsNotFound(%v) = false, want true", name, err)
		}
	}

	// The negative cases are the ones that must not be treated as an absence. Reporting
	// an outage as "not found" would turn a dependency failure into a null field, which
	// is precisely the behaviour the aggregator is designed to avoid.
	for name, err := range map[string]error{
		"invalid input": ErrInvalidInput,
		"unavailable":   ErrUnavailable,
		"rejected":      ErrRejected,
		"unrelated":     errors.New("boom"),
		"nil":           nil,
	} {
		if IsNotFound(err) {
			t.Errorf("%s: IsNotFound(%v) = true, want false", name, err)
		}
	}
}

func TestInvalidInputCarriesAClientSafeMessage(t *testing.T) {
	// The whole reason this type exists: the bare sentinel says "invalid input", which
	// tells a caller nothing. The message is the single most useful thing in the
	// response, and it is client-safe because only reviewed messages are ever put here.
	err := &InvalidInput{Message: "search term must be at least 2 characters", Field: "query"}

	if err.Error() != "search term must be at least 2 characters" {
		t.Errorf("Error() = %q, want the explanation rather than the sentinel text", err.Error())
	}
	if err.Field != "query" {
		t.Errorf("Field = %q, want query", err.Field)
	}
}

func TestInvalidInputStillMatchesItsSentinel(t *testing.T) {
	// Callers branch on the sentinel as often as on the type. A type whose Unwrap was
	// removed would leave errors.Is failing, and the error presenter would fall through
	// to its generic branch and lose the client-safe code.
	err := &InvalidInput{Message: "price must be positive"}

	if !errors.Is(err, ErrInvalidInput) {
		t.Fatal("errors.Is does not see through the type to ErrInvalidInput")
	}

	// And a wrapped one still works, since transports add context.
	wrapped := fmt.Errorf("createProduct: %w", err)
	if !errors.Is(wrapped, ErrInvalidInput) {
		t.Fatal("errors.Is does not see through a wrapper around InvalidInput")
	}
}

func TestInvalidInputIsNotConfusedWithItsNeighbours(t *testing.T) {
	// The presenter picks a client-facing code per sentinel. If InvalidInput matched
	// ErrUnavailable, a validation failure would be presented as a dependency outage and
	// a client would be told to retry something that will never succeed.
	err := &InvalidInput{Message: "slug must be lowercase"}

	if IsNotFound(err) {
		t.Error("InvalidInput matched ErrNotFound")
	}
	if errors.Is(err, ErrUnavailable) {
		t.Error("InvalidInput matched ErrUnavailable")
	}
	if errors.Is(err, ErrConflict) {
		t.Error("InvalidInput matched ErrConflict")
	}
}

func TestAnEmptyInvalidInputMessageIsStillASentinel(t *testing.T) {
	// A zero-value InvalidInput must not panic and must still classify. The presenter
	// reads the type before the message, so a caller that forgets the message gets a
	// correct classification with an unhelpful string — a degraded error rather than a
	// panic on the request path.
	err := &InvalidInput{}

	if err.Error() != "" {
		t.Errorf("Error() = %q, want empty", err.Error())
	}
	if !errors.Is(err, ErrInvalidInput) {
		t.Error("a zero-value InvalidInput does not match its sentinel")
	}
}
