package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"
	"github.com/yuin/goldmark"

	"gogo/internal/features/contentcms/application/ports"
	"gogo/internal/features/contentcms/domain"
	"gogo/internal/features/contentcms/presentation/pages"
	"gogo/internal/shared/tenant"
	"gogo/internal/shared/web/navigation"
	"gogo/internal/shared/web/render"
	"gogo/internal/shared/web/session"
	shellsite "gogo/internal/shared/web/site"
)

type PageHandler struct {
	reader     ports.Reader
	store      ports.PageStore
	siteStore  ports.SiteStore
	siteCache  *publishedSiteCache
	authorizer ports.PageAuthorizer
	logger     *slog.Logger
}

func NewPageHandler(reader ports.Reader, logger *slog.Logger) *PageHandler {
	return NewPageHandlerWithNative(reader, DisabledPageStore(), nil, logger)
}

func NewPageHandlerWithNative(reader ports.Reader, store ports.PageStore, authorizer ports.PageAuthorizer, logger *slog.Logger) *PageHandler {
	return NewPageHandlerWithSite(reader, store, DisabledSiteStore(), authorizer, logger)
}

// NewPageHandlerWithSite wires the native page store and the global shell
// store. The shell's optional SiteSettingsProvider reads the published shell;
// the native editor writes drafts under the same authorizer.
func NewPageHandlerWithSite(reader ports.Reader, store ports.PageStore, siteStore ports.SiteStore, authorizer ports.PageAuthorizer, logger *slog.Logger) *PageHandler {
	if siteStore == nil {
		siteStore = DisabledSiteStore()
	}
	return &PageHandler{
		reader: reader, store: store, siteStore: siteStore,
		siteCache: newPublishedSiteCache(), authorizer: authorizer, logger: logger,
	}
}

func (h *PageHandler) RegisterRoutes(r chi.Router) {
	r.Get("/pages/{slug}", h.handle)
	r.Post("/pages/{slug}/forms/{component}", h.submitPublicForm)

	r.With(h.requireEditor, privateNoStore).Get("/admin/content/pages", h.listPages)
	r.With(h.requireEditor, privateNoStore).Post("/admin/content/pages", h.createPage)
	r.With(h.requireEditor, privateNoStore).Get("/admin/content/pages/", h.listPages)
	r.With(h.requireEditor, privateNoStore).Post("/admin/content/pages/", h.createPage)
	r.With(h.requireEditor, privateNoStore).Get("/admin/content/pages/{slug}", h.editPage)
	r.With(h.requireEditor, privateNoStore).Post("/admin/content/pages/{slug}", h.saveDraftForm)
	r.With(h.requireEditor, privateNoStore).Get("/admin/content/pages/{slug}/draft", h.getDraftJSON)
	r.With(h.requireEditor, privateNoStore).Put("/admin/content/pages/{slug}/draft", h.saveDraftJSON)
	r.With(h.requireEditor, privateNoStore).Post("/admin/content/pages/{slug}/publish", h.publishPage)
	r.With(h.requireEditor, privateNoStore).Get("/admin/content/pages/{slug}/revisions", h.listRevisions)
	r.With(h.requireEditor, privateNoStore).Post("/admin/content/pages/{slug}/revisions/{revision}/restore", h.restoreRevision)
	r.With(h.requireEditor, privateNoStore).Get("/admin/content/sections", h.listSections)
	r.With(h.requireEditor, privateNoStore).Post("/admin/content/sections", h.createSection)
	r.With(h.requireEditor, privateNoStore).Delete("/admin/content/sections/{id}", h.deleteSection)
	r.With(h.requireEditor, privateNoStore).Get("/admin/content/site", h.editSite)
	r.With(h.requireEditor, privateNoStore).Post("/admin/content/site/draft", h.saveSiteDraftForm)
	r.With(h.requireEditor, privateNoStore).Post("/admin/content/site/publish", h.publishSiteForm)
	r.With(h.requireEditor, privateNoStore).Get("/preview/pages/{slug}", h.previewDraft)
	r.With(h.requireEditor, privateNoStore).Get("/preview/site", h.previewSite)
}

func (*PageHandler) Nav() []navigation.Item {
	return []navigation.Item{
		{Label: "Pages", Path: "/admin/content/pages"},
		{Label: "Site", Path: "/admin/content/site"},
	}
}

func privateNoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		next.ServeHTTP(w, r)
	})
}

func (h *PageHandler) requireEditor(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.authorizer == nil {
			h.editorUnavailable(w, r, http.StatusServiceUnavailable, "Content authoring is unavailable")
			return
		}
		sessionID := session.Read(r)
		user, err := h.authorizer.Current(r.Context(), sessionID)
		if err != nil {
			h.logger.Error("content editor session validation failed", "error", err)
			h.editorUnavailable(w, r, http.StatusServiceUnavailable, "Authentication unavailable")
			return
		}
		if user == nil || user.ID == "" {
			if wantsJSON(r) {
				writeProblem(w, http.StatusUnauthorized, "unauthenticated", "Sign in to continue.", nil, 0)
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if user.TenantID != tenant.FromContextOrDefault(r.Context()).String() {
			h.editorUnavailable(w, r, http.StatusForbidden, "Forbidden")
			return
		}
		allowed, err := h.authorizer.CanManage(r.Context(), sessionID)
		if err != nil {
			h.logger.Error("content editor permission check failed", "error", err)
			h.editorUnavailable(w, r, http.StatusServiceUnavailable, "Authorization unavailable")
			return
		}
		if !allowed {
			h.editorUnavailable(w, r, http.StatusForbidden, "Forbidden")
			return
		}
		ctx := context.WithValue(r.Context(), editorUserKey{}, user.ID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (h *PageHandler) editorUnavailable(w http.ResponseWriter, r *http.Request, status int, message string) {
	if wantsJSON(r) {
		code := "unavailable"
		if status == http.StatusForbidden {
			code = "forbidden"
		}
		writeProblem(w, status, code, message, nil, 0)
		return
	}
	http.Error(w, message, status)
}

type editorUserKey struct{}

func editorActor(r *http.Request) string {
	actor, _ := r.Context().Value(editorUserKey{}).(string)
	return actor
}

func wantsJSON(r *http.Request) bool {
	if r.Method == http.MethodPut || r.Method == http.MethodDelete || r.Method == http.MethodPatch {
		return true
	}
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		return true
	}
	if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		return true
	}
	path := r.URL.Path
	return strings.HasSuffix(path, "/draft") || strings.HasSuffix(path, "/revisions") ||
		strings.Contains(path, "/revisions/") || path == "/admin/content/sections"
}

func (h *PageHandler) listPages(w http.ResponseWriter, r *http.Request) {
	pagesList, err := h.store.List(r.Context(), tenant.FromContextOrDefault(r.Context()).String())
	if err != nil {
		h.editorStoreError(w, r, "list content pages", err)
		return
	}
	if err := render.HTML(w, r, pages.PageList(pagesList, "")); err != nil {
		h.logger.Error("render content page list", "error", err)
	}
}

func (h *PageHandler) createPage(w http.ResponseWriter, r *http.Request) {
	if !sameOriginRequest(r) {
		h.editorUnavailable(w, r, http.StatusForbidden, "Invalid request origin")
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}
	tenantID := tenant.FromContextOrDefault(r.Context()).String()
	slug, title := strings.TrimSpace(r.PostFormValue("slug")), strings.TrimSpace(r.PostFormValue("title"))
	page, err := h.store.CreateDraft(r.Context(), tenantID, slug, title, domain.Document{SchemaVersion: domain.SchemaVersion, Components: []domain.Component{}}, editorActor(r))
	if err != nil {
		switch {
		case errors.Is(err, ports.ErrAlreadyExists):
			http.Error(w, "A page with this slug already exists", http.StatusConflict)
		case errors.Is(err, domain.ErrInvalidData):
			http.Error(w, "Check the page title and slug", http.StatusBadRequest)
		default:
			h.editorStoreError(w, r, "create content page", err)
		}
		return
	}
	location := "/admin/content/pages/" + page.Slug
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", location)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, location, http.StatusSeeOther)
}

func (h *PageHandler) editPage(w http.ResponseWriter, r *http.Request) {
	tenantID := tenant.FromContextOrDefault(r.Context()).String()
	page, err := h.store.ReadDraft(r.Context(), tenantID, chi.URLParam(r, "slug"))
	if err != nil {
		h.editorStoreError(w, r, "read content page draft", err)
		return
	}
	sections, err := h.store.ListSections(r.Context(), tenantID)
	if err != nil {
		h.logger.Warn("content sections unavailable in editor", "error", err)
		sections = nil
	}
	bootstrap := pages.EditorBootstrap{
		Page: pages.EditorPage{
			Slug: page.Slug, Title: page.Title, Status: page.Status, Revision: page.Revision,
			UpdatedAt: page.UpdatedAt, PublishedAt: page.PublishedAt,
		},
		Document:    page.Document,
		Definitions: domain.ComponentDefinitions,
		Categories:  domain.Categories,
		Presets:     domain.BuiltinPresets(),
		Sections:    sections,
		DataSources: domain.ApprovedDataSources,
		Values:      domain.BindingValuesWithSite(page.Title, page.Slug, page.TenantID, shellsite.FromContext(r.Context()).SiteName),
		Endpoints: pages.EditorEndpoints{
			Draft:     "/admin/content/pages/" + page.Slug + "/draft",
			Save:      "/admin/content/pages/" + page.Slug + "/draft",
			Publish:   "/admin/content/pages/" + page.Slug + "/publish",
			Revisions: "/admin/content/pages/" + page.Slug + "/revisions",
			Restore:   "/admin/content/pages/" + page.Slug + "/revisions/{revision}/restore",
			Sections:  "/admin/content/sections",
			Preview:   "/preview/pages/" + page.Slug,
			Public:    "/pages/" + page.Slug,
		},
		Diagnostics: domain.ValidationErrors(page.Document.Validate()),
		Breakpoints: []pages.EditorBreakpoint{
			{ID: domain.BreakpointDesktop, Label: "Desktop", Width: 1024},
			{ID: domain.BreakpointTablet, Label: "Tablet", Width: 768},
			{ID: domain.BreakpointMobile, Label: "Mobile", Width: 390},
		},
	}
	w.Header().Set("ETag", revisionETag(page.Revision))
	if err := render.HTML(w, r, pages.Editor(bootstrap)); err != nil {
		h.logger.Error("render content page editor", "slug", page.Slug, "error", err)
	}
}

func (h *PageHandler) previewDraft(w http.ResponseWriter, r *http.Request) {
	page, err := h.store.ReadDraft(r.Context(), tenant.FromContextOrDefault(r.Context()).String(), chi.URLParam(r, "slug"))
	if err != nil {
		h.editorStoreError(w, r, "read content page preview", err)
		return
	}
	if err := render.HTML(w, r, pages.DraftPreview(page)); err != nil {
		h.logger.Error("render content page preview", "slug", page.Slug, "error", err)
	}
}

func sameOriginRequest(r *http.Request) bool {
	rawOrigin := r.Header.Get("Origin")
	origin, err := url.Parse(rawOrigin)
	if err != nil || origin.User != nil || origin.Host == "" || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return false
	}
	if origin.Scheme != "http" && origin.Scheme != "https" {
		return false
	}
	originHost := normalizedHost(origin.Host, origin.Scheme)
	requestScheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		requestScheme = "https"
	}
	return originHost != "" && originHost == normalizedHost(r.Host, requestScheme)
}

func normalizedHost(host, scheme string) string {
	parsed, err := url.Parse("//" + host)
	if err != nil || parsed.Hostname() == "" {
		return ""
	}
	port := parsed.Port()
	if port == "" {
		if scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return strings.ToLower(parsed.Hostname()) + ":" + port
}

func (h *PageHandler) editorStoreError(w http.ResponseWriter, r *http.Request, operation string, err error) {
	if errors.Is(err, ports.ErrNotFound) {
		if wantsJSON(r) {
			writeProblem(w, http.StatusNotFound, "not_found", "Page not found.", nil, 0)
			return
		}
		http.NotFound(w, r)
		return
	}
	h.logger.Error(operation, "error", err)
	if wantsJSON(r) {
		writeProblem(w, http.StatusServiceUnavailable, "unavailable", "Content pages are temporarily unavailable.", nil, 0)
		return
	}
	http.Error(w, "Content pages are temporarily unavailable", http.StatusServiceUnavailable)
}

func (h *PageHandler) writeEditorStatus(w http.ResponseWriter, r *http.Request, message, publishedURL string) {
	if err := render.HTML(w, r, pages.OperationStatus(message, publishedURL)); err != nil {
		h.logger.Error("render content editor status", "error", err)
	}
}

// handle reads only published documents. A malformed persisted document is a
// server-side failure, never a reason to render a diagnostic to the public.
func (h *PageHandler) handle(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	if h.serveNativePublished(w, r, slug) {
		return
	}
	if h.reader == nil {
		http.NotFound(w, r)
		return
	}
	page, err := h.reader.ReadPublished(r.Context(), slug)
	switch {
	case errors.Is(err, ports.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		h.logger.Error("editorial page unavailable", "slug", slug, "error", err)
		http.Error(w, "Halaman konten sementara tidak tersedia.", http.StatusServiceUnavailable)
		return
	}
	view, err := renderPage(page)
	if err != nil {
		h.logger.Error("editorial page rendering failed", "slug", page.Slug, "error", err)
		http.Error(w, "Halaman konten sementara tidak tersedia.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", publicCacheControl(r))
	if err := h.renderLegacyPage(w, r, view); err != nil {
		h.logger.Error("editorial page response failed", "slug", page.Slug, "error", err)
	}
}

// renderLegacyPage preserves the existing Builder.io block rendering while
// placing it inside the shared CMS-managed shell. The html/template output is
// already escaped; it is the trusted body passed to the shell.
func (h *PageHandler) renderLegacyPage(w http.ResponseWriter, r *http.Request, view renderedPage) error {
	var body bytes.Buffer
	if err := pageTemplate.Execute(&body, view); err != nil {
		return err
	}
	return render.HTML(w, r, pages.LegacyPage(view.Title, templ.Raw(body.String())))
}

// serveNativePublished renders a published native page when one exists. It
// reports whether it handled the response; a missing page returns false so the
// caller can fall back to the legacy editorial reader or the manual homepage.
func (h *PageHandler) serveNativePublished(w http.ResponseWriter, r *http.Request, slug string) bool {
	if h.store == nil {
		return false
	}
	page, err := h.store.ReadPublished(r.Context(), tenant.FromContextOrDefault(r.Context()).String(), slug)
	switch {
	case err == nil:
		if err := page.Document.ValidateForPublish(); err != nil {
			h.logger.Error("refusing to render invalid published document", "slug", slug, "error", err)
			http.Error(w, "Halaman konten sementara tidak tersedia.", http.StatusInternalServerError)
			return true
		}
		w.Header().Set("Cache-Control", publicCacheControl(r))
		notice := formNoticeForPage(r, page.Document.Components)
		if err := render.HTML(w, r, pages.PublicNativePageNotice(page, notice)); err != nil {
			h.logger.Error("render native published page", "slug", slug, "error", err)
		}
		return true
	case errors.Is(err, ports.ErrNotFound), errors.Is(err, ErrNativeStoreUnavailable):
		return false
	default:
		h.logger.Error("native published page unavailable", "slug", slug, "error", err)
		http.Error(w, "Halaman konten sementara tidak tersedia.", http.StatusServiceUnavailable)
		return true
	}
}

func publicCacheControl(r *http.Request) string {
	if formNotice(r) != "" {
		return "no-store"
	}
	return "public, max-age=60, stale-while-revalidate=30"
}

func formNotice(r *http.Request) string {
	return formNoticeForPage(r, nil)
}

// formNoticeForPage resolves the success copy from the published document so
// a page can customize it without trusting a query-string message. The formId
// only selects an approved form component; unknown values fall back to the
// generic confirmation.
func formNoticeForPage(r *http.Request, components []domain.Component) string {
	switch r.URL.Query().Get("form") {
	case "received":
		if formID := strings.TrimSpace(r.URL.Query().Get("formId")); formID != "" {
			if component, ok := findFormComponent(components, formID); ok {
				if message, _ := component.Props["successMessage"].(string); strings.TrimSpace(message) != "" {
					return strings.TrimSpace(message)
				}
			}
		}
		return "Your message was received."
	case "invalid":
		return "Check the required fields and try again."
	default:
		return ""
	}
}

var emailPattern = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// submitPublicForm stores a submission for a form component in the published
// document. Fields, names, and types come from the published component, never
// from the request body.
func (h *PageHandler) submitPublicForm(w http.ResponseWriter, r *http.Request) {
	if !sameOriginRequest(r) {
		http.Error(w, "Invalid request origin", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form submission", http.StatusBadRequest)
		return
	}
	slug := chi.URLParam(r, "slug")
	tenantID := tenant.FromContextOrDefault(r.Context()).String()
	page, err := h.store.ReadPublished(r.Context(), tenantID, slug)
	if err != nil {
		if errors.Is(err, ports.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		h.logger.Error("read page for form submission", "slug", slug, "error", err)
		http.Error(w, "Form submissions are temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	component, ok := findFormComponent(page.Document.Components, chi.URLParam(r, "component"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	values, valid := validatedFormValues(component, r.PostForm)
	if !valid {
		http.Redirect(w, r, "/pages/"+slug+"?form=invalid#form-"+component.ID, http.StatusSeeOther)
		return
	}
	if err := h.store.SubmitForm(r.Context(), tenantID, slug, page.Revision, component.ID, values); err != nil {
		h.logger.Error("store public form submission", "slug", slug, "component", component.ID, "error", err)
		http.Error(w, "Form submissions are temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	http.Redirect(w, r, "/pages/"+slug+"?form=received&formId="+url.QueryEscape(component.ID)+"#form-"+component.ID, http.StatusSeeOther)
}

func findFormComponent(components []domain.Component, id string) (domain.Component, bool) {
	for _, component := range components {
		if component.ID == id && component.Type == "form" {
			return component, true
		}
		if found, ok := findFormComponent(component.Children, id); ok {
			return found, true
		}
	}
	return domain.Component{}, false
}

func validatedFormValues(component domain.Component, form url.Values) (map[string]string, bool) {
	fields, _ := component.Props["fields"].([]any)
	values := make(map[string]string, len(fields))
	for _, raw := range fields {
		field, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name, _ := field["name"].(string)
		if name == "" {
			continue
		}
		value := form.Get(name)
		required, _ := field["required"].(bool)
		if required && strings.TrimSpace(value) == "" {
			return nil, false
		}
		if len(value) > 2_000 {
			return nil, false
		}
		if fieldType, _ := field["type"].(string); fieldType == "email" && value != "" && !emailPattern.MatchString(value) {
			return nil, false
		}
		values[name] = value
	}
	return values, len(values) > 0
}

func (h *PageHandler) handleDraftError(w http.ResponseWriter, r *http.Request, operation string, err error) {
	switch {
	case errors.Is(err, ports.ErrNotFound):
		h.editorStoreError(w, r, operation, err)
	case errors.Is(err, ports.ErrConflict):
		http.Error(w, "This page changed in another session. Reload before saving.", http.StatusConflict)
	case errors.Is(err, domain.ErrInvalidData):
		http.Error(w, "The page data is invalid", http.StatusUnprocessableEntity)
	default:
		h.editorStoreError(w, r, operation, err)
	}
}

func decodeDocument(raw string) (domain.Document, error) {
	return decodeDocumentBytes([]byte(raw))
}

func decodeDocumentBytes(raw []byte) (domain.Document, error) {
	if len(raw) == 0 || len(raw) > domain.MaxDocumentBytes+16_000 {
		return domain.Document{}, domain.ErrInvalidDocument
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	var document domain.Document
	if err := decoder.Decode(&document); err != nil {
		return domain.Document{}, fmt.Errorf("decode document: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return domain.Document{}, errors.New("document contains trailing data")
	}
	normalized, err := document.Normalized()
	if err != nil {
		return domain.Document{}, err
	}
	if err := normalized.Validate(); err != nil {
		return domain.Document{}, err
	}
	return normalized, nil
}

func revisionETag(revision int64) string {
	return `"` + strconv.FormatInt(revision, 10) + `"`
}

func parseRevisionETag(raw string) (int64, bool) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "W/")
	raw = strings.Trim(raw, `"`)
	revision, err := strconv.ParseInt(raw, 10, 64)
	return revision, err == nil && revision > 0
}

// Rendered editorial blocks from the Builder.io read path.
type renderedPage struct {
	Title  string
	Blocks []renderedBlock
}

type renderedBlock struct {
	Type     string
	Title    string
	Body     template.HTML
	ImageURL string
	ImageAlt string
	Label    string
	Href     string
	Items    []renderedFAQItem
}

type renderedFAQItem struct {
	Question string
	Answer   template.HTML
}

func renderPage(page ports.EditorialPage) (renderedPage, error) {
	if err := page.Validate(); err != nil {
		return renderedPage{}, err
	}
	view := renderedPage{Title: page.Title, Blocks: make([]renderedBlock, 0, len(page.EditorialBlocks))}
	for _, block := range page.EditorialBlocks {
		item := renderedBlock{
			Type: block.Type, Title: block.Title, ImageURL: block.ImageURL,
			ImageAlt: block.ImageAlt, Label: block.Label, Href: block.Href,
		}
		if block.Type == "markdown" {
			html, err := markdownHTML(block.Body)
			if err != nil {
				return renderedPage{}, err
			}
			item.Body = html
		}
		if block.Type == "faq" {
			item.Items = make([]renderedFAQItem, 0, len(block.Items))
			for _, faq := range block.Items {
				answer, err := markdownHTML(faq.Answer)
				if err != nil {
					return renderedPage{}, err
				}
				item.Items = append(item.Items, renderedFAQItem{Question: faq.Question, Answer: answer})
			}
		}
		view.Blocks = append(view.Blocks, item)
	}
	return view, nil
}

func markdownHTML(source string) (template.HTML, error) {
	var output bytes.Buffer
	if err := goldmark.New().Convert([]byte(source), &output); err != nil {
		return "", err
	}
	return template.HTML(output.String()), nil //nolint:gosec // output is generated by the safe Goldmark renderer above
}

var pageTemplate = template.Must(template.New("editorial-page-body").Parse(`<article class="space-y-8"><header class="rounded-2xl border border-base-300 bg-base-100 p-6 shadow-sm sm:p-10"><h1 class="text-3xl font-bold tracking-tight sm:text-4xl">{{.Title}}</h1></header>{{range .Blocks}}<section class="rounded-2xl border border-base-300 bg-base-100 p-6 shadow-sm sm:p-8">{{if .Title}}<h2 class="mb-5 text-2xl font-semibold">{{end}}
{{if or (eq .Type "hero") (eq .Type "banner")}}{{if .ImageURL}}<img class="mb-5 max-h-96 w-full rounded-xl object-cover" src="{{.ImageURL}}" alt="{{.ImageAlt}}">{{end}}{{end}}
{{if eq .Type "markdown"}}<div class="space-y-4 leading-7">{{.Body}}</div>{{end}}
{{if eq .Type "faq"}}<div class="space-y-3">{{range .Items}}<details class="collapse collapse-arrow rounded-xl border border-base-300 bg-base-100"><summary class="collapse-title font-semibold">{{.Question}}</summary><div class="collapse-content leading-7">{{.Answer}}</div></details>{{end}}</div>{{end}}
{{if eq .Type "cta"}}<p><a class="btn btn-primary" href="{{.Href}}">{{.Label}}</a></p>{{end}}</section>{{end}}</article>`))

var _ interface {
	RegisterRoutes(chi.Router)
	Nav() []navigation.Item
} = (*PageHandler)(nil)
