package compose

import (
	"context"

	"gogo/internal/features/contentcms/application/ports"
	"gogo/internal/shared/authconsumer"
)

type Client struct {
	auth *authconsumer.Client
}

var _ ports.PageAuthorizer = (*Client)(nil)

func New(auth *authconsumer.Client) *Client { return &Client{auth: auth} }

func (c *Client) Current(ctx context.Context, sessionID string) (*ports.EditorUser, error) {
	outcome, err := c.auth.Lookup(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if outcome.Employee.ID == "" {
		return nil, nil
	}
	return &ports.EditorUser{ID: outcome.Employee.ID, TenantID: outcome.Session.TenantID}, nil
}

func (c *Client) CanManage(ctx context.Context, sessionID string) (bool, error) {
	return c.auth.CheckPermission(ctx, sessionID, ports.PageManagePermission)
}
