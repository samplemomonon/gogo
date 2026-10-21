package handler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"gogo/internal/features/contentcms/application/ports"
	shellsite "gogo/internal/shared/web/site"
)

type readerFunc func(context.Context, string) (ports.EditorialPage, error)

func (f readerFunc) ReadPublished(ctx context.Context, slug string) (ports.EditorialPage, error) {
	return f(ctx, slug)
}

func TestPageHandlerRendersSSRPublishedPage(t *testing.T) {
	t.Parallel()
	router := chi.NewRouter()
	handler := NewPageHandler(readerFunc(func(_ context.Context, slug string) (ports.EditorialPage, error) {
		if slug != "faq" {
			t.Fatalf("slug = %q, want faq", slug)
		}
		return ports.EditorialPage{Slug: "faq", Title: "FAQ & bantuan", EditorialBlocks: []ports.EditorialBlock{{Type: "markdown", Body: "Jawaban **tersedia**"}}}, nil
	}), slog.New(slog.NewTextHandler(io.Discard, nil)))
	handler.RegisterRoutes(router)

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/pages/faq", nil)
	request = request.WithContext(shellsite.WithSettings(request.Context(), shellsite.Settings{
		SiteName: "Toko Legacy", HomePageSlug: "home", FooterText: "Footer Legacy",
	}))
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusOK, response.Body)
	}
	if got := response.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", got)
	}
	body := response.Body.String()
	if !strings.Contains(body, "<strong>tersedia</strong>") || !strings.Contains(body, "FAQ &amp; bantuan") {
		t.Fatalf("server-rendered content missing or unescaped: %s", response.Body)
	}
	if !strings.Contains(body, "Toko Legacy") || !strings.Contains(body, "Footer Legacy") {
		t.Fatalf("legacy editorial page did not use the shared site shell: %s", response.Body)
	}
}

func TestPageHandlerFailsClosedWhenCMSUnavailable(t *testing.T) {
	t.Parallel()
	router := chi.NewRouter()
	handler := NewPageHandler(readerFunc(func(context.Context, string) (ports.EditorialPage, error) {
		return ports.EditorialPage{}, errors.New("private upstream detail")
	}), slog.New(slog.NewTextHandler(io.Discard, nil)))
	handler.RegisterRoutes(router)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/pages/faq", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", response.Code)
	}
	if strings.Contains(response.Body.String(), "private upstream detail") {
		t.Fatalf("upstream detail leaked to client: %s", response.Body)
	}
}

func TestDisabledReaderReportsPageNotFound(t *testing.T) {
	t.Parallel()
	if _, err := ports.DisabledReader().ReadPublished(context.Background(), "faq"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("ReadPublished() error = %v, want ErrNotFound", err)
	}
}

func TestMarkdownRenderingEscapesRawHTML(t *testing.T) {
	t.Parallel()

	page := ports.EditorialPage{
		Slug: "faq", Title: "FAQ",
		EditorialBlocks: []ports.EditorialBlock{{Type: "markdown", Body: "<script>alert(1)</script>\n\n**aman**"}},
	}
	view, err := renderPage(page)
	if err != nil {
		t.Fatalf("renderPage() error = %v", err)
	}
	body := string(view.Blocks[0].Body)
	if strings.Contains(body, "<script>") {
		t.Fatalf("raw script was rendered: %q", body)
	}
	if !strings.Contains(body, "raw HTML omitted") || !strings.Contains(body, "<strong>aman</strong>") {
		t.Fatalf("Markdown escaping/formatting missing: %q", body)
	}
}

func TestMarkdownRenderingRejectsDangerousLinkSchemes(t *testing.T) {
	t.Parallel()
	page := ports.EditorialPage{Slug: "faq", Title: "FAQ", EditorialBlocks: []ports.EditorialBlock{{Type: "markdown", Body: "[tautan](javascript:alert(1))"}}}
	view, err := renderPage(page)
	if err != nil {
		t.Fatalf("renderPage() error = %v", err)
	}
	if strings.Contains(strings.ToLower(string(view.Blocks[0].Body)), "href=\"javascript:") {
		t.Fatalf("dangerous URL rendered: %q", view.Blocks[0].Body)
	}
}
