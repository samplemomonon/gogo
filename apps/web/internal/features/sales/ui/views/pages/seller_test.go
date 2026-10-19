// Package pages_test covers the seller console's page shell: the document the
// console's own chrome produces when it wraps a section. The interesting decisions
// here are structural — which surface the title names, which nav item is current,
// and what the inset deliberately does not restate — so the tests render the real
// shell into a buffer and assert on the document.
package pages_test

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/a-h/templ"

	"gogo/internal/features/sales/ui/views/pages"
	"gogo/internal/shared/web/navigation"
)

// renderShell renders the console's page around content at the given request path,
// with the navigation context the shell composition provides.
func renderShell(t *testing.T, path string, content templ.Component) string {
	t.Helper()

	ctx := navigation.WithPath(t.Context(), path)
	ctx = navigation.WithNav(ctx, nil)

	var buf bytes.Buffer
	if err := pages.Seller(content).Render(ctx, &buf); err != nil {
		t.Fatalf("render Seller at %s: %v", path, err)
	}
	return buf.String()
}

// anchorFor returns the opening tag of the first anchor pointing at href, so an
// assertion can inspect that link's own attributes regardless of their order.
func anchorFor(t *testing.T, body, href string) string {
	t.Helper()

	re := regexp.MustCompile(`<a [^>]*href="` + regexp.QuoteMeta(href) + `"[^>]*>`)
	match := re.FindString(body)
	if match == "" {
		t.Fatalf("no anchor found for %s", href)
	}
	return match
}

// TestSellerRendersTheFullDocumentShell pins the outer contract of every console
// page: a complete, standalone HTML document with the Indonesian lang the shell
// declares, the console's own sidebar, and the content inside the shell's main
// landmark. The console is served as whole pages; nothing here is a fragment.
func TestSellerRendersTheFullDocumentShell(t *testing.T) {
	t.Parallel()

	body := renderShell(t, "/seller", templ.Raw(`<p id="section-marker">isi</p>`))

	for _, want := range []string{
		"<!doctype html>",
		`<html lang="id">`,
		`<main id="main"`,
		`id="seller-sidebar"`,
		`id="section-marker"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the document is missing %s", want)
		}
	}

	// The section renders inside the shell, not beside it: its marker must come
	// after the shell opened.
	if strings.Index(body, `id="section-marker"`) < strings.Index(body, `<main id="main"`) {
		t.Error("the section content renders before the shell's main landmark")
	}
}

// TestSellerTitlesEveryConsoleSurface pins the title rule: the title names the
// surface the path resolves to, using the most specific nav entry, and falls back
// to the dashboard for a path no entry covers. A title that stayed constant would
// make every tab and history entry look the same.
func TestSellerTitlesEveryConsoleSurface(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"/seller":           "Seller — Dashboard",
		"/seller/kasir":     "Seller — Kasir",
		"/seller/transaksi": "Seller — Transaksi",
		"/seller/produk":    "Seller — Produk",
		// A nested path resolves through the same prefix rule the nav uses, so a
		// future detail page is titled after its section.
		"/seller/produk/42": "Seller — Produk",
		"/seller/kategori":  "Seller — Kategori",
		"/seller/stok":      "Seller — Stok",
		// An unknown path under /seller resolves to the dashboard through the
		// prefix rule — the console's base href covers everything beneath it —
		// rather than to a blank title.
		"/seller/tidak-ada": "Seller — Dashboard",
	}
	for path, want := range cases {
		path, want := path, want
		t.Run(path, func(t *testing.T) {
			t.Parallel()

			body := renderShell(t, path, templ.Raw(""))
			if !strings.Contains(body, "<title>"+want+"</title>") {
				t.Errorf("title for %s is not %q", path, want)
			}
		})
	}
}

// TestSellerMarksExactlyOneCurrentDestination pins the nav's current-page state:
// the most specific matching entry carries aria-current and the primary chrome,
// prefix matches do not, and a path that matches nothing leaves every entry
// unmarked. Two current items — the dashboard prefixing every path — would tell a
// screen reader the wrong surface is serving the request.
func TestSellerMarksExactlyOneCurrentDestination(t *testing.T) {
	t.Parallel()

	body := renderShell(t, "/seller/kasir", templ.Raw(""))
	if got := strings.Count(body, `aria-current="page"`); got != 1 {
		t.Fatalf("aria-current count = %d, want exactly 1", got)
	}
	current := anchorFor(t, body, "/seller/kasir")
	if !strings.Contains(current, `aria-current="page"`) {
		t.Error("the kasir link is not marked current on /seller/kasir")
	}
	other := anchorFor(t, body, "/seller/kategori")
	if strings.Contains(other, `aria-current="page"`) {
		t.Error("a non-current destination is marked current")
	}

	// A nested path marks its section, not the dashboard its prefix also matches.
	nested := renderShell(t, "/seller/produk/42", templ.Raw(""))
	if got := strings.Count(nested, `aria-current="page"`); got != 1 {
		t.Errorf("aria-current count on a nested path = %d, want exactly 1", got)
	}
	if tag := anchorFor(t, nested, "/seller/produk"); !strings.Contains(tag, `aria-current="page"`) {
		t.Error("the produk section is not current on a nested produk path")
	}

	// A path outside the console marks nothing rather than everything. (Any path
	// under /seller is covered by the dashboard's base href, so "unmatched"
	// requires leaving the console entirely.)
	unknown := renderShell(t, "/lain", templ.Raw(""))
	if got := strings.Count(unknown, `aria-current="page"`); got != 0 {
		t.Errorf("aria-current count on an unmatched path = %d, want 0", got)
	}
}

// TestSellerSidebarLinksOnlyToServedSurfaces pins the console's sidebar as the
// single list of its destinations: the six served surfaces, and none of the
// removed ones. A dead link in the navigation looks like a feature that still
// exists, which is worse than its absence.
func TestSellerSidebarLinksOnlyToServedSurfaces(t *testing.T) {
	t.Parallel()

	body := renderShell(t, "/seller", templ.Raw(""))

	for _, href := range []string{
		"/seller", "/seller/kasir", "/seller/transaksi",
		"/seller/produk", "/seller/kategori", "/seller/stok",
	} {
		_ = anchorFor(t, body, href)
	}
	if strings.Contains(body, "/seller/supplier") {
		t.Error("the sidebar still links to the removed supplier surface")
	}

	// The sidebar's identity and its brand link are part of the contract: the
	// drawer trigger and the mobile behaviour hang off the id.
	if !strings.Contains(body, `id="seller-sidebar"`) {
		t.Error("the sidebar lost its identity")
	}
	if !strings.Contains(body, "Konsol seller") {
		t.Error("the sidebar's brand link lost its accessible name")
	}
}

// TestTheInsetDoesNotRestateTheShellContainer pins the bug the class was defined
// to prevent: the shell already wraps every page body in one `ds-container py-8`,
// so restating it in the console's inset nested a second max-width container and
// a second gutter inside the first. The document must contain the shell's
// container exactly once and the inset's own class must stay free of it.
func TestTheInsetDoesNotRestateTheShellContainer(t *testing.T) {
	t.Parallel()

	body := renderShell(t, "/seller", templ.Raw(""))

	if got := strings.Count(body, "ds-container py-8"); got != 1 {
		t.Errorf("shell container occurrences = %d, want exactly 1", got)
	}
	if pages.SidebarInsetClass != "gap-6" {
		t.Errorf("SidebarInsetClass = %q, want the container-free gap-6", pages.SidebarInsetClass)
	}
	if strings.Contains(pages.SidebarInsetClass, "container") || strings.Contains(pages.SidebarInsetClass, "px-4") {
		t.Error("the inset class restates the shell container")
	}
}
