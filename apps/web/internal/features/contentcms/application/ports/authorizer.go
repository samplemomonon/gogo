package ports

import "context"

type EditorUser struct {
	ID       string
	TenantID string
}

type PageAuthorizer interface {
	Current(ctx context.Context, sessionID string) (*EditorUser, error)
	CanManage(ctx context.Context, sessionID string) (bool, error)
}
