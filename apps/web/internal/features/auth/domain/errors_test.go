package domain_test

import (
	"errors"
	"fmt"
	"testing"

	"gogo/internal/features/auth/domain"
)

// TestClassifyAndMessageEverySentinel pins the error table the presentation
// adapters project onto the wire. The shadowing it guards against is subtle:
// the specific sentinels wrap the general buckets (ErrInvalidPIN wraps
// ErrInvalidEmployee, ErrExpired wraps ErrInvalidSession), so checking a bucket
// before its specifics silently demotes every specific answer to the bucket's
// code and message.
func TestClassifyAndMessageEverySentinel(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantKind    domain.Kind
		wantMessage string
	}{
		{"unknown employee", domain.ErrEmployeeNotFound, domain.KindNotFound, "employee not found"},
		{"unknown session", domain.ErrSessionNotFound, domain.KindNotFound, "session not found"},
		{"mismatched credentials", domain.ErrInvalidCredentials, domain.KindUnauthenticated, "invalid credentials"},
		{"malformed PIN", domain.ErrInvalidPIN, domain.KindFailedPrecondition, "PIN must be 6 digits"},
		{"expired session", domain.ErrExpired, domain.KindFailedPrecondition, "session has expired"},
		{"malformed employee", domain.ErrInvalidEmployee, domain.KindInvalid, "invalid employee"},
		{"malformed session", domain.ErrInvalidSession, domain.KindInvalid, "invalid session"},
		{"missing tenant", domain.ErrInvalidTenant, domain.KindInvalid, "tenant is required"},
		{"missing id", domain.ErrInvalidID, domain.KindInvalid, "id is required"},
		{"missing name", domain.ErrInvalidName, domain.KindInvalid, "name is required"},
		{"missing role", domain.ErrInvalidRole, domain.KindInvalid, "role is required"},
		{"duplicate email", domain.ErrDuplicateEmail, domain.KindInvalid, "email already exists"},
		{"missing credential", domain.ErrMissingCredential, domain.KindInvalid, "credential is required"},
		{"an unknown failure", errors.New("database down"), domain.KindInternal, "something went wrong"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := domain.Classify(tc.err); got != tc.wantKind {
				t.Errorf("Classify(%v) = %v, want %v", tc.err, got, tc.wantKind)
			}
			if got := domain.Message(tc.err); got != tc.wantMessage {
				t.Errorf("Message(%v) = %q, want %q", tc.err, got, tc.wantMessage)
			}

			// A store or adapter may wrap the sentinel on its way out; the
			// table must answer the same through the chain.
			wrapped := fmt.Errorf("store: %w", tc.err)
			if got := domain.Classify(wrapped); got != tc.wantKind {
				t.Errorf("Classify(wrapped) = %v, want %v", got, tc.wantKind)
			}
			if got := domain.Message(wrapped); got != tc.wantMessage {
				t.Errorf("Message(wrapped) = %q, want %q", got, tc.wantMessage)
			}
		})
	}
}
