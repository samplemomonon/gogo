// Package ports defines the auth page's input ports.
package ports

import (
	"context"

	"gogo/internal/features/auth/ui/application/results"
)

// Auth is the authentication capability the login page consumes remotely.
//
// It speaks the page's own read model (results.Session) rather than the auth
// contract's wire types: the contract is an implementation detail of the
// adapter, and a port that returns wire messages makes every caller — including
// the handler and its tests — a second consumer of the contract.
type Auth interface {
	// Login authenticates and returns the new session.
	//
	// A refused attempt returns an error that still explains itself: the caller
	// shows the reason to the user, so an error carrying only "unavailable"
	// would leave a wrong PIN indistinguishable from a broken dependency.
	Login(ctx context.Context, tenantID, email, pin string) (*results.Session, error)
	// Logout revokes a session. A session that is already gone is not an error
	// the caller can act on.
	Logout(ctx context.Context, sessionID string) error
}
