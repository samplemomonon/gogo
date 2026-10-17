// Package authclient adapts the shared auth contract consumer to the seller
// page's employee-session port.
package authclient

import (
	"context"

	"gogo/internal/pages/seller/application/ports"
	"gogo/internal/pages/seller/application/results"
	"gogo/internal/shared/authconsumer"
)

type Client struct {
	auth *authconsumer.Client
}

var _ ports.EmployeeSessions = (*Client)(nil)

func New(auth *authconsumer.Client) *Client { return &Client{auth: auth} }

func (c *Client) Current(ctx context.Context, sessionID string) (*results.Employee, error) {
	outcome, err := c.auth.Lookup(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if outcome.Employee.ID == "" {
		return nil, nil
	}
	return &results.Employee{ID: outcome.Employee.ID, Name: outcome.Employee.Name, Role: outcome.Employee.Role}, nil
}

func (c *Client) CheckPermission(ctx context.Context, sessionID, permissionKey string) (bool, error) {
	allowed, err := c.auth.CheckPermission(ctx, sessionID, permissionKey)
	if authconsumer.IsSignedOut(err) {
		return false, nil
	}
	return allowed, err
}
