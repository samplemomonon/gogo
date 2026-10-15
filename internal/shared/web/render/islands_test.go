package render

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/a-h/templ"

	"gogo/internal/shared/tenant"
)

// countComponent renders a marker and increments a counter so tests can
// observe how many times the engine actually re-rendered an island.
type countComponent struct {
	count *int
	label string
}

func (c countComponent) Render(_ context.Context, w io.Writer) error {
	*c.count++
	_, err := fmt.Fprintf(w, "<span>%s-%d</span>", c.label, *c.count)
	return err
}

func renderPage(
	t *testing.T,
	composer *Composer,
	page *Page,
) string {
	t.Helper()

	var buf bytes.Buffer
	if err := composer.RenderPage(context.Background(), &buf, page, RenderingContext{}); err != nil {
		t.Fatalf("RenderPage returned error: %v", err)
	}
	return buf.String()
}

func renderComponentBytes(
	t *testing.T,
	composer *Composer,
	component *Component,
) (string, error) {
	t.Helper()

	var buf bytes.Buffer
	if err := composer.RenderComponent(context.Background(), &buf, component, RenderingContext{}); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func TestContractPlanner(t *testing.T) {
	t.Parallel()

	planner := ContractPlanner{}

	tests := []struct {
		name     string
		contract RenderingContract
		wantMode RenderMode
		wantTTL  time.Duration
	}{
		{
			name:     "realtime + personalized renders on the client",
			contract: RenderingContract{Freshness: FreshnessRealtime, Personalized: true},
			wantMode: ModeClient,
		},
		{
			name:     "realtime renders dynamically",
			contract: RenderingContract{Freshness: FreshnessRealtime},
			wantMode: ModeDynamic,
		},
		{
			name:     "cache ttl revalidates",
			contract: RenderingContract{Public: true, CacheTTL: 5 * time.Minute},
			wantMode: ModeRevalidate,
			wantTTL:  5 * time.Minute,
		},
		{
			name:     "stable public content is static",
			contract: RenderingContract{Public: true, RequiresSEO: true},
			wantMode: ModeStatic,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			decision := planner.Decide(tt.contract, RenderingContext{})
			if decision.Mode != tt.wantMode {
				t.Errorf("Mode = %v, want %v", decision.Mode, tt.wantMode)
			}
			if decision.CacheTTL != tt.wantTTL {
				t.Errorf("CacheTTL = %v, want %v", decision.CacheTTL, tt.wantTTL)
			}
		})
	}

	t.Run("explicit mode override wins", func(t *testing.T) {
		contract := RenderingContract{
			Mode:     Mode(ModeClient),
			CacheTTL: time.Hour,
		}
		decision := planner.Decide(contract, RenderingContext{})
		if decision.Mode != ModeClient {
			t.Errorf("Mode = %v, want explicit ModeClient pin", decision.Mode)
		}
	})
}

func TestTTLCache(t *testing.T) {
	t.Parallel()

	t.Run("missing key", func(t *testing.T) {
		cache := NewTTLCache()
		if _, ok := cache.Get("nope"); ok {
			t.Error("missing key should not hit")
		}
	})

	t.Run("set and get roundtrip", func(t *testing.T) {
		cache := NewTTLCache()
		cache.Set("k", []byte("value"), time.Minute)
		data, ok := cache.Get("k")
		if !ok {
			t.Fatal("expected cache hit")
		}
		if string(data) != "value" {
			t.Errorf("data = %q, want %q", data, "value")
		}
	})

	t.Run("zero ttl never expires", func(t *testing.T) {
		cache := NewTTLCache()
		cache.Set("k", []byte("value"), 0)
		time.Sleep(20 * time.Millisecond)
		if _, ok := cache.Get("k"); !ok {
			t.Error("zero-ttl entry should not expire on its own")
		}
	})

	t.Run("entry expires after ttl", func(t *testing.T) {
		cache := NewTTLCache()
		cache.Set("k", []byte("value"), 15*time.Millisecond)
		time.Sleep(60 * time.Millisecond)
		if _, ok := cache.Get("k"); ok {
			t.Error("entry should have expired")
		}
	})

	t.Run("invalidate removes entries", func(t *testing.T) {
		cache := NewTTLCache()
		cache.Set("a", []byte("1"), 0)
		cache.Set("b", []byte("2"), 0)
		cache.Invalidate("a", "c")
		if _, ok := cache.Get("a"); ok {
			t.Error("a should be invalidated")
		}
		if _, ok := cache.Get("b"); !ok {
			t.Error("b should still be cached")
		}
	})
}

func TestComposer(t *testing.T) {
	t.Parallel()

	t.Run("static island renders once and serves the cache", func(t *testing.T) {
		composer := NewComposer(NewTTLCache())
		renders := 0
		comp := Island("static",
			countComponent{count: &renders, label: "s"},
			RenderingContract{Public: true},
		)

		for i := 0; i < 3; i++ {
			html := renderPage(t, composer, &Page{Components: []*Component{comp}})
			if !strings.Contains(html, "s-1") {
				t.Error("rendered HTML should contain the island marker")
			}
		}
		if renders != 1 {
			t.Fatalf("static island rendered %d times, want 1 (cached)", renders)
		}

		composer.Invalidate("static")
		renderPage(t, composer, &Page{Components: []*Component{comp}})
		if renders != 2 {
			t.Errorf("static island should re-render after invalidate, got %d renders", renders)
		}
	})

	t.Run("dynamic island renders on every request", func(t *testing.T) {
		composer := NewComposer(NewTTLCache())
		renders := 0
		comp := Island("dynamic",
			countComponent{count: &renders, label: "d"},
			RenderingContract{Freshness: FreshnessRealtime},
		)

		for i := 0; i < 3; i++ {
			renderPage(t, composer, &Page{Components: []*Component{comp}})
		}
		if renders != 3 {
			t.Errorf("dynamic island rendered %d times, want 3", renders)
		}
	})

	t.Run("revalidate serves cache until ttl expires", func(t *testing.T) {
		composer := NewComposer(NewTTLCache())
		renders := 0
		comp := Island("revalidate",
			countComponent{count: &renders, label: "r"},
			RenderingContract{Public: true, CacheTTL: 15 * time.Millisecond},
		)

		renderPage(t, composer, &Page{Components: []*Component{comp}})
		renderPage(t, composer, &Page{Components: []*Component{comp}})
		if renders != 1 {
			t.Fatalf("revalidate island rendered %d times, want 1 before expiry", renders)
		}

		time.Sleep(60 * time.Millisecond)
		renderPage(t, composer, &Page{Components: []*Component{comp}})
		if renders != 2 {
			t.Errorf("revalidate island should re-render after expiry, got %d renders", renders)
		}
	})

	t.Run("personalized islands are never cached", func(t *testing.T) {
		cache := NewTTLCache()
		composer := NewComposer(cache)
		renders := 0
		comp := Island("personalized",
			countComponent{count: &renders, label: "p"},
			RenderingContract{Public: true, Personalized: true},
		)

		renderPage(t, composer, &Page{Components: []*Component{comp}})
		renderPage(t, composer, &Page{Components: []*Component{comp}})
		if renders != 2 {
			t.Errorf("personalized island rendered %d times, want 2 (never cached)", renders)
		}
		if _, ok := cache.Get("personalized"); ok {
			t.Error("personalized island must not be stored in the cache")
		}
	})

	t.Run("client island renders a lazy-loading placeholder", func(t *testing.T) {
		composer := NewComposer(NewTTLCache())
		stock := ClientIsland(
			"detail.x.stock",
			nil,
			"/catalog/x/stock",
			templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
				_, err := io.WriteString(w, "Checking stock…")
				return err
			}),
			RenderingContract{Mode: Mode(ModeClient)},
		)

		html := renderPage(t, composer, &Page{Components: []*Component{stock}})
		for _, want := range []string{
			`id="island-detail.x.stock"`,
			`hx-get="/catalog/x/stock"`,
			`hx-trigger="load"`,
			"Checking stock…",
		} {
			if !strings.Contains(html, want) {
				t.Errorf("client placeholder missing %q", want)
			}
		}
	})

	t.Run("client island escapes attribute values", func(t *testing.T) {
		composer := NewComposer(nil)
		hostile := ClientIsland(
			`evil"><script>alert(1)</script>`,
			nil,
			`/x"><script>alert(1)</script>`,
			nil,
			RenderingContract{Mode: Mode(ModeClient)},
		)

		html, err := renderComponentBytes(t, composer, hostile)
		if err != nil {
			t.Fatalf("render client island returned error: %v", err)
		}
		for _, dangerous := range []string{`"><script>alert(1)`, `hx-get="/x"><script>`} {
			if strings.Contains(html, dangerous) {
				t.Errorf("client island must escape attribute values, found %q in %q", dangerous, html)
			}
		}
		if !strings.Contains(html, "&lt;script&gt;") {
			t.Error("escaped form of the hostile name should be present")
		}
	})

	t.Run("page layout wraps the composed islands", func(t *testing.T) {
		composer := NewComposer(NewTTLCache())
		renders := 0
		page := &Page{
			Layout: func(inner templ.Component) templ.Component {
				return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
					if _, err := io.WriteString(w, "<main>"); err != nil {
						return err
					}
					if err := inner.Render(ctx, w); err != nil {
						return err
					}
					_, err := io.WriteString(w, "</main>")
					return err
				})
			},
			Components: []*Component{
				Island("a", countComponent{count: &renders, label: "a"}, RenderingContract{}),
				Island("b", countComponent{count: &renders, label: "b"}, RenderingContract{}),
			},
		}

		html := renderPage(t, composer, page)
		if !strings.HasPrefix(html, "<main>") || !strings.HasSuffix(html, "</main>") {
			t.Error("layout should wrap the island region")
		}
		if !strings.Contains(html, "a-") || !strings.Contains(html, "b-") {
			t.Error("layout should contain both islands")
		}
	})

	t.Run("render component serves a bare island fragment", func(t *testing.T) {
		composer := NewComposer(NewTTLCache())
		renders := 0
		comp := Island("fragment",
			countComponent{count: &renders, label: "f"},
			RenderingContract{Freshness: FreshnessRealtime},
		)

		var buf bytes.Buffer
		if err := composer.RenderComponent(context.Background(), &buf, comp, RenderingContext{}); err != nil {
			t.Fatalf("RenderComponent returned error: %v", err)
		}
		if got := buf.String(); !strings.Contains(got, "f-1") {
			t.Errorf("fragment = %q, want bare island HTML", got)
		}
	})

	t.Run("nil cache disables caching", func(t *testing.T) {
		composer := NewComposer(nil)
		renders := 0
		comp := Island("nocache",
			countComponent{count: &renders, label: "n"},
			RenderingContract{Public: true},
		)

		renderPage(t, composer, &Page{Components: []*Component{comp}})
		renderPage(t, composer, &Page{Components: []*Component{comp}})
		if renders != 2 {
			t.Errorf("island rendered %d times, want 2 with nil cache", renders)
		}
	})
}

// TestComposerTenantIsolation pins the guarantee that makes a cached island
// safe to serve to a second requester: a page-level island is cached per
// tenant, so one tenant can never be handed another's rendered rows. Both
// requests below use the identical island name and differ only in the tenant
// the gateway resolved from the request's host.
func TestComposerTenantIsolation(t *testing.T) {
	t.Parallel()

	composer := NewComposer(NewTTLCache())
	renders := 0
	comp := Island("home.product-grid",
		countComponent{count: &renders, label: "grid"},
		RenderingContract{Public: true, CacheTTL: time.Minute},
	)

	renderFor := func(id string) string {
		t.Helper()
		ctx := tenant.WithID(context.Background(), tenant.ID(id))
		var buf bytes.Buffer
		if err := composer.RenderComponent(ctx, &buf, comp, RenderingContext{}); err != nil {
			t.Fatalf("RenderComponent returned error: %v", err)
		}
		return buf.String()
	}

	alpha := renderFor("alpha")
	if !strings.Contains(alpha, "grid-1") {
		t.Fatalf("alpha's first render = %q, want the first-render marker", alpha)
	}

	if again := renderFor("alpha"); again != alpha {
		t.Errorf("alpha's second render = %q, want the cached %q", again, alpha)
	}
	if renders != 1 {
		t.Fatalf("renders = %d across two alpha requests, want 1 (served from cache)", renders)
	}

	beta := renderFor("beta")
	if beta == alpha {
		t.Errorf("beta was served alpha's cached HTML %q", beta)
	}
	if !strings.Contains(beta, "grid-2") {
		t.Errorf("beta's render = %q, want its own freshly rendered output", beta)
	}

	composer.Invalidate("home.product-grid")
	renderFor("alpha")
	renderFor("beta")
	if renders != 4 {
		t.Errorf("renders = %d after invalidating the island family, want 4", renders)
	}
}

func TestRenderNavigation(t *testing.T) {
	t.Parallel()

	composer := NewComposer(NewTTLCache())
	renders := 0
	page := &Page{
		Layout: func(inner templ.Component) templ.Component {
			return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
				if _, err := io.WriteString(w, "<div id=\"shell\">"); err != nil {
					return err
				}
				if err := inner.Render(ctx, w); err != nil {
					return err
				}
				_, err := io.WriteString(w, "</div>")
				return err
			})
		},
		Components: []*Component{
			Island("grid", countComponent{count: &renders, label: "g"}, RenderingContract{Public: true}),
		},
	}

	t.Run("plain request gets the full page", func(t *testing.T) {
		var buf bytes.Buffer
		if err := composer.RenderNavigation(
			context.Background(),
			&buf,
			page,
			RenderingContext{Path: "/", IsHTMX: false},
		); err != nil {
			t.Fatalf("RenderNavigation returned error: %v", err)
		}

		html := buf.String()
		if !strings.HasPrefix(html, `<div id="shell">`) || !strings.HasSuffix(html, "</div>") {
			t.Errorf("plain request should be wrapped in the page shell, got %q", html)
		}
		if !strings.Contains(html, "g-") {
			t.Error("full page should contain the island content")
		}
	})

	t.Run("htmx request gets the bare fragment", func(t *testing.T) {
		var buf bytes.Buffer
		if err := composer.RenderNavigation(
			context.Background(),
			&buf,
			page,
			RenderingContext{Path: "/", IsHTMX: true},
		); err != nil {
			t.Fatalf("RenderNavigation returned error: %v", err)
		}

		html := buf.String()
		if strings.Contains(html, "shell") {
			t.Errorf("htmx fragment must not include the page shell, got %q", html)
		}
		if !strings.Contains(html, "g-1") {
			t.Errorf("htmx fragment should contain the island region, got %q", html)
		}
	})
}
