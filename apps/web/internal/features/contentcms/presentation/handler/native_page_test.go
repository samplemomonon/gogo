package handler

import (
	"bytes"
	"context"
	"errors"
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
	"gogo/internal/features/contentcms/presentation/pages"
)

// memoryPageStore is an in-memory PageStore with the same optimistic
// concurrency contract as the PostgreSQL implementation.
type memoryPageStore struct {
	mu        sync.Mutex
	pages     map[string]ports.Page
	revisions map[string][]ports.Revision
	sections  map[string]ports.ReusableSection
	forms     []memoryFormSubmission
}

type memoryFormSubmission struct {
	tenantID    string
	slug        string
	revision    int64
	componentID string
	values      map[string]string
}

func newMemoryPageStore() *memoryPageStore {
	return &memoryPageStore{
		pages:     make(map[string]ports.Page),
		revisions: make(map[string][]ports.Revision),
		sections:  make(map[string]ports.ReusableSection),
	}
}

func storeKey(tenantID, slug string) string { return tenantID + ":" + slug }

func (s *memoryPageStore) List(_ context.Context, tenantID string) ([]ports.Page, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []ports.Page
	for _, page := range s.pages {
		if page.TenantID == tenantID {
			result = append(result, page)
		}
	}
	return result, nil
}

func (s *memoryPageStore) ReadDraft(_ context.Context, tenantID, slug string) (ports.Page, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	page, ok := s.pages[storeKey(tenantID, slug)]
	if !ok {
		return ports.Page{}, ports.ErrNotFound
	}
	return page, nil
}

func (s *memoryPageStore) ReadPublished(_ context.Context, tenantID, slug string) (ports.Page, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	page, ok := s.pages[storeKey(tenantID, slug)]
	if !ok || page.Status != ports.StatusPublished {
		return ports.Page{}, ports.ErrNotFound
	}
	return page, nil
}

func (s *memoryPageStore) CreateDraft(_ context.Context, tenantID, slug, title string, document domain.Document, actorID string) (ports.Page, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := domain.ValidatePageIdentity(tenantID, slug, title); err != nil {
		return ports.Page{}, err
	}
	normalized, err := document.Normalized()
	if err != nil {
		return ports.Page{}, err
	}
	if err := normalized.Validate(); err != nil {
		return ports.Page{}, err
	}
	key := storeKey(tenantID, slug)
	if _, exists := s.pages[key]; exists {
		return ports.Page{}, ports.ErrAlreadyExists
	}
	now := time.Now()
	page := ports.Page{TenantID: tenantID, Slug: slug, Title: title, Document: normalized, Status: ports.StatusDraft, Revision: 1, CreatedAt: now, UpdatedAt: now}
	s.pages[key] = page
	s.revisions[key] = append(s.revisions[key], ports.Revision{TenantID: tenantID, Slug: slug, Revision: 1, Title: title, Document: normalized, Status: ports.StatusDraft, ActorID: actorID, CreatedAt: now})
	return page, nil
}

func (s *memoryPageStore) SaveDraft(_ context.Context, tenantID, slug, title string, document domain.Document, expectedRevision int64, actorID string) (ports.Page, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := storeKey(tenantID, slug)
	page, exists := s.pages[key]
	if !exists {
		return ports.Page{}, ports.ErrNotFound
	}
	if err := domain.ValidatePageIdentity(tenantID, slug, title); err != nil {
		return ports.Page{}, err
	}
	normalized, err := document.Normalized()
	if err != nil {
		return ports.Page{}, err
	}
	if err := normalized.Validate(); err != nil {
		return ports.Page{}, err
	}
	if expectedRevision > 0 && expectedRevision != page.Revision {
		return ports.Page{}, &ports.ConflictError{Expected: expectedRevision, Current: page.Revision}
	}
	now := time.Now()
	page.Title, page.Document, page.Status = title, normalized, ports.StatusDraft
	page.Revision++
	page.UpdatedAt = now
	s.pages[key] = page
	s.revisions[key] = append(s.revisions[key], ports.Revision{TenantID: tenantID, Slug: slug, Revision: page.Revision, Title: title, Document: normalized, Status: ports.StatusDraft, ActorID: actorID, CreatedAt: now})
	return page, nil
}

func (s *memoryPageStore) Publish(_ context.Context, tenantID, slug string, expectedRevision int64, actorID string) (ports.Page, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := storeKey(tenantID, slug)
	page, exists := s.pages[key]
	if !exists {
		return ports.Page{}, ports.ErrNotFound
	}
	if expectedRevision > 0 && expectedRevision != page.Revision {
		return ports.Page{}, &ports.ConflictError{Expected: expectedRevision, Current: page.Revision}
	}
	if err := page.Document.ValidateForPublish(); err != nil {
		return ports.Page{}, err
	}
	now := time.Now()
	page.Status = ports.StatusPublished
	page.Revision++
	page.UpdatedAt = now
	page.PublishedAt = &now
	s.pages[key] = page
	s.revisions[key] = append(s.revisions[key], ports.Revision{TenantID: tenantID, Slug: slug, Revision: page.Revision, Title: page.Title, Document: page.Document, Status: ports.StatusPublished, ActorID: actorID, CreatedAt: now})
	return page, nil
}

func (s *memoryPageStore) ListRevisions(_ context.Context, tenantID, slug string, limit int) ([]ports.Revision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	revisions := s.revisions[storeKey(tenantID, slug)]
	if len(revisions) == 0 {
		if _, ok := s.pages[storeKey(tenantID, slug)]; !ok {
			return nil, ports.ErrNotFound
		}
	}
	result := make([]ports.Revision, 0, len(revisions))
	for i := len(revisions) - 1; i >= 0 && len(result) < limit; i-- {
		result = append(result, revisions[i])
	}
	return result, nil
}

func (s *memoryPageStore) ReadRevision(_ context.Context, tenantID, slug string, revision int64) (ports.Revision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, candidate := range s.revisions[storeKey(tenantID, slug)] {
		if candidate.Revision == revision {
			return candidate, nil
		}
	}
	return ports.Revision{}, ports.ErrNotFound
}

func (s *memoryPageStore) ListSections(_ context.Context, tenantID string) ([]ports.ReusableSection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []ports.ReusableSection
	for _, section := range s.sections {
		if section.TenantID == tenantID {
			result = append(result, section)
		}
	}
	return result, nil
}

func (s *memoryPageStore) CreateSection(_ context.Context, tenantID, id, name string, component domain.Component, actorID string) (ports.ReusableSection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := (domain.Document{SchemaVersion: domain.SchemaVersion, Components: []domain.Component{component}}).Validate(); err != nil {
		return ports.ReusableSection{}, err
	}
	section := ports.ReusableSection{TenantID: tenantID, ID: id, Name: name, Component: component, CreatedAt: time.Now()}
	s.sections[storeKey(tenantID, id)] = section
	return section, nil
}

func (s *memoryPageStore) DeleteSection(_ context.Context, tenantID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sections[storeKey(tenantID, id)]; !ok {
		return ports.ErrNotFound
	}
	delete(s.sections, storeKey(tenantID, id))
	return nil
}

func (s *memoryPageStore) SubmitForm(_ context.Context, tenantID, slug string, pageRevision int64, componentID string, values map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.forms = append(s.forms, memoryFormSubmission{tenantID: tenantID, slug: slug, revision: pageRevision, componentID: componentID, values: values})
	return nil
}

type allowedPageEditor struct{}

func (allowedPageEditor) Current(_ context.Context, sessionID string) (*ports.EditorUser, error) {
	if sessionID == "valid" {
		return &ports.EditorUser{ID: "editor-1", TenantID: "default"}, nil
	}
	return nil, nil
}

func (allowedPageEditor) CanManage(context.Context, string) (bool, error) { return true, nil }

func TestNativePageCreateSavePreviewAndPublicPublish(t *testing.T) {
	t.Parallel()
	store := newMemoryPageStore()
	handler := NewPageHandlerWithNative(ports.DisabledReader(), store, allowedPageEditor{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	router := chi.NewRouter()
	handler.RegisterRoutes(router)

	create := httptest.NewRequest(http.MethodPost, "/admin/content/pages/", strings.NewReader(url.Values{"slug": {"welcome"}, "title": {"Welcome"}}.Encode()))
	create.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	create.Header.Set("Origin", "http://example.test")
	create.Host = "example.test"
	create.AddCookie(&http.Cookie{Name: "session_id", Value: "valid"})
	created := httptest.NewRecorder()
	router.ServeHTTP(created, create)
	if created.Code != http.StatusSeeOther || created.Header().Get("Location") != "/admin/content/pages/welcome" {
		t.Fatalf("create response = %d, location %q; body=%s", created.Code, created.Header().Get("Location"), created.Body)
	}

	document := `{"schemaVersion":1,"components":[{"id":"heading-1","type":"heading","props":{"text":"Hello from draft"}}]}`
	saveForm := url.Values{"title": {"Welcome"}, "document": {document}}
	save := httptest.NewRequest(http.MethodPost, "/admin/content/pages/welcome", strings.NewReader(saveForm.Encode()))
	save.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	save.Header.Set("Origin", "http://example.test")
	save.Header.Set("HX-Request", "true")
	save.Host = "example.test"
	save.AddCookie(&http.Cookie{Name: "session_id", Value: "valid"})
	saved := httptest.NewRecorder()
	router.ServeHTTP(saved, save)
	if saved.Code != http.StatusOK || !strings.Contains(saved.Body.String(), "Draft saved") {
		t.Fatalf("save response = %d; body=%s", saved.Code, saved.Body)
	}

	preview := httptest.NewRequest(http.MethodGet, "/preview/pages/welcome", nil)
	preview.AddCookie(&http.Cookie{Name: "session_id", Value: "valid"})
	previewed := httptest.NewRecorder()
	router.ServeHTTP(previewed, preview)
	if previewed.Code != http.StatusOK || !strings.Contains(previewed.Body.String(), "Hello from draft") {
		t.Fatalf("preview response = %d; body=%s", previewed.Code, previewed.Body)
	}

	publishForm := url.Values{"title": {"Welcome"}, "document": {document}}
	publish := httptest.NewRequest(http.MethodPost, "/admin/content/pages/welcome/publish", strings.NewReader(publishForm.Encode()))
	publish.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	publish.Header.Set("Origin", "http://example.test")
	publish.Header.Set("HX-Request", "true")
	publish.Host = "example.test"
	publish.AddCookie(&http.Cookie{Name: "session_id", Value: "valid"})
	published := httptest.NewRecorder()
	router.ServeHTTP(published, publish)
	if published.Code != http.StatusOK || !strings.Contains(published.Body.String(), "Page published") {
		t.Fatalf("publish response = %d; body=%s", published.Code, published.Body)
	}

	public := httptest.NewRecorder()
	router.ServeHTTP(public, httptest.NewRequest(http.MethodGet, "/pages/welcome", nil))
	if public.Code != http.StatusOK || !strings.Contains(public.Body.String(), "Hello from draft") {
		t.Fatalf("public response = %d; body=%s", public.Code, public.Body)
	}
}

func TestNativeEditorRequiresSessionPermissionAndSameOrigin(t *testing.T) {
	t.Parallel()
	store := newMemoryPageStore()
	handler := NewPageHandlerWithNative(ports.DisabledReader(), store, allowedPageEditor{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	router := chi.NewRouter()
	handler.RegisterRoutes(router)

	unauthenticated := httptest.NewRecorder()
	router.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, "/admin/content/pages/", nil))
	if unauthenticated.Code != http.StatusSeeOther {
		t.Fatalf("unauthenticated status = %d, want redirect", unauthenticated.Code)
	}

	crossSite := httptest.NewRequest(http.MethodPost, "/admin/content/pages/", strings.NewReader("slug=bad&title=Bad"))
	crossSite.Header.Set("Origin", "https://attacker.test")
	crossSite.Host = "example.test"
	crossSite.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	crossSite.AddCookie(&http.Cookie{Name: "session_id", Value: "valid"})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, crossSite)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status = %d, want 403", response.Code)
	}
}

func TestRegisteredComponentEscapesTextAndReportsUnknownTypes(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	component := pages.RegisteredComponent(domain.Component{Type: "heading", Props: map[string]any{"text": "<script>alert(1)</script>"}})
	if err := component.Render(context.Background(), &output); err != nil {
		t.Fatalf("render heading: %v", err)
	}
	if strings.Contains(output.String(), "<script>") || !strings.Contains(output.String(), "&lt;script&gt;") {
		t.Fatalf("heading content was not escaped: %s", output.String())
	}

	output.Reset()
	unknown := pages.RegisteredComponent(domain.Component{Type: "unexpected"})
	if err := unknown.Render(context.Background(), &output); err != nil {
		t.Fatalf("render diagnostic: %v", err)
	}
	if !strings.Contains(output.String(), "Unsupported component") {
		t.Fatalf("unknown component diagnostic missing: %s", output.String())
	}
}

func TestDecodeDocumentRejectsMalformedJSONAndUnsafeURL(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		`{"schemaVersion":1,`,
		`{"schemaVersion":1,"components":[{"id":"button-1","type":"button","props":{"label":"go","href":"javascript:alert(1)"}}]}`,
		`{"schemaVersion":1,"components":[],"unexpected":true}`,
	} {
		if _, err := decodeDocument(raw); err == nil {
			t.Errorf("decodeDocument(%q) accepted invalid data", raw)
		}
	}
}

func TestEditorUnavailableAndForbiddenStates(t *testing.T) {
	t.Parallel()
	authorizer := staticPageAuthorizer{user: &ports.EditorUser{ID: "editor-1", TenantID: "default"}, allowed: false}
	handler := NewPageHandlerWithNative(ports.DisabledReader(), newMemoryPageStore(), authorizer, slog.New(slog.NewTextHandler(io.Discard, nil)))
	router := chi.NewRouter()
	handler.RegisterRoutes(router)
	request := httptest.NewRequest(http.MethodGet, "/admin/content/pages/", nil)
	request.AddCookie(&http.Cookie{Name: "session_id", Value: "valid"})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("forbidden status = %d, want 403", response.Code)
	}

	unavailable := staticPageAuthorizer{err: errors.New("auth unavailable")}
	handler = NewPageHandlerWithNative(ports.DisabledReader(), newMemoryPageStore(), unavailable, slog.New(slog.NewTextHandler(io.Discard, nil)))
	router = chi.NewRouter()
	handler.RegisterRoutes(router)
	request = httptest.NewRequest(http.MethodGet, "/admin/content/pages/", nil)
	request.AddCookie(&http.Cookie{Name: "session_id", Value: "valid"})
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("auth unavailable status = %d, want 503", response.Code)
	}
}

type staticPageAuthorizer struct {
	user    *ports.EditorUser
	allowed bool
	err     error
}

func (a staticPageAuthorizer) Current(context.Context, string) (*ports.EditorUser, error) {
	return a.user, a.err
}
func (a staticPageAuthorizer) CanManage(context.Context, string) (bool, error) {
	return a.allowed, a.err
}
