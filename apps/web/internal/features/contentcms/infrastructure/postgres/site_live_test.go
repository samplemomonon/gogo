package postgres

import (
	"context"
	"os"
	"testing"

	"gogo/internal/features/contentcms/domain"
)

func TestLiveSiteStoreDraftPublishIsolation(t *testing.T) {
	dsn := os.Getenv("CONTENTCMS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("CONTENTCMS_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := Connect(ctx, dsn, 1, 2)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer pool.Close()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	store := NewStore(pool)
	tenantID := "contentcms-live-site-test"
	_, _ = pool.Exec(ctx, `DELETE FROM content_site_settings WHERE tenant_id = $1`, tenantID)
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM content_site_settings WHERE tenant_id = $1`, tenantID) }()

	published, err := store.ReadSitePublished(ctx, tenantID)
	if err != nil || published.Settings.SiteName != "Gogo" {
		t.Fatalf("initial published = %+v, %v", published, err)
	}
	draft := domain.DefaultSiteSettings()
	draft.SiteName = "Draft Store"
	draft.HeaderLinks = []domain.SiteLink{{Label: "Home", Href: "/"}}
	saved, err := store.SaveSiteDraft(ctx, tenantID, draft, 0, "live-test")
	if err != nil {
		t.Fatalf("SaveSiteDraft: %v", err)
	}
	if saved.Revision != 1 {
		t.Fatalf("saved revision = %d, want 1", saved.Revision)
	}
	published, err = store.ReadSitePublished(ctx, tenantID)
	if err != nil || published.Settings.SiteName != "Gogo" {
		t.Fatalf("published before publish = %+v, %v", published, err)
	}
	published, err = store.PublishSite(ctx, tenantID, 1, "live-test")
	if err != nil || published.Settings.SiteName != "Draft Store" {
		t.Fatalf("PublishSite = %+v, %v", published, err)
	}
	draft.SiteName = "Draft Store 2"
	if _, err := store.SaveSiteDraft(ctx, tenantID, draft, 2, "live-test"); err != nil {
		t.Fatalf("SaveSiteDraft after publish: %v", err)
	}
	published, err = store.ReadSitePublished(ctx, tenantID)
	if err != nil || published.Settings.SiteName != "Draft Store" {
		t.Fatalf("published after draft save = %+v, %v", published, err)
	}
}
