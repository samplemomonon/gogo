package render

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/a-h/templ"
)

func writeComponent(text string) templ.Component {
	return templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		_, err := io.WriteString(w, text)
		return err
	})
}

func TestHTMLRendersComponentWithDefaultStatus(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	if err := HTML(rec, req, writeComponent("<p>hello</p>")); err != nil {
		t.Fatalf("HTML returned error: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if contentType := rec.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", contentType)
	}
	if !strings.Contains(rec.Body.String(), "<p>hello</p>") {
		t.Errorf("body = %q, want the rendered component", rec.Body.String())
	}
}

func TestHTMLStatusSetsStatusAndContentType(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	if err := HTMLStatus(rec, req, writeComponent("gone"), http.StatusNotFound); err != nil {
		t.Fatalf("HTMLStatus returned error: %v", err)
	}

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if contentType := rec.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", contentType)
	}
}

func TestRespondNavigationServesFullPageForPlainRequests(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	err := Respond(rec, req, Strategy{
		Kind:     KindNavigation,
		Page:     writeComponent("<html>page</html>"),
		Fragment: writeComponent("<section>fragment</section>"),
	})
	if err != nil {
		t.Fatalf("Respond returned error: %v", err)
	}

	if !strings.Contains(rec.Body.String(), "<html>page</html>") {
		t.Errorf("plain request should receive the full page, got %q", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "fragment") {
		t.Errorf("plain request must not receive the fragment")
	}
}

func TestRespondNavigationServesFragmentForHTMX(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("HX-Request", "true")

	err := Respond(rec, req, Strategy{
		Kind:     KindNavigation,
		Page:     writeComponent("<html>page</html>"),
		Fragment: writeComponent("<section>region</section>"),
	})
	if err != nil {
		t.Fatalf("Respond returned error: %v", err)
	}

	if !strings.Contains(rec.Body.String(), "<section>region</section>") {
		t.Errorf("htmx request should receive the fragment, got %q", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "<html>page</html>") {
		t.Errorf("htmx request must not receive the full page")
	}
}

func TestRespondMutationRedirectsForPlainRequests(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin", nil)

	err := Respond(rec, req, Strategy{
		Kind:     KindMutation,
		Fragment: writeComponent("<tr>row</tr>"),
		Redirect: "/admin",
	})
	if err != nil {
		t.Fatalf("Respond returned error: %v", err)
	}

	if rec.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", rec.Code)
	}
	if location := rec.Header().Get("Location"); location != "/admin" {
		t.Errorf("Location = %q, want /admin", location)
	}
	if strings.Contains(rec.Body.String(), "<tr>row</tr>") {
		t.Errorf("plain mutation must not render the fragment")
	}
}

func TestRespondMutationServesFragmentForHTMX(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin", nil)
	req.Header.Set("HX-Request", "true")

	err := Respond(rec, req, Strategy{
		Kind:     KindMutation,
		Fragment: writeComponent("<tr>row</tr>"),
		Redirect: "/admin",
	})
	if err != nil {
		t.Fatalf("Respond returned error: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<tr>row</tr>") {
		t.Errorf("htmx mutation should receive the fragment, got %q", rec.Body.String())
	}
}

func TestRespondRejectsUnknownKind(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	err := Respond(rec, req, Strategy{Kind: Kind(99), Fragment: writeComponent("x")})
	if err == nil {
		t.Fatal("Respond should return an error for an unknown kind")
	}
	if !strings.Contains(err.Error(), "unknown strategy kind") {
		t.Errorf("error = %v, want an unknown-kind message", err)
	}
}
