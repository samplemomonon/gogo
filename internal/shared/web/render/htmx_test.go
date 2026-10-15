package render

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/a-h/templ"
)

// TestFragmentNegotiation pins the request shapes a render strategy has to
// distinguish. Each case is one way the previous "is HX-Request set?" check
// answered wrongly, or would have.
func TestFragmentNegotiation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		headers map[string]string
		want    bool
		why     string
	}{
		{
			name: "ordinary navigation gets a document",
			want: false,
			why:  "a plain browser navigation must get a full page for bookmarks, history and SEO",
		},
		{
			name:    "legacy htmx request gets a fragment",
			headers: map[string]string{"HX-Request": "true"},
			want:    true,
			why:     "pre-htmx-4 clients send no request type, so the boolean still has to work",
		},
		{
			name:    "explicit partial gets a fragment",
			headers: map[string]string{"HX-Request": "true", "HX-Request-Type": "partial"},
			want:    true,
			why:     "the request type says what shape is wanted",
		},
		{
			name:    "hx-boost navigation gets a document",
			headers: map[string]string{"HX-Request": "true", "HX-Request-Type": "full"},
			want:    false,
			why:     "hx-boost implies hx-target=body; a fragment here injects a bare section into body",
		},
		{
			name:    "history restore gets a document",
			headers: map[string]string{"HX-Request": "true", "HX-History-Restore-Request": "true"},
			want:    false,
			why:     "after a history-cache miss the browser re-requests and expects the whole page",
		},
		{
			name:    "explicit request type beats the boolean",
			headers: map[string]string{"HX-Request": "false", "HX-Request-Type": "partial"},
			want:    true,
			why:     "the two headers disagree; the one describing the wanted shape wins",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := httptest.NewRequest(http.MethodGet, "/", nil)
			for k, v := range tc.headers {
				r.Header.Set(k, v)
			}

			if got := IsFragment(r); got != tc.want {
				t.Errorf("IsFragment = %v, want %v; %s", got, tc.want, tc.why)
			}
		})
	}
}

// TestRespondDeclaresVary is the shared-cache guard.
//
// Respond chooses between two representations of the same URL. A cache in front
// of the gateway that does not key on the request headers will serve one
// variant to a caller expecting the other, and the result is a raw fragment
// rendered as a whole page or the reverse. htmx's docs call Vary mandatory for
// this pattern; without an assertion here, dropping it is invisible until
// somebody puts a CDN in front.
func TestRespondDeclaresVary(t *testing.T) {
	t.Parallel()

	strategy := Strategy{
		Kind:     KindNavigation,
		Page:     htmlComponent("<html></html>"),
		Fragment: htmlComponent("<section></section>"),
		Redirect: "/after",
	}

	for _, name := range []string{"navigation as document", "navigation as fragment"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if name == "navigation as fragment" {
				r.Header.Set("HX-Request", "true")
			}
			rec := httptest.NewRecorder()

			if err := Respond(rec, r, strategy); err != nil {
				t.Fatalf("Respond: %v", err)
			}

			vary := rec.Header().Values("Vary")
			if !contains(vary, headerHXRequest) {
				t.Errorf("Vary = %v, want it to include %s; a shared cache would serve one representation to callers expecting the other",
					vary, headerHXRequest)
			}
			if !contains(vary, headerHXRequestType) {
				t.Errorf("Vary = %v, want it to include %s; hx-boost and explicit request types are part of the decision",
					vary, headerHXRequestType)
			}
		})
	}
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

// htmlComponent builds a ComponentFunc that emits a fixed string, so the test
// exercises the negotiation and headers rather than any template.
func htmlComponent(body string) templ.Component {
	return templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		_, err := io.WriteString(w, body)
		return err
	})
}
