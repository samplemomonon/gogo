package ports

import (
	"context"
	"time"

	"gogo/internal/features/contentcms/domain"
)

// SiteSnapshot is the draft or published shell for one tenant. Revision 0
// represents the built-in default before the tenant has saved site settings.
type SiteSnapshot struct {
	TenantID          string              `json:"tenantId"`
	Settings          domain.SiteSettings `json:"settings"`
	Status            string              `json:"status"`
	Revision          int64               `json:"revision"`
	PublishedRevision int64               `json:"publishedRevision,omitempty"`
	UpdatedAt         time.Time           `json:"updatedAt"`
	PublishedAt       *time.Time          `json:"publishedAt,omitempty"`
}

// SiteStore is the tenant-scoped persistence boundary for the global shell.
// Draft saves never change the public shell; Publish copies the validated
// draft to the published row in one transaction.
type SiteStore interface {
	ReadSiteDraft(ctx context.Context, tenantID string) (SiteSnapshot, error)
	ReadSitePublished(ctx context.Context, tenantID string) (SiteSnapshot, error)
	SaveSiteDraft(ctx context.Context, tenantID string, settings domain.SiteSettings, expectedRevision int64, actorID string) (SiteSnapshot, error)
	PublishSite(ctx context.Context, tenantID string, expectedRevision int64, actorID string) (SiteSnapshot, error)
}
