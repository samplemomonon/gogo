package ui

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
)

type readerFunc func(context.Context, string) (Page, error)

func (f readerFunc) ReadPublished(ctx context.Context, slug string) (Page, error) {
	return f(ctx, slug)
}

func TestPageHandlerRendersSSRPublishedPage(t *testing.T) {
	t.Parallel()
	router := chi.NewRouter()
	handler := NewPageHandler(readerFunc(func(_ context.Context, slug string) (Page, error) {
		if slug != "faq" {
			t.Fatalf("slug = %q, want faq", slug)
		}
		return Page{Slug: "faq", Title: "FAQ & bantuan", Blocks: []Block{{Type: "markdown", Body: "Jawaban **tersedia**"}}}, nil
	}), slog.New(slog.NewTextHandler(io.Discard, nil)))
	handler.RegisterRoutes(router)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/pages/faq", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusOK, response.Body)
	}
	if got := response.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", got)
	}
	if !strings.Contains(response.Body.String(), "<strong>tersedia</strong>") || !strings.Contains(response.Body.String(), "FAQ &amp; bantuan") {
		t.Fatalf("server-rendered content missing or unescaped: %s", response.Body)
	}
}

func TestPageHandlerFailsClosedWhenCMSUnavailable(t *testing.T) {
	t.Parallel()
	router := chi.NewRouter()
	handler := NewPageHandler(readerFunc(func(context.Context, string) (Page, error) {
		return Page{}, errors.New("private upstream detail")
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
	if _, err := DisabledReader().ReadPublished(context.Background(), "faq"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ReadPublished() error = %v, want ErrNotFound", err)
	}
}
