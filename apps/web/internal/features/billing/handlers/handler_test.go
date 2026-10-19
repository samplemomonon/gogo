package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"gogo/internal/features/billing/activity"
	"gogo/internal/shared/events"
	"gogo/internal/shared/productevents"
)

func newTestHandler(t *testing.T) http.Handler {
	t.Helper()

	feed := activity.New(10)
	feed.OnEvent(context.Background(), events.Event{
		Type:    productevents.ProductCreated,
		Payload: productevents.Payload{ID: "42"},
	})

	r := chi.NewRouter()
	New(feed).RegisterRoutes(r)
	return r
}

func get(t *testing.T, h http.Handler, target string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, target, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestIndexServesTheFullDocumentWithoutHTMX(t *testing.T) {
	t.Parallel()

	rec := get(t, newTestHandler(t), "/billing", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /billing = %d, want 200", rec.Code)
	}
	html := rec.Body.String()
	for _, want := range []string{"<!doctype html>", "Tagihan", `id="billing-region"`} {
		if !strings.Contains(html, want) {
			t.Errorf("full document missing %q", want)
		}
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", got)
	}
}

// TestIndexServesTheFragmentForHTMX pins the negotiated representation: the
// same URL must yield only the fragment for an in-page swap. A handler that
// shipped the shell into an hx-target would nest a whole document inside the
// page.
func TestIndexServesTheFragmentForHTMX(t *testing.T) {
	t.Parallel()

	for name, headers := range map[string]map[string]string{
		"legacy boolean": {"HX-Request": "true"},
		"typed partial":  {"HX-Request-Type": "partial"},
	} {
		headers := headers
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rec := get(t, newTestHandler(t), "/billing", headers)

			if rec.Code != http.StatusOK {
				t.Fatalf("GET /billing (HTMX) = %d, want 200", rec.Code)
			}
			html := rec.Body.String()
			if strings.Contains(html, "<!doctype html>") {
				t.Error("an HTMX request received the full document; it must get the bare fragment")
			}
			if !strings.Contains(html, `id="billing-fragment"`) {
				t.Error("the HTMX response is missing the fragment region")
			}
		})
	}
}

// TestIndexSetsVaryForTheSharedCache guards the caching contract from
// DECISION_UI_OWNERSHIP §6: with two representations on one URL, a shared
// cache that does not see Vary is free to hand a fragment to a navigation.
func TestIndexSetsVaryForTheSharedCache(t *testing.T) {
	t.Parallel()

	rec := get(t, newTestHandler(t), "/billing", nil)

	vary := strings.Join(rec.Header().Values("Vary"), ", ")
	for _, want := range []string{"HX-Request", "HX-Request-Type"} {
		if !strings.Contains(vary, want) {
			t.Errorf("Vary = %q, want it to include %s", vary, want)
		}
	}
}

func TestFragmentEndpoint(t *testing.T) {
	t.Parallel()

	rec := get(t, newTestHandler(t), "/billing/fragment", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /billing/fragment = %d, want 200", rec.Code)
	}
	html := rec.Body.String()
	if !strings.Contains(html, `id="billing-fragment"`) || !strings.Contains(html, "42") {
		t.Errorf("fragment response missing the feed content: %q", html)
	}
	if strings.Contains(html, "<!doctype html>") {
		t.Error("the fragment endpoint must never render the document shell")
	}
}
