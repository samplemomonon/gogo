package shell

import (
	"bytes"
	"context"
	"io"
	"regexp"
	"strings"
	"testing"

	"github.com/a-h/templ"

	"gogo.design/system/web/ui"
	"gogo/internal/shared/web/navigation"
)

func TestPageWrapsInnerInBaseShell(t *testing.T) {
	t.Parallel()

	inner := templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		_, err := io.WriteString(w, `<section id="module-region">hello</section>`)
		return err
	})

	var buf bytes.Buffer
	if err := Page("Test page", inner).Render(context.Background(), &buf); err != nil {
		t.Fatalf("Page.Render returned error: %v", err)
	}

	html := buf.String()
	for _, want := range []string{
		"<!doctype html>",
		"<title>Test page</title>",
		"Gogo",               // from the shell header brand
		`id="module-region"`, // the module's own content
		"hello",
		"/assets/js/htmx.min.js",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("shell page missing %q", want)
		}
	}
}

// TestPageEmitsShellScripts guards the one failure mode no compiler catches.
// templ parses a <script> body as raw text or as JavaScript, and neither
// spelling evaluates @templ.Raw or @templ.JS inside it — both leak into the
// HTML verbatim, so the browser sees a SyntaxError while templ generate and
// go build both stay green. Assert on rendered output, not on markup shape.
func TestPageEmitsShellScripts(t *testing.T) {
	t.Parallel()

	inner := templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		_, err := io.WriteString(w, `<p>ok</p>`)
		return err
	})

	var buf bytes.Buffer
	if err := Page("Scripts", inner).Render(context.Background(), &buf); err != nil {
		t.Fatalf("Page.Render returned error: %v", err)
	}
	html := buf.String()

	if strings.Contains(html, "@templ.") {
		t.Error("templ expression leaked into the document as literal text; the script blocks did not evaluate")
	}
	for _, want := range []string{
		"window.__gogoTheme",                        // ThemeBootstrapJS
		"getElementById(\"theme-toggle\")",          // ThemeApplyJS
		"window.htmx.config.includeIndicatorStyles", // HtmxConfigJS
		"label[role='button'][for]",                 // DrawerTriggerJS
		"navigator.sendBeacon(\"/rum\"",             // WebVitalsJS
		"web-vitals.attribution.iife.js",            // vendored web-vitals
	} {
		if !strings.Contains(html, want) {
			t.Errorf("shell script missing %q; expected InlineScript to emit it as JavaScript", want)
		}
	}
}

// A theme swap restyles every colour token in the document at once. Left
// animated, each of those changes starts its own transition and the switch takes
// visibly longer than the click that asked for it, with the controls arriving at
// their new colours out of step. The bootstrap must therefore suppress
// transitions around the swap and release them again afterwards — the flag is
// set before the new theme lands and removed two frames later, so nothing is
// left mid-transition.
func TestThemeSwapSuppressesTransitionsForOneFrame(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	if err := Page("Scripts", templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		_, err := io.WriteString(w, "<p>ok</p>")
		return err
	})).Render(context.Background(), &buf); err != nil {
		t.Fatalf("Page.Render returned error: %v", err)
	}
	html := buf.String()

	for _, want := range []string{
		`setAttribute("data-theme-switching"`,    // suppress before the swap
		`setAttribute("data-theme", next)`,       // then change the theme
		`requestAnimationFrame`,                  // and let the frame commit
		`removeAttribute("data-theme-switching"`, // then hand motion back
	} {
		if !strings.Contains(html, want) {
			t.Errorf("theme bootstrap missing %q; a theme swap would animate every colour in the page", want)
		}
	}

	// The suppression has to be released, or the whole app would be frozen for
	// anyone who ever switched theme.
	suppress := strings.Index(html, `setAttribute("data-theme-switching"`)
	release := strings.Index(html, `removeAttribute("data-theme-switching"`)
	if suppress < 0 || release < 0 || release < suppress {
		t.Errorf("the suppression flag must be set before it is released (set at %d, released at %d)", suppress, release)
	}
}

// The console sidebars park themselves directly under this bar with
// `lg:top-16`, which only lines up while the bar is 4rem tall. Raising or
// lowering it without moving them leaves a strip of sidebar behind the bar, or
// a gap above it, so the bar's height is pinned here rather than left implicit
// in the markup for the sidebars to guess at.
func TestHeaderHeightMatchesTheSidebarOffset(t *testing.T) {
	t.Parallel()

	inner := templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		_, err := io.WriteString(w, "<p>ok</p>")
		return err
	})

	var buf bytes.Buffer
	if err := Page("Nav", inner).Render(context.Background(), &buf); err != nil {
		t.Fatalf("Page.Render returned error: %v", err)
	}

	header := regexp.MustCompile(`(?s)<header[^>]*>`).FindString(buf.String())
	if header == "" {
		t.Fatal("the shell must render a top bar")
	}
	for _, want := range []string{"sticky", "top-0", "min-h-16"} {
		if !strings.Contains(header, want) {
			t.Errorf("top bar missing %q; the console sidebars offset themselves by 4rem to clear it, so a change here must change them too:\n%s", want, header)
		}
	}
}

func TestPageEscapesTitle(t *testing.T) {
	t.Parallel()

	inner := templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		_, err := io.WriteString(w, "<p>ok</p>")
		return err
	})

	var buf bytes.Buffer
	if err := Page("<script>alert(1)</script>", inner).Render(context.Background(), &buf); err != nil {
		t.Fatalf("Page.Render returned error: %v", err)
	}

	if strings.Contains(buf.String(), "<script>alert") {
		t.Error("title must be HTML-escaped")
	}
	if !strings.Contains(buf.String(), "&lt;script&gt;") {
		t.Error("escaped title should appear in the document")
	}
}

// TestPageHighlightsActiveNav pins the gateway's path-aware navigation: the
// module whose route covers the current request path is marked active.
func TestPageHighlightsActiveNav(t *testing.T) {
	t.Parallel()

	inner := templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		_, err := io.WriteString(w, `<section id="module-region">ok</section>`)
		return err
	})

	nav := []navigation.Item{
		{Label: "Home", Path: "/"},
		{Label: "Store", Path: "/catalog"},
		{Label: "Admin", Path: "/admin"},
	}

	for _, tt := range []struct {
		path   string
		active string
	}{
		{path: "/", active: "Home"},
		{path: "/catalog/products/1", active: "Store"},
		{path: "/admin", active: "Admin"},
		{path: "/admin/products", active: "Admin"},
		// A path nested under /catalog activates Store, never the root item.
		{path: "/catalog/admin", active: "Store"},
	} {
		ctx := navigation.WithPath(navigation.WithNav(context.Background(), nav), tt.path)

		var buf bytes.Buffer
		if err := Page("Nav", inner).Render(ctx, &buf); err != nil {
			t.Fatalf("Page.Render returned error for %q: %v", tt.path, err)
		}
		html := buf.String()

		for _, item := range nav {
			// Assert the semantics, not an exact class string, so reordering
			// the utility classes does not read as a behaviour change.
			anchor := navAnchor(html, item.Path)
			if anchor == "" {
				t.Fatalf("path %q: nav item %q was not rendered", tt.path, item.Path)
			}
			active := strings.Contains(anchor, `aria-current="page"`)
			highlighted := strings.Contains(anchor, "btn-active")

			want := item.Label == tt.active
			if active != want {
				t.Errorf("path %q: item %q aria-current = %v, want %v", tt.path, item.Label, active, want)
			}
			if highlighted != want {
				t.Errorf("path %q: item %q highlighted = %v, want %v", tt.path, item.Label, highlighted, want)
			}
		}
	}
}

// navAnchor returns the top-bar nav item rendered for a path, or "" when the
// path has no nav item. The search is scoped to the <nav> landmark so it cannot
// match the brand link, which also points at "/".
func navAnchor(html, path string) string {
	nav := regexp.MustCompile(`(?s)<nav[^>]*aria-label="Navigasi utama"[^>]*>(.*?)</nav>`).FindStringSubmatch(html)
	if len(nav) < 2 {
		return ""
	}
	return regexp.MustCompile(`<a href="` + regexp.QuoteMeta(path) + `"[^>]*>`).FindString(nav[1])
}

func TestPageRendersFederatedNav(t *testing.T) {
	t.Parallel()

	inner := templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		_, err := io.WriteString(w, `<section id="module-region">ok</section>`)
		return err
	})

	// Simulate the gateway: it injects the registry-compiled nav into the
	// request context, and the shell renders it without knowing the module.
	ctx := navigation.WithNav(context.Background(), []navigation.Item{
		{Label: "Billing", Path: "/billing"},
	})

	var buf bytes.Buffer
	if err := Page("Nav", inner).Render(ctx, &buf); err != nil {
		t.Fatalf("Page.Render returned error: %v", err)
	}

	html := buf.String()
	for _, want := range []string{`href="/billing"`, "Billing"} {
		if !strings.Contains(html, want) {
			t.Errorf("federated nav missing %q", want)
		}
	}
}

// TestPageRendersMobileBottomNav pins the mobile tab bar: the shell renders the
// icon-carrying primary destinations in its own "Navigasi utama" landmark, marked
// active by the same path rule as the top bar, while a text-only item (billing)
// stays out of it so the bar cannot overflow as modules register.
func TestPageRendersMobileBottomNav(t *testing.T) {
	t.Parallel()

	inner := templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		_, err := io.WriteString(w, `<section id="module-region">ok</section>`)
		return err
	})

	nav := []navigation.Item{
		{Label: "Home", Path: "/", Icon: ui.IconHome("size-5")},
		{Label: "Seller", Path: "/seller", Icon: ui.IconStore("size-5")},
		{Label: "Billing", Path: "/billing"},
	}
	ctx := navigation.WithPath(navigation.WithNav(context.Background(), nav), "/")

	var buf bytes.Buffer
	if err := Page("Nav", inner).Render(ctx, &buf); err != nil {
		t.Fatalf("Page.Render returned error: %v", err)
	}
	html := buf.String()

	bar := regexp.MustCompile(`(?s)<nav[^>]*aria-label="Navigasi bawah"[^>]*>(.*?)</nav>`).FindStringSubmatch(html)
	if len(bar) < 2 {
		t.Fatal("shell must render the mobile bottom nav landmark")
	}
	for _, want := range []string{`href="/"`, "Home", `href="/seller"`, "Seller"} {
		if !strings.Contains(bar[1], want) {
			t.Errorf("bottom nav missing %q in:\n%s", want, bar[1])
		}
	}
	if strings.Contains(bar[1], `href="/billing"`) {
		t.Error("a text-only destination must not appear in the mobile tab bar")
	}
	if !strings.Contains(bar[1], `aria-current="page"`) {
		t.Error("the current destination should be marked active in the tab bar")
	}
}

func TestRegistryPageRendersManifests(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	err := RegistryPage("gogo", "1.2.0", []RegistryView{
		{Name: "catalog", Version: "1.5.0", Entry: "/", Description: "Storefront"},
		{Name: "ops", Version: "1.0.0", Entry: "/healthz"},
	}).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("RegistryPage.Render returned error: %v", err)
	}

	html := buf.String()
	for _, want := range []string{
		"<!doctype html>",
		"module registry",
		"gogo 1.2.0", // hero carries the service identity + version
		`href="/"`,
		`href="/healthz"`,
		"catalog",
		"Storefront",
		"v1.5.0",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("registry page missing %q", want)
		}
	}
}

func TestRegistryPageEscapesManifestData(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	err := RegistryPage("gogo", "", []RegistryView{
		{Name: `<script>alert("x")</script>`, Version: `1.0"`, Entry: `/"><script>`,
			Description: `<img src=x onerror=alert(1)>`},
	}).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("RegistryPage.Render returned error: %v", err)
	}

	html := buf.String()
	for _, dangerous := range []string{"<script>alert", "<img src=x", `href="/"><script>"`} {
		if strings.Contains(html, dangerous) {
			t.Errorf("registry page must escape manifest data, found %q", dangerous)
		}
	}
	for _, escaped := range []string{"&lt;script&gt;", "&lt;img src=x", "&#34;"} {
		if !strings.Contains(html, escaped) {
			t.Errorf("registry page should contain escaped form %q of the manifest data", escaped)
		}
	}
}
