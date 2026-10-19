// Package authclient is the profile page's adapter over the auth.v1 gRPC
// contract: it resolves a session and projects the employee into the page's own
// read model. The shared authconsumer kit owns the contract's wire vocabulary
// and the calling convention, so this is only the final hop.
package authclient

import (
	"context"

	"gogo/internal/features/profile/ui/application/results"
	"gogo/internal/shared/authconsumer"
)

// Client adapts the shared auth kit to the profile page's port.
type Client struct {
	auth *authconsumer.Client
}

// New wraps the shared auth client as the profile page's adapter.
func New(auth *authconsumer.Client) *Client {
	return &Client{auth: auth}
}

// Current resolves sessionID to the account it belongs to.
//
// A missing, expired or revoked session is not a failure: it yields a nil
// account, so the page renders its signed-out state. An outage is returned as an
// error, because "nobody is signed in" and "we cannot tell who is signed in" are
// different states and the page treats them differently.
func (c *Client) Current(ctx context.Context, sessionID string) (*results.Account, error) {
	outcome, err := c.auth.Lookup(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if outcome.Employee.ID == "" {
		return nil, nil
	}
	return &results.Account{
		ID:    outcome.Employee.ID,
		Name:  outcome.Employee.Name,
		Email: outcome.Employee.Email,
		Role:  outcome.Employee.Role,
	}, nil
}
