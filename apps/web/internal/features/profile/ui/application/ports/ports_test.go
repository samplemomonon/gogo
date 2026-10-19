// Package ports_test pins the profile page's input port contract: the auth
// gRPC adapter is the port's one production implementation, and an absent
// session must be answered as "nobody signed in" rather than as a failure.
package ports_test

import (
	"context"
	"testing"

	"gogo/internal/features/profile/ui/application/ports"
	"gogo/internal/features/profile/ui/infrastructure/authclient"
	"gogo/internal/shared/authconsumer"
	"gogo/internal/shared/breaker"
)

// The adapter must satisfy ports.Accounts. This assertion fails to compile the
// moment the adapter's method signature drifts from the interface, so the
// drift surfaces here rather than as a broken resolve in the composition root.
var _ ports.Accounts = (*authclient.Client)(nil)

// TestTheAdapterAnswersAnAbsentSessionThroughThePort pins the port's documented
// contract at runtime: a session that is absent, expired or revoked yields a
// nil account with a nil error, because "nobody is signed in" is an answer.
// The adapter is handed no live connection on purpose — an empty session must
// be resolved without touching the wire, so the check cannot mask a dial.
func TestTheAdapterAnswersAnAbsentSessionThroughThePort(t *testing.T) {
	t.Parallel()

	var accounts ports.Accounts = authclient.New(authconsumer.New(nil, breaker.NewDefault()))

	account, err := accounts.Current(context.Background(), "")
	if err != nil {
		t.Fatalf("Current(\"\") = %v, want nil: an absent session is not a failure", err)
	}
	if account != nil {
		t.Errorf("Current(\"\") = %+v, want nil (nobody signed in)", account)
	}
}
