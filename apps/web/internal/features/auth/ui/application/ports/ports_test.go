// Package ports_test pins the login page's owned input port. The page depends
// on ports.Auth — its own interface, in its own language — and the remote
// adapter (infrastructure/authclient) is the production type that must satisfy
// it. The assertion below fails to compile the moment either side drifts: a
// port method the adapter no longer implements, or an adapter whose signature
// changed shape. The port is the page's contract; the auth.v1 wire contract
// stays behind the adapter.
package ports_test

import (
	"testing"

	"gogo/internal/features/auth/ui/application/ports"
	"gogo/internal/features/auth/ui/infrastructure/authclient"
)

// The remote adapter is the only production implementation of the page's port,
// so composition root wiring would not build if this stopped holding. This is
// the contract the port package exists to declare.
var _ ports.Auth = (*authclient.Client)(nil)

func TestTheRemoteAdapterSatisfiesThePagesOwnedPort(t *testing.T) {
	t.Parallel()

	// Assigning the adapter to a ports.Auth variable makes the conformance
	// above exercisable at run time as well as at compile time. New does not
	// dial or touch the wrapped client, so a nil shared client is safe here.
	var adapter ports.Auth = authclient.New(nil)
	if adapter == nil {
		t.Fatal("authclient.New returned nil; the page's port has no adapter")
	}
}
