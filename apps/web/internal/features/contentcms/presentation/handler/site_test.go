package handler

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"gogo/internal/features/contentcms/application/ports"
	"gogo/internal/features/contentcms/domain"
	shellsite "gogo/internal/shared/web/site"
)

type memorySiteStore struct {
	mu        sync.Mutex
	exists    bool
	snapshot  ports.SiteSnapshot
	published *domain.SiteSettings
}

func newMemorySiteStore() *memorySiteStore { return &memorySiteStore{} }

func (s *memorySiteStore) ReadSiteDraft(_ context.Context, tenantID string) (ports.SiteSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.exists {
		return ports.SiteSnapshot{TenantID: tenantID, Settings: domain.DefaultSiteSettings(), Status: ports.StatusDraft}, nil
	}
	return s.snapshot, nil
}

func (s *memorySiteStore) ReadSitePublished(_ context.Context, tenantID string) (ports.SiteSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.published == nil {
		return ports.SiteSnapshot{TenantID: tenantID, Settings: domain.DefaultSiteSettings(), Status: ports.StatusDraft}, nil
	}
	return ports.SiteSnapshot{
		TenantID: tenantID, Settings: *s.published, Status: ports.StatusPublished,
		Revision: s.snapshot.PublishedRevision, PublishedAt: s.snapshot.PublishedAt,
	}, nil
}

func (s *memorySiteStore) SaveSiteDraft(_ context.Context, tenantID string, settings domain.SiteSettings, expectedRevision int64, _ string) (ports.SiteSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	settings = settings.Normalized()
	if err := settings.Validate(); err != nil {
		return ports.SiteSnapshot{}, err
	}
	if s.exists && expectedRevision != s.snapshot.Revision {
		return ports.SiteSnapshot{}, &ports.ConflictError{Expected: expectedRevision, Current: s.snapshot.Revision}
	}
	if !s.exists && expectedRevision > 0 {
		return ports.SiteSnapshot{}, ports.ErrNotFound
	}
	revision := int64(1)
	if s.exists {
		revision = s.snapshot.Revision + 1
	}
	publishedRevision := s.snapshot.PublishedRevision
	publishedAt := s.snapshot.PublishedAt
	s.snapshot = ports.SiteSnapshot{
		TenantID: tenantID, Settings: settings, Status: ports.StatusDraft, Revision: revision,
		PublishedRevision: publishedRevision, PublishedAt: publishedAt,
	}
	s.exists = true
	return s.snapshot, nil
}

func (s *memorySiteStore) PublishSite(_ context.Context, tenantID string, expectedRevision int64, _ string) (ports.SiteSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.exists {
		return ports.SiteSnapshot{}, ports.ErrNotFound
	}
	if expectedRevision > 0 && expectedRevision != s.snapshot.Revision {
		return ports.SiteSnapshot{}, &ports.ConflictError{Expected: expectedRevision, Current: s.snapshot.Revision}
	}
	if err := s.snapshot.Settings.Validate(); err != nil {
		return ports.SiteSnapshot{}, err
	}
	published := s.snapshot.Settings
	s.published = &published
	s.snapshot.Status = ports.StatusPublished
	s.snapshot.Revision++
	s.snapshot.PublishedRevision = s.snapshot.Revision
	now := time.Now()
	s.snapshot.PublishedAt = &now
	return s.snapshot, nil
}

func siteFormRequest(t *testing.T, method, target string, values url.Values) *http.Request {
	t.Helper()
	request := httptest.NewRequest(method, target, strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", "http://example.test")
	request.Host = "example.test"
	request.AddCookie(&http.Cookie{Name: "session_id", Value: "valid"})
	return request
}

func TestSiteSettingsSavePublishAndDraftPreviewAreIsolated(t *testing.T) {
	t.Parallel()
	store := newMemorySiteStore()
	handler := NewPageHandlerWithSite(ports.DisabledReader(), newMemoryPageStore(), store, allowedPageEditor{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	router := chi.NewRouter()
	handler.RegisterRoutes(router)

	settings := url.Values{
		"revision":       {"0"},
		"siteName":       {"Toko Baru"},
		"tagline":        {"Belanja lokal"},
		"homePageSlug":   {"home"},
		"headerLinks":    {"Beranda | /\nTentang | /pages/tentang"},
		"footerText":     {"Toko Baru — lokal"},
		"footerLinks":    {"Privasi | /pages/privasi"},
		"themePrimary":   {"#4f46e5"},
		"themeSecondary": {""},
		"themeAccent":    {""},
	}
	saved := httptest.NewRecorder()
	router.ServeHTTP(saved, siteFormRequest(t, http.MethodPost, "/admin/content/site/draft", settings))
	if saved.Code != http.StatusSeeOther {
		t.Fatalf("save status = %d, want redirect; body=%s", saved.Code, saved.Body)
	}

	publicSettings, err := handler.SiteSettings(context.Background())
	if err != nil {
		t.Fatalf("SiteSettings() error = %v", err)
	}
	if publicSettings.SiteName != "Gogo" {
		t.Fatalf("public SiteName = %q before publish, want default Gogo", publicSettings.SiteName)
	}

	preview := httptest.NewRecorder()
	previewRequest := httptest.NewRequest(http.MethodGet, "/preview/site", nil)
	previewRequest.AddCookie(&http.Cookie{Name: "session_id", Value: "valid"})
	router.ServeHTTP(preview, previewRequest)
	if preview.Code != http.StatusOK {
		t.Fatalf("preview status = %d; body=%s", preview.Code, preview.Body)
	}
	for _, want := range []string{"Toko Baru", "Beranda", "Toko Baru — lokal", "--color-primary:#4f46e5"} {
		if !strings.Contains(preview.Body.String(), want) {
			t.Errorf("draft preview missing %q: %s", want, preview.Body)
		}
	}

	published := httptest.NewRecorder()
	router.ServeHTTP(published, siteFormRequest(t, http.MethodPost, "/admin/content/site/publish", url.Values{"revision": {"1"}}))
	if published.Code != http.StatusSeeOther {
		t.Fatalf("publish status = %d, want redirect; body=%s", published.Code, published.Body)
	}

	publicSettings, err = handler.SiteSettings(context.Background())
	if err != nil {
		t.Fatalf("SiteSettings() after publish error = %v", err)
	}
	if publicSettings.SiteName != "Toko Baru" || len(publicSettings.HeaderLinks) != 2 {
		t.Fatalf("published settings = %+v, want published site", publicSettings)
	}
}

func TestSiteSettingsValidationPreservesRawFormValues(t *testing.T) {
	t.Parallel()
	handler := NewPageHandlerWithSite(ports.DisabledReader(), newMemoryPageStore(), newMemorySiteStore(), allowedPageEditor{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	router := chi.NewRouter()
	handler.RegisterRoutes(router)

	response := httptest.NewRecorder()
	values := url.Values{
		"revision":     {"0"},
		"siteName":     {"Toko Raw"},
		"homePageSlug": {"home"},
		"headerLinks":  {"missing separator"},
	}
	router.ServeHTTP(response, siteFormRequest(t, http.MethodPost, "/admin/content/site/draft", values))
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", response.Code, response.Body)
	}
	body := response.Body.String()
	for _, want := range []string{"Toko Raw", "missing separator", "write one link per line"} {
		if !strings.Contains(body, want) {
			t.Errorf("validation response missing %q", want)
		}
	}
}

func TestSiteSettingsRoutesRequireEditor(t *testing.T) {
	t.Parallel()
	handler := NewPageHandlerWithSite(ports.DisabledReader(), newMemoryPageStore(), newMemorySiteStore(), allowedPageEditor{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	router := chi.NewRouter()
	handler.RegisterRoutes(router)

	for _, path := range []string{"/admin/content/site", "/preview/site"} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/login" {
			t.Errorf("%s unauthenticated response = %d, location %q; want login redirect", path, response.Code, response.Header().Get("Location"))
		}
	}
}

func TestServeHomeUsesPublishedNativePageAndRespectsConfiguredSlug(t *testing.T) {
	t.Parallel()
	pages := newMemoryPageStore()
	initial := domain.Document{SchemaVersion: domain.SchemaVersion, Components: []domain.Component{{
		ID: "heading-1", Type: "heading", Props: map[string]any{"text": "CMS homepage"},
	}}}
	created, err := pages.CreateDraft(context.Background(), "default", "beranda", "Beranda", initial, "editor-1")
	if err != nil {
		t.Fatalf("CreateDraft() error = %v", err)
	}
	if _, err := pages.Publish(context.Background(), "default", "beranda", created.Revision, "editor-1"); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	handler := NewPageHandlerWithSite(ports.DisabledReader(), pages, newMemorySiteStore(), allowedPageEditor{}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request = request.WithContext(shellsite.WithSettings(request.Context(), shellsite.Settings{
		SiteName: "Gogo", HomePageSlug: "beranda", FooterText: "Gogo",
	}))
	response := httptest.NewRecorder()
	if !handler.ServeHome(response, request) {
		t.Fatal("ServeHome() did not handle a published configured homepage")
	}
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "CMS homepage") {
		t.Fatalf("ServeHome response = %d; body=%s", response.Code, response.Body)
	}

	missing := httptest.NewRequest(http.MethodGet, "/", nil)
	missing = missing.WithContext(shellsite.WithSettings(missing.Context(), shellsite.Settings{
		SiteName: "Gogo", HomePageSlug: "missing", FooterText: "Gogo",
	}))
	if handler.ServeHome(httptest.NewRecorder(), missing) {
		t.Fatal("ServeHome() handled a missing homepage instead of falling back")
	}
}
