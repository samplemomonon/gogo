// Package ports defines the profile page's input ports.
package ports

import (
	"context"

	"gogo/internal/features/profile/ui/application/results"
)

// Accounts is the account capability the profile page consumes remotely. It is
// named Accounts, not Auth, because the DI container keys a typed registration
// by the interface's short name: a second `ports.Auth` would collide with the
// auth page's port.
type Accounts interface {
	// Current returns the signed-in account for sessionID, or a nil account
	// when the session is absent, expired or revoked.
	Current(ctx context.Context, sessionID string) (*results.Account, error)
}
