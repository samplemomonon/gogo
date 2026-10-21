package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"gogo/internal/features/contentcms/application/ports"
	"gogo/internal/features/contentcms/domain"
)

const headingDocument = `{
  "schemaVersion": 2,
  "components": [
    {
      "id": "heading-1",
      "type": "heading",
      "props": {"text": "Hello draft", "level": "h2", "align": "left", "size": "xl", "weight": "normal", "color": ""}
    }
  ]
}`

const responsiveDocument = `{
  "schemaVersion": 2,
  "components": [
    {
      "id": "heading-1",
      "type": "heading",
      "props": {"text": "Responsive", "level": "h2", "align": "left", "size": "xl", "weight": "normal", "color": ""},
      "responsive": {
        "tablet": {"align": "center", "size": "2xl"},
        "mobile": {"align": "right"}
      }
    }
  ]
}`

const formDocument = `{
  "schemaVersion": 2,
  "components": [
    {"id": "intro", "type": "text", "props": {"text": "Contact us"}},
    {
      "id": "contact-form",
      "type": "form",
      "props": {
        "title": "Contact",
        "submitLabel": "Send",
        "fields": [
          {"name": "email", "label": "Email", "type": "email", "required": true},
          {"name": "message", "label": "Message", "type": "textarea", "required": false}
        ],
        "successMessage": "Thanks",
        "align": "left",
        "background": "",
        "color": ""
      }
    }
  ]
}`

const legacyDocument = `{
  "schemaVersion": 1,
  "components": [
    {
      "id": "legacy-section",
      "type": "section",
      "props": {"layout": "grid", "columns": 2, "background": "#abcdef", "mystery": "kept"},
      "children": [
        {"id": "legacy-text", "type": "text", "props": {"text": "Legacy content"}}
      ]
    }
  ]
}`

type editorAPIHarness struct {
	t      *testing.T
	store  *memoryPageStore
	router http.Handler
}

func newEditorAPIHarness(t *testing.T) *editorAPIHarness {
	t.Helper()
	store := newMemoryPageStore()
	handler := NewPageHandlerWithNative(ports.DisabledReader(), store, allowedPageEditor{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	router := chi.NewRouter()
	handler.RegisterRoutes(router)
	return &editorAPIHarness{t: t, store: store, router: router}
}

func (h *editorAPIHarness) createDraft(slug, title, document string) ports.Page {
	h.t.Helper()
	decoded, err := decodeDocument(document)
	if err != nil {
		h.t.Fatalf("decode seed document: %v", err)
	}
	page, err := h.store.CreateDraft(context.Background(), "default", slug, title, decoded, "seed")
	if err != nil {
		h.t.Fatalf("seed draft: %v", err)
	}
	return page
}

func (h *editorAPIHarness) request(method, path string, body []byte) *http.Request {
	h.t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request := httptest.NewRequest(method, path, reader)
	request.Host = "example.test"
	request.Header.Set("Origin", "http://example.test")
	request.AddCookie(&http.Cookie{Name: "session_id", Value: "valid"})
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	return request
}

func (h *editorAPIHarness) do(request *http.Request) *httptest.ResponseRecorder {
	h.t.Helper()
	response := httptest.NewRecorder()
	h.router.ServeHTTP(response, request)
	return response
}

func savePayload(t *testing.T, title, document string, expected int64) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"title":            title,
		"document":         json.RawMessage(document),
		"expectedRevision": expected,
	})
	if err != nil {
		t.Fatalf("marshal save payload: %v", err)
	}
	return payload
}

func decodeDraftPayload(t *testing.T, body []byte) draftPayload {
	t.Helper()
	var payload draftPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode draft payload: %v; body=%s", err, body)
	}
	return payload
}

func TestEditorJSONDraftSaveConflictRevisionRestoreAndPublish(t *testing.T) {
	t.Parallel()
	harness := newEditorAPIHarness(t)
	harness.createDraft("welcome", "Welcome", headingDocument)

	draft := harness.do(harness.request(http.MethodGet, "/admin/content/pages/welcome/draft", nil))
	if draft.Code != http.StatusOK {
		t.Fatalf("get draft = %d; body=%s", draft.Code, draft.Body)
	}
	if got := draft.Header().Get("ETag"); got != `"1"` {
		t.Fatalf("draft ETag = %q, want %q", got, `"1"`)
	}
	payload := decodeDraftPayload(t, draft.Body.Bytes())
	if payload.Page.Revision != 1 || len(payload.Document.Components) != 1 {
		t.Fatalf("draft payload = %#v", payload)
	}

	save := harness.request(http.MethodPut, "/admin/content/pages/welcome/draft", savePayload(t, "Renamed", headingDocument, 1))
	save.Header.Set("If-Match", `"1"`)
	saved := harness.do(save)
	if saved.Code != http.StatusOK {
		t.Fatalf("save draft = %d; body=%s", saved.Code, saved.Body)
	}
	if got := saved.Header().Get("ETag"); got != `"2"` {
		t.Fatalf("save ETag = %q, want revision 2", got)
	}
	var state draftState
	if err := json.Unmarshal(saved.Body.Bytes(), &state); err != nil || state.Revision != 2 || state.Title != "Renamed" {
		t.Fatalf("save state = %#v, err=%v; body=%s", state, err, saved.Body)
	}

	stale := harness.request(http.MethodPut, "/admin/content/pages/welcome/draft", savePayload(t, "Stale", headingDocument, 1))
	stale.Header.Set("If-Match", `"1"`)
	conflicted := harness.do(stale)
	if conflicted.Code != http.StatusConflict {
		t.Fatalf("stale save = %d, want 409; body=%s", conflicted.Code, conflicted.Body)
	}
	var conflict problemResponse
	if err := json.Unmarshal(conflicted.Body.Bytes(), &conflict); err != nil || conflict.CurrentRevision != 2 {
		t.Fatalf("conflict = %#v, err=%v; body=%s", conflict, err, conflicted.Body)
	}

	missing := harness.do(harness.request(http.MethodPut, "/admin/content/pages/welcome/draft", savePayload(t, "No revision", headingDocument, 0)))
	if missing.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing revision = %d, want 412; body=%s", missing.Code, missing.Body)
	}

	revisions := harness.do(harness.request(http.MethodGet, "/admin/content/pages/welcome/revisions?limit=10", nil))
	if revisions.Code != http.StatusOK {
		t.Fatalf("list revisions = %d; body=%s", revisions.Code, revisions.Body)
	}
	var history struct {
		Revisions []revisionSummary `json:"revisions"`
	}
	if err := json.Unmarshal(revisions.Body.Bytes(), &history); err != nil || len(history.Revisions) != 2 {
		t.Fatalf("revisions = %#v, err=%v; body=%s", history, err, revisions.Body)
	}
	if history.Revisions[0].Revision != 2 || history.Revisions[1].Revision != 1 {
		t.Fatalf("revision order = %#v", history.Revisions)
	}

	restore := harness.request(http.MethodPost, "/admin/content/pages/welcome/revisions/1/restore", []byte(`{"expectedRevision":2}`))
	restore.Header.Set("If-Match", `"2"`)
	restored := harness.do(restore)
	if restored.Code != http.StatusOK {
		t.Fatalf("restore revision = %d; body=%s", restored.Code, restored.Body)
	}
	restoredPayload := decodeDraftPayload(t, restored.Body.Bytes())
	if restoredPayload.Page.Revision != 3 || restoredPayload.Page.Title != "Welcome" {
		t.Fatalf("restored page = %#v", restoredPayload.Page)
	}

	publish := harness.request(http.MethodPost, "/admin/content/pages/welcome/publish", []byte(`{"expectedRevision":3}`))
	publish.Header.Set("If-Match", `"3"`)
	published := harness.do(publish)
	if published.Code != http.StatusOK {
		t.Fatalf("publish = %d; body=%s", published.Code, published.Body)
	}
	var publishedState struct {
		draftState
		PublicURL string `json:"publicUrl"`
	}
	if err := json.Unmarshal(published.Body.Bytes(), &publishedState); err != nil || publishedState.Status != ports.StatusPublished || publishedState.PublicURL != "/pages/welcome" {
		t.Fatalf("publish state = %#v, err=%v; body=%s", publishedState, err, published.Body)
	}

	public := harness.do(harness.request(http.MethodGet, "/pages/welcome", nil))
	if public.Code != http.StatusOK || !strings.Contains(public.Body.String(), "Hello draft") {
		t.Fatalf("public page = %d; body=%s", public.Code, public.Body)
	}
	if got := public.Header().Get("Cache-Control"); got != "public, max-age=60, stale-while-revalidate=30" {
		t.Fatalf("public Cache-Control = %q", got)
	}
}

func TestEditorJSONValidationErrorsAndLegacyMigration(t *testing.T) {
	t.Parallel()
	harness := newEditorAPIHarness(t)
	harness.createDraft("legacy", "Legacy", headingDocument)

	invalid := `{"schemaVersion":2,"components":[{"id":"video-1","type":"video","props":{}}]}`
	rejected := harness.do(harness.request(http.MethodPut, "/admin/content/pages/legacy/draft", savePayload(t, "Legacy", invalid, 1)))
	if rejected.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid save = %d, want 422; body=%s", rejected.Code, rejected.Body)
	}
	var problem problemResponse
	if err := json.Unmarshal(rejected.Body.Bytes(), &problem); err != nil || len(problem.Errors) == 0 {
		t.Fatalf("validation problem = %#v, err=%v; body=%s", problem, err, rejected.Body)
	}
	if problem.Errors[0].Path != "components[0].type" {
		t.Fatalf("validation path = %#v", problem.Errors)
	}

	migrated := harness.do(harness.request(http.MethodPut, "/admin/content/pages/legacy/draft", savePayload(t, "Legacy migrated", legacyDocument, 1)))
	if migrated.Code != http.StatusOK {
		t.Fatalf("legacy save = %d; body=%s", migrated.Code, migrated.Body)
	}
	read := harness.do(harness.request(http.MethodGet, "/admin/content/pages/legacy/draft", nil))
	payload := decodeDraftPayload(t, read.Body.Bytes())
	if payload.Document.SchemaVersion != domain.SchemaVersion {
		t.Fatalf("migrated schemaVersion = %d", payload.Document.SchemaVersion)
	}
	if len(payload.Document.Components) != 1 || payload.Document.Components[0].Type != "container" {
		t.Fatalf("migrated components = %#v", payload.Document.Components)
	}
	if payload.Document.Components[0].Extensions["legacyProps"] == nil {
		t.Fatalf("legacy props were not preserved: %#v", payload.Document.Components[0].Extensions)
	}
	if !strings.Contains(string(payload.Document.Components[0].Extensions["legacyProps"]), `"mystery":"kept"`) {
		t.Fatalf("legacy props payload = %s", payload.Document.Components[0].Extensions["legacyProps"])
	}
}

func TestEditorSectionsAndPublicFormSubmission(t *testing.T) {
	t.Parallel()
	harness := newEditorAPIHarness(t)
	harness.createDraft("contact", "Contact", formDocument)

	sectionBody := []byte(`{"name":"Hero heading","component":{"id":"section-heading","type":"heading","props":{"text":"Reusable","level":"h2"}}}`)
	created := harness.do(harness.request(http.MethodPost, "/admin/content/sections", sectionBody))
	if created.Code != http.StatusCreated {
		t.Fatalf("create section = %d; body=%s", created.Code, created.Body)
	}
	var section ports.ReusableSection
	if err := json.Unmarshal(created.Body.Bytes(), &section); err != nil || section.ID == "" || section.Name != "Hero heading" {
		t.Fatalf("created section = %#v, err=%v; body=%s", section, err, created.Body)
	}

	listed := harness.do(harness.request(http.MethodGet, "/admin/content/sections", nil))
	var listedPayload struct {
		Sections []ports.ReusableSection `json:"sections"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &listedPayload); err != nil || len(listedPayload.Sections) != 1 {
		t.Fatalf("listed sections = %#v, err=%v; body=%s", listedPayload, err, listed.Body)
	}

	invalidSection := harness.do(harness.request(http.MethodPost, "/admin/content/sections", []byte(`{"name":"Bad","component":{"id":"bad","type":"image","props":{"src":"javascript:alert(1)"}}}`)))
	if invalidSection.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid section = %d, want 422; body=%s", invalidSection.Code, invalidSection.Body)
	}

	deleted := harness.do(harness.request(http.MethodDelete, "/admin/content/sections/"+section.ID, nil))
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete section = %d; body=%s", deleted.Code, deleted.Body)
	}

	save := harness.request(http.MethodPut, "/admin/content/pages/contact/draft", savePayload(t, "Contact", formDocument, 1))
	save.Header.Set("If-Match", `"1"`)
	if response := harness.do(save); response.Code != http.StatusOK {
		t.Fatalf("save contact draft = %d; body=%s", response.Code, response.Body)
	}
	publish := harness.request(http.MethodPost, "/admin/content/pages/contact/publish", []byte(`{"expectedRevision":2}`))
	publish.Header.Set("If-Match", `"2"`)
	if response := harness.do(publish); response.Code != http.StatusOK {
		t.Fatalf("publish contact = %d; body=%s", response.Code, response.Body)
	}

	public := harness.do(harness.request(http.MethodGet, "/pages/contact", nil))
	if public.Code != http.StatusOK {
		t.Fatalf("public contact = %d; body=%s", public.Code, public.Body)
	}
	page := public.Body.String()
	for _, expected := range []string{
		`action="/pages/contact/forms/contact-form"`,
		`name="email"`,
		`name="message"`,
		`type="email"`,
		"Contact us",
	} {
		if !strings.Contains(page, expected) {
			t.Fatalf("public form missing %q: %s", expected, page)
		}
	}

	submit := harness.request(http.MethodPost, "/pages/contact/forms/contact-form", []byte(url.Values{"email": {"person@example.test"}, "message": {"Hello"}}.Encode()))
	submit.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	submitted := harness.do(submit)
	if submitted.Code != http.StatusSeeOther {
		t.Fatalf("form submit = %d; body=%s", submitted.Code, submitted.Body)
	}
	if location := submitted.Header().Get("Location"); location != "/pages/contact?form=received&formId=contact-form#form-contact-form" {
		t.Fatalf("form redirect = %q", location)
	}
	if len(harness.store.forms) != 1 {
		t.Fatalf("stored forms = %#v", harness.store.forms)
	}
	stored := harness.store.forms[0]
	if stored.componentID != "contact-form" || stored.values["email"] != "person@example.test" || stored.values["message"] != "Hello" {
		t.Fatalf("stored form = %#v", stored)
	}

	missingRequired := harness.request(http.MethodPost, "/pages/contact/forms/contact-form", []byte(url.Values{"message": {"No email"}}.Encode()))
	missingRequired.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	invalid := harness.do(missingRequired)
	if invalid.Code != http.StatusSeeOther || invalid.Header().Get("Location") != "/pages/contact?form=invalid#form-contact-form" {
		t.Fatalf("invalid form = %d, location %q", invalid.Code, invalid.Header().Get("Location"))
	}
	if len(harness.store.forms) != 1 {
		t.Fatalf("invalid form was stored: %#v", harness.store.forms)
	}

	notFound := harness.request(http.MethodPost, "/pages/contact/forms/missing", []byte(url.Values{"email": {"person@example.test"}}.Encode()))
	notFound.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if response := harness.do(notFound); response.Code != http.StatusNotFound {
		t.Fatalf("missing form = %d, want 404", response.Code)
	}

	notice := harness.do(harness.request(http.MethodGet, "/pages/contact?form=received", nil))
	if notice.Code != http.StatusOK || !strings.Contains(notice.Body.String(), "Your message was received.") {
		t.Fatalf("form notice = %d; body=%s", notice.Code, notice.Body)
	}
	if got := notice.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("form notice Cache-Control = %q", got)
	}

	custom := harness.do(harness.request(http.MethodGet, "/pages/contact?form=received&formId=contact-form", nil))
	if custom.Code != http.StatusOK || !strings.Contains(custom.Body.String(), "Thanks") {
		t.Fatalf("custom form notice = %d; body=%s", custom.Code, custom.Body)
	}
	if strings.Contains(custom.Body.String(), "Your message was received.") {
		t.Fatalf("custom form notice used the fallback copy: %s", custom.Body)
	}

	unknownForm := harness.do(harness.request(http.MethodGet, "/pages/contact?form=received&formId=missing", nil))
	if unknownForm.Code != http.StatusOK || !strings.Contains(unknownForm.Body.String(), "Your message was received.") {
		t.Fatalf("unknown form notice = %d; body=%s", unknownForm.Code, unknownForm.Body)
	}
}

func TestEditorBootstrapExposesSchemaAndEndpoints(t *testing.T) {
	t.Parallel()
	harness := newEditorAPIHarness(t)
	harness.createDraft("welcome", "Welcome", headingDocument)

	response := harness.do(harness.request(http.MethodGet, "/admin/content/pages/welcome", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("editor page = %d; body=%s", response.Code, response.Body)
	}
	body := response.Body.String()
	for _, expected := range []string{
		"data-page-builder",
		`data-slug="welcome"`,
		`id="page-builder-data"`,
		`"type":"heading"`,
		`"type":"container"`,
		`"type":"ad-slot"`,
		`"label":"Dynamic Data"`,
		`"/admin/content/pages/welcome/draft"`,
		`"/admin/content/pages/welcome/revisions"`,
		`"/preview/pages/welcome"`,
		`page-builder.js`,
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("editor bootstrap missing %q", expected)
		}
	}
	if got := response.Header().Get("ETag"); got != `"1"` {
		t.Errorf("editor ETag = %q, want %q", got, `"1"`)
	}
}

func TestEditorJSONSaveFailureReturnsStructuredProblem(t *testing.T) {
	t.Parallel()
	base := newMemoryPageStore()
	document, err := decodeDocument(headingDocument)
	if err != nil {
		t.Fatalf("decode seed document: %v", err)
	}
	if _, err := base.CreateDraft(context.Background(), "default", "welcome", "Welcome", document, "seed"); err != nil {
		t.Fatalf("seed draft: %v", err)
	}
	handler := NewPageHandlerWithNative(ports.DisabledReader(), failingSaveStore{memoryPageStore: base}, allowedPageEditor{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	router := chi.NewRouter()
	handler.RegisterRoutes(router)

	request := httptest.NewRequest(http.MethodPut, "/admin/content/pages/welcome/draft", bytes.NewReader(savePayload(t, "Welcome", headingDocument, 1)))
	request.Host = "example.test"
	request.Header.Set("Origin", "http://example.test")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("If-Match", `"1"`)
	request.AddCookie(&http.Cookie{Name: "session_id", Value: "valid"})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("failed save = %d, want 503; body=%s", response.Code, response.Body)
	}
	var problem problemResponse
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode problem response: %v; body=%s", err, response.Body)
	}
	if problem.Code != "unavailable" || !strings.Contains(problem.Message, "temporarily unavailable") {
		t.Fatalf("problem = %#v", problem)
	}
	if strings.Contains(response.Body.String(), "database unavailable") {
		t.Fatalf("internal store error leaked to the client: %s", response.Body)
	}
}

type failingSaveStore struct {
	*memoryPageStore
}

func (failingSaveStore) SaveDraft(context.Context, string, string, string, domain.Document, int64, string) (ports.Page, error) {
	return ports.Page{}, errors.New("database unavailable")
}

func TestPublicPageRendersResponsiveStylesAndFailsClosed(t *testing.T) {
	t.Parallel()
	harness := newEditorAPIHarness(t)
	harness.createDraft("responsive", "Responsive", responsiveDocument)

	publish := harness.request(http.MethodPost, "/admin/content/pages/responsive/publish", []byte(`{"expectedRevision":1}`))
	publish.Header.Set("If-Match", `"1"`)
	published := harness.do(publish)
	if published.Code != http.StatusOK {
		t.Fatalf("publish responsive = %d; body=%s", published.Code, published.Body)
	}

	public := harness.do(harness.request(http.MethodGet, "/pages/responsive", nil))
	if public.Code != http.StatusOK {
		t.Fatalf("public responsive = %d; body=%s", public.Code, public.Body)
	}
	page := public.Body.String()
	for _, expected := range []string{
		".cms-heading-1{",
		"@media (min-width:768px) and (max-width:1023px)",
		"@media (max-width:767px)",
		"text-align:center",
		"text-align:right",
	} {
		if !strings.Contains(page, expected) {
			t.Fatalf("responsive page missing %q: %s", expected, page)
		}
	}

	// The public boundary must never render a stored document that no longer
	// satisfies publish validation. Simulate corruption after publication.
	harness.store.mu.Lock()
	key := storeKey("default", "responsive")
	corrupt := harness.store.pages[key]
	corrupt.Document = domain.Document{
		SchemaVersion: domain.SchemaVersion,
		Components: []domain.Component{{
			ID: "heading-1", Type: "heading", Props: map[string]any{"text": ""},
		}},
	}
	harness.store.pages[key] = corrupt
	harness.store.mu.Unlock()

	failed := harness.do(harness.request(http.MethodGet, "/pages/responsive", nil))
	if failed.Code != http.StatusInternalServerError {
		t.Fatalf("invalid published page = %d, want 500; body=%s", failed.Code, failed.Body)
	}
	if strings.Contains(failed.Body.String(), "cms-heading-1") {
		t.Fatalf("invalid document leaked into public render: %s", failed.Body)
	}
}

func TestEditorJSONSaveRejectsCrossOriginRequests(t *testing.T) {
	t.Parallel()
	harness := newEditorAPIHarness(t)
	harness.createDraft("welcome", "Welcome", headingDocument)

	request := harness.request(http.MethodPut, "/admin/content/pages/welcome/draft", savePayload(t, "Welcome", headingDocument, 1))
	request.Header.Set("Origin", "https://attacker.test")
	response := harness.do(request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-origin save = %d, want 403; body=%s", response.Code, response.Body)
	}
}
