package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"gogo/internal/features/contentcms/application/ports"
	"gogo/internal/features/contentcms/domain"
	"gogo/internal/features/contentcms/presentation/pages"
	"gogo/internal/shared/tenant"
	"gogo/internal/shared/web/render"
	shellsite "gogo/internal/shared/web/site"
)

const publishedSiteCacheTTL = 30 * time.Second

// publishedSiteCache prevents the gateway's per-request shell middleware from
// turning every HTML response into a settings query. Publishing invalidates this
// process's entry immediately; the short TTL bounds staleness when another
// replica published.
type publishedSiteCache struct {
	mu      sync.Mutex
	entries map[string]publishedSiteEntry
}

type publishedSiteEntry struct {
	settings shellsite.Settings
	expires  time.Time
}

func newPublishedSiteCache() *publishedSiteCache {
	return &publishedSiteCache{entries: make(map[string]publishedSiteEntry)}
}

func (c *publishedSiteCache) get(tenantID string) (shellsite.Settings, bool) {
	if c == nil {
		return shellsite.Settings{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[tenantID]
	if !ok || time.Now().After(entry.expires) {
		if ok {
			delete(c.entries, tenantID)
		}
		return shellsite.Settings{}, false
	}
	return entry.settings, true
}

func (c *publishedSiteCache) set(tenantID string, settings shellsite.Settings) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[tenantID] = publishedSiteEntry{settings: settings, expires: time.Now().Add(publishedSiteCacheTTL)}
}

func (c *publishedSiteCache) invalidate(tenantID string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, tenantID)
}

// SiteSettings implements the gateway's optional SiteSettingsProvider. It
// resolves only the published shell, so a draft save can never leak into the
// public header, footer, menu, or theme.
func (h *PageHandler) SiteSettings(ctx context.Context) (shellsite.Settings, error) {
	if h.siteStore == nil {
		return shellsite.Default(), nil
	}
	tenantID := tenant.FromContextOrDefault(ctx).String()
	if settings, ok := h.siteCache.get(tenantID); ok {
		return settings, nil
	}
	snapshot, err := h.siteStore.ReadSitePublished(ctx, tenantID)
	if err != nil {
		return shellsite.Default(), err
	}
	if err := snapshot.Settings.Validate(); err != nil {
		return shellsite.Default(), fmt.Errorf("published site settings failed validation: %w", err)
	}
	settings := toShellSettings(snapshot.Settings)
	h.siteCache.set(tenantID, settings)
	return settings, nil
}

// ServeHome renders the tenant's published home page when it exists. The shell
// home dispatcher calls it before falling back to the manual storefront.
func (h *PageHandler) ServeHome(w http.ResponseWriter, r *http.Request) bool {
	slug := shellsite.FromContext(r.Context()).HomePageSlug
	if strings.TrimSpace(slug) == "" {
		slug = domain.DefaultSiteSettings().HomePageSlug
	}
	return h.serveNativePublished(w, r, slug)
}

func (h *PageHandler) editSite(w http.ResponseWriter, r *http.Request) {
	tenantID := tenant.FromContextOrDefault(r.Context()).String()
	snapshot, err := h.siteStore.ReadSiteDraft(r.Context(), tenantID)
	if err != nil {
		h.siteStoreError(w, r, "read site draft", err)
		return
	}
	if err := snapshot.Settings.Validate(); err != nil {
		h.logger.Error("refusing to render invalid site draft", "error", err)
		http.Error(w, "Site settings are temporarily unavailable", http.StatusInternalServerError)
		return
	}
	data := pages.SiteEditorData{
		Settings:    snapshot.Settings,
		HeaderLinks: formatSiteLinks(snapshot.Settings.HeaderLinks),
		FooterLinks: formatSiteLinks(snapshot.Settings.FooterLinks),
		Revision:    snapshot.Revision,
		Status:      snapshot.Status,
		UpdatedAt:   snapshot.UpdatedAt,
		PublishedAt: snapshot.PublishedAt,
		Saved:       r.URL.Query().Get("saved") == "1",
		Published:   r.URL.Query().Get("published") == "1",
		PreviewURL:  "/preview/site",
		PublicURL:   "/",
	}
	if err := render.HTML(w, r, pages.SiteEditor(data)); err != nil {
		h.logger.Error("render site editor", "error", err)
	}
}

func (h *PageHandler) saveSiteDraftForm(w http.ResponseWriter, r *http.Request) {
	if !sameOriginRequest(r) {
		http.Error(w, "Invalid request origin", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}
	input, err := siteFormInputFromRequest(r)
	if err != nil {
		h.renderSiteFormError(w, r, http.StatusUnprocessableEntity, err.Error(), input)
		return
	}
	revision, err := siteExpectedRevision(r)
	if err != nil {
		h.renderSiteFormError(w, r, http.StatusBadRequest, "Invalid revision", input)
		return
	}
	tenantID := tenant.FromContextOrDefault(r.Context()).String()
	snapshot, err := h.siteStore.SaveSiteDraft(r.Context(), tenantID, input.Settings, revision, editorActor(r))
	if err != nil {
		h.siteSaveError(w, r, "save site draft", input, err)
		return
	}
	h.siteCache.invalidate(tenantID)
	http.Redirect(w, r, "/admin/content/site?saved=1&revision="+strconv.FormatInt(snapshot.Revision, 10), http.StatusSeeOther)
}

func (h *PageHandler) publishSiteForm(w http.ResponseWriter, r *http.Request) {
	if !sameOriginRequest(r) {
		http.Error(w, "Invalid request origin", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}
	revision, err := siteExpectedRevision(r)
	if err != nil {
		http.Error(w, "Invalid revision", http.StatusBadRequest)
		return
	}
	tenantID := tenant.FromContextOrDefault(r.Context()).String()
	if _, err := h.siteStore.PublishSite(r.Context(), tenantID, revision, editorActor(r)); err != nil {
		current, readErr := h.siteStore.ReadSiteDraft(r.Context(), tenantID)
		input := siteFormInput{Settings: domain.DefaultSiteSettings()}
		if readErr == nil {
			input.Settings = current.Settings
			input.HeaderLinksText = formatSiteLinks(current.Settings.HeaderLinks)
			input.FooterLinksText = formatSiteLinks(current.Settings.FooterLinks)
		}
		h.siteSaveError(w, r, "publish site", input, err)
		return
	}
	h.siteCache.invalidate(tenantID)
	http.Redirect(w, r, "/admin/content/site?published=1", http.StatusSeeOther)
}

func (h *PageHandler) previewSite(w http.ResponseWriter, r *http.Request) {
	tenantID := tenant.FromContextOrDefault(r.Context()).String()
	snapshot, err := h.siteStore.ReadSiteDraft(r.Context(), tenantID)
	if err != nil {
		h.siteStoreError(w, r, "read site preview", err)
		return
	}
	if err := snapshot.Settings.Validate(); err != nil {
		h.logger.Error("refusing to preview invalid site draft", "error", err)
		http.Error(w, "Site settings are temporarily unavailable", http.StatusInternalServerError)
		return
	}
	ctx := shellsite.WithSettings(r.Context(), toShellSettings(snapshot.Settings))
	r = r.WithContext(ctx)
	if err := render.HTML(w, r, pages.SitePreview(pages.SitePreviewData{
		Settings: snapshot.Settings,
		Revision: snapshot.Revision,
	})); err != nil {
		h.logger.Error("render site preview", "error", err)
	}
}

func (h *PageHandler) siteStoreError(w http.ResponseWriter, r *http.Request, operation string, err error) {
	if errors.Is(err, ports.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	h.logger.Error(operation, "error", err)
	http.Error(w, "Site settings are temporarily unavailable", http.StatusServiceUnavailable)
}

func (h *PageHandler) siteSaveError(w http.ResponseWriter, r *http.Request, operation string, input siteFormInput, err error) {
	var conflict *ports.ConflictError
	switch {
	case errors.As(err, &conflict):
		h.renderSiteFormError(w, r, http.StatusConflict,
			fmt.Sprintf("These settings changed in another tab (current revision %d). Reload before saving again.", conflict.Current),
			input)
	case errors.Is(err, domain.ErrInvalidData):
		h.renderSiteFormError(w, r, http.StatusUnprocessableEntity, err.Error(), input)
	case errors.Is(err, ports.ErrNotFound):
		h.renderSiteFormError(w, r, http.StatusConflict, "Save a draft before publishing.", input)
	default:
		h.logger.Error(operation, "error", err)
		http.Error(w, "Site settings are temporarily unavailable", http.StatusServiceUnavailable)
	}
}

func (h *PageHandler) renderSiteFormError(w http.ResponseWriter, r *http.Request, status int, message string, input siteFormInput) {
	input.Settings = input.Settings.Normalized()
	data := pages.SiteEditorData{
		Settings:    input.Settings,
		HeaderLinks: input.HeaderLinksText,
		FooterLinks: input.FooterLinksText,
		Revision:    siteFormRevision(r),
		Error:       message,
		PreviewURL:  "/preview/site",
		PublicURL:   "/",
	}
	if err := render.HTMLStatus(w, r, pages.SiteEditor(data), status); err != nil {
		h.logger.Error("render site form error", "error", err)
	}
}

func siteExpectedRevision(r *http.Request) (int64, error) {
	raw := strings.TrimSpace(r.PostFormValue("revision"))
	if raw == "" {
		return 0, nil
	}
	revision, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || revision < 0 {
		return 0, fmt.Errorf("invalid revision")
	}
	return revision, nil
}

func siteFormRevision(r *http.Request) int64 {
	revision, _ := siteExpectedRevision(r)
	return revision
}

// siteFormInput keeps the submitted textarea text alongside the parsed model
// so a validation error re-renders exactly what the editor typed.
type siteFormInput struct {
	Settings        domain.SiteSettings
	HeaderLinksText string
	FooterLinksText string
}

func siteFormInputFromRequest(r *http.Request) (siteFormInput, error) {
	input := siteFormInput{
		HeaderLinksText: r.PostFormValue("headerLinks"),
		FooterLinksText: r.PostFormValue("footerLinks"),
		Settings: domain.SiteSettings{
			SchemaVersion: domain.SiteSchemaVersion,
			SiteName:      strings.TrimSpace(r.PostFormValue("siteName")),
			Tagline:       strings.TrimSpace(r.PostFormValue("tagline")),
			LogoURL:       strings.TrimSpace(r.PostFormValue("logoUrl")),
			LogoAlt:       strings.TrimSpace(r.PostFormValue("logoAlt")),
			HomePageSlug:  strings.TrimSpace(r.PostFormValue("homePageSlug")),
			FooterText:    strings.TrimSpace(r.PostFormValue("footerText")),
			Theme: domain.SiteTheme{
				Primary:   strings.TrimSpace(r.PostFormValue("themePrimary")),
				Secondary: strings.TrimSpace(r.PostFormValue("themeSecondary")),
				Accent:    strings.TrimSpace(r.PostFormValue("themeAccent")),
			},
		},
	}
	headerLinks, err := parseSiteLinks(input.HeaderLinksText)
	if err != nil {
		return input, fmt.Errorf("Header links: %w", err)
	}
	footerLinks, err := parseSiteLinks(input.FooterLinksText)
	if err != nil {
		return input, fmt.Errorf("Footer links: %w", err)
	}
	input.Settings.HeaderLinks = headerLinks
	input.Settings.FooterLinks = footerLinks
	input.Settings = input.Settings.Normalized()
	return input, input.Settings.Validate()
}

func parseSiteLinks(raw string) ([]domain.SiteLink, error) {
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	links := make([]domain.SiteLink, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("write one link per line as Label | /path")
		}
		label, href := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		if label == "" || href == "" {
			return nil, fmt.Errorf("each link needs both a label and a same-origin path")
		}
		links = append(links, domain.SiteLink{Label: label, Href: href})
	}
	return links, nil
}

func formatSiteLinks(links []domain.SiteLink) string {
	lines := make([]string, 0, len(links))
	for _, link := range links {
		lines = append(lines, link.Label+" | "+link.Href)
	}
	return strings.Join(lines, "\n")
}

func toShellSettings(settings domain.SiteSettings) shellsite.Settings {
	settings = settings.Normalized()
	headerLinks := make([]shellsite.Link, 0, len(settings.HeaderLinks))
	for _, link := range settings.HeaderLinks {
		headerLinks = append(headerLinks, shellsite.Link{Label: link.Label, Href: link.Href})
	}
	footerLinks := make([]shellsite.Link, 0, len(settings.FooterLinks))
	for _, link := range settings.FooterLinks {
		footerLinks = append(footerLinks, shellsite.Link{Label: link.Label, Href: link.Href})
	}
	return shellsite.Settings{
		SiteName:     settings.SiteName,
		Tagline:      settings.Tagline,
		LogoURL:      settings.LogoURL,
		LogoAlt:      settings.LogoAlt,
		HomePageSlug: settings.HomePageSlug,
		HeaderLinks:  headerLinks,
		FooterText:   settings.FooterText,
		FooterLinks:  footerLinks,
		Theme: shellsite.Theme{
			Primary:   settings.Theme.Primary,
			Secondary: settings.Theme.Secondary,
			Accent:    settings.Theme.Accent,
		},
	}
}
