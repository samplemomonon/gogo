package handler

import (
	"context"

	"gogo/internal/features/contentcms/application/ports"
	"gogo/internal/features/contentcms/domain"
)

type disabledSiteStore struct{}

// DisabledSiteStore keeps the public shell on its built-in defaults when the
// deployment has no native content database; authoring attempts still fail
// closed with ErrNativeStoreUnavailable.
func DisabledSiteStore() ports.SiteStore { return disabledSiteStore{} }

func (disabledSiteStore) ReadSiteDraft(_ context.Context, tenantID string) (ports.SiteSnapshot, error) {
	return ports.SiteSnapshot{
		TenantID: tenantID,
		Settings: domain.DefaultSiteSettings(),
		Status:   ports.StatusDraft,
	}, nil
}

func (disabledSiteStore) ReadSitePublished(_ context.Context, tenantID string) (ports.SiteSnapshot, error) {
	return ports.SiteSnapshot{
		TenantID: tenantID,
		Settings: domain.DefaultSiteSettings(),
		Status:   ports.StatusDraft,
	}, nil
}

func (disabledSiteStore) SaveSiteDraft(context.Context, string, domain.SiteSettings, int64, string) (ports.SiteSnapshot, error) {
	return ports.SiteSnapshot{}, ErrNativeStoreUnavailable
}

func (disabledSiteStore) PublishSite(context.Context, string, int64, string) (ports.SiteSnapshot, error) {
	return ports.SiteSnapshot{}, ErrNativeStoreUnavailable
}
