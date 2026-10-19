// Package authclient is the login page's adapter over the auth.v1 contract. It
// is the page's final hop: the shared authconsumer kit owns the contract's wire
// vocabulary and the calling convention, and this maps the shared outcome into
// the page's own read model.
package authclient

import (
	"context"

	"gogo/internal/features/auth/ui/application/results"
	"gogo/internal/shared/authconsumer"
)

// Client adapts the shared auth kit to the login page's port.
type Client struct {
	auth *authconsumer.Client
}

// New wraps the shared auth client as the login page's adapter.
func New(auth *authconsumer.Client) *Client {
	return &Client{auth: auth}
}

// Login authenticates and maps the shared outcome into the page's read model.
func (c *Client) Login(ctx context.Context, tenantID, email, pin string) (*results.Session, error) {
	outcome, err := c.auth.Login(ctx, authconsumer.Credentials{
		TenantID: tenantID,
		Email:    email,
		PIN:      pin,
	})
	if err != nil {
		return nil, err
	}
	return &results.Session{
		ID:        outcome.Session.ID,
		CreatedAt: outcome.Session.CreatedAt,
		ExpiresAt: outcome.Session.ExpiresAt,
		Employee: results.Employee{
			ID:    outcome.Employee.ID,
			Name:  outcome.Employee.Name,
			Email: outcome.Employee.Email,
			Role:  outcome.Employee.Role,
		},
	}, nil
}

// Logout revokes the session carried by sessionID.
func (c *Client) Logout(ctx context.Context, sessionID string) error {
	return c.auth.Logout(ctx, sessionID)
}
