// Package apperror_test pins the sentinel vocabulary: every error is non-nil,
// distinct from the others, and carries the message callers match on in logs.
// A layer above wraps these with %w, so errors.Is must keep working through
// wrapping — that property is what the whole package exists for.
package apperror_test

import (
	"errors"
	"fmt"
	"testing"

	"media-service/internal/apperror"
)

// TestSentinelsAreDistinctAndNonNil guards against two sentinels collapsing
// onto one value, which would silently merge two failure classes — a missing
// object in the bucket and a storage outage, for example — into one status.
func TestSentinelsAreDistinctAndNonNil(t *testing.T) {
	t.Parallel()

	sentinels := map[string]error{
		"ErrNotFound":     apperror.ErrNotFound,
		"ErrInvalidInput": apperror.ErrInvalidInput,
		"ErrConflict":     apperror.ErrConflict,
		"ErrStorage":      apperror.ErrStorage,
		"ErrNotReady":     apperror.ErrNotReady,
	}

	seen := make(map[error]string, len(sentinels))
	for name, err := range sentinels {
		if err == nil {
			t.Errorf("%s is nil", name)
			continue
		}
		if other, dup := seen[err]; dup {
			t.Errorf("%s and %s are the same error value", name, other)
		}
		seen[err] = name
	}
}

// TestMessagesAreStable pins the exact strings. They travel into log records
// and, for some sentinels, into response bodies, so a reworded sentinel is a
// visible behaviour change rather than a refactor.
func TestMessagesAreStable(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		err  error
		want string
	}{
		{apperror.ErrNotFound, "not found"},
		{apperror.ErrInvalidInput, "invalid input"},
		{apperror.ErrConflict, "conflict"},
		{apperror.ErrStorage, "object store error"},
		{apperror.ErrNotReady, "media is not ready"},
	} {
		if got := tt.err.Error(); got != tt.want {
			t.Errorf("message = %q, want %q", got, tt.want)
		}
	}
}

// TestSentinelSurvivesWrapping is the contract every layer above depends on:
// context is added with fmt.Errorf, and errors.Is still classifies the cause.
func TestSentinelSurvivesWrapping(t *testing.T) {
	t.Parallel()

	wrapped := fmt.Errorf("confirm upload 7: %w", apperror.ErrStorage)
	if !errors.Is(wrapped, apperror.ErrStorage) {
		t.Error("errors.Is did not see ErrStorage through a %w wrap")
	}
	if errors.Is(wrapped, apperror.ErrNotFound) {
		t.Error("a wrap of ErrStorage classified as ErrNotFound")
	}
}
