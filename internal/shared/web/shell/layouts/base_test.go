package layouts

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/a-h/templ"

	"gogo/internal/shared/web/navigation"
)

func renderBase(t *testing.T, title string) string {
	t.Helper()

	body := templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		_, err := io.WriteString(w, `<section id="page-body">isi halaman</section>`)
		return err
	})

	var buf bytes.Buffer
	nav := []navigation.Item{navigation.Home, navigation.Billing, navigation.Profile}
	if err := Base(title, nav, "/billing", "").Render(ownerContext(body), &buf); err != nil {
		t.Fatalf("Base.Render returned error: %v", err)
	}
	return buf.String()
}

// ownerContext exists because Base takes the page body as a slot; templ
// components with children accept them through the context. This mirrors how
// shell.Page composes the layout.
func ownerContext(body templ.Component) context.Context {
	return templ.WithChildren(context.Background(), body)
}

func TestBaseDocumentStructure(t *testing.T) {
	t.Parallel()

	html := renderBase(t, "Tagihan · modul")

	for _, want := range []string{
		"<!doctype html>",
		`lang="id"`,
		"<title>Tagihan · modul</title>",
		`id="page-body"`,
		"isi halaman",
		navigation.Billing.Label,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("Base output missing %q", want)
		}
	}
}

// TestBaseLeavesThemeUnsetOnTheServer pins the load-bearing absence documented
// on Base: a server-set data-theme would defeat the stylesheet's
// `:root:not([data-theme])` dark-mode rule for undecided readers. The renderer
// must not set it, and no future refactor should "helpfully" add a default.
func TestBaseLeavesThemeUnsetOnTheServer(t *testing.T) {
	t.Parallel()

	html := renderBase(t, "Tagihan")

	start := strings.Index(html, "<html")
	if start == -1 {
		t.Fatalf("no <html> tag in the rendered document")
	}
	end := strings.Index(html[start:], ">")
	if end == -1 {
		t.Fatalf("unterminated <html> tag in the rendered document")
	}
	tag := html[start : start+end+1]
	if strings.Contains(tag, "data-theme") {
		t.Errorf("<html> tag %q sets data-theme; the server must leave the theme to the bootstrap script", tag)
	}
}

// TestBaseRunsThemeBootstrapBeforeTheStylesheet pins the other load-bearing
// ordering from the Base doc comment: the bootstrap resolves the stored theme
// before the stylesheet applies, so the first paint never flashes the wrong
// one. Moving it after the stylesheet reintroduces the flash.
func TestBaseRunsThemeBootstrapBeforeTheStylesheet(t *testing.T) {
	t.Parallel()

	html := renderBase(t, "Tagihan")

	bootstrap := strings.Index(html, "gogo-theme")
	stylesheet := strings.Index(html, "stylesheet")
	if bootstrap == -1 {
		t.Fatal("theme bootstrap script is missing from the document")
	}
	if stylesheet == -1 {
		t.Fatal("stylesheet link is missing from the document")
	}
	if bootstrap > stylesheet {
		t.Error("theme bootstrap appears after the stylesheet; the first paint would flash the wrong theme")
	}
}

func TestBaseEscapesTheTitle(t *testing.T) {
	t.Parallel()

	html := renderBase(t, `Tagihan <script>alert(1)</script>`)

	if strings.Contains(html, "<script>alert(1)</script>") {
		t.Error("the title leaked unescaped into the document")
	}
	if !strings.Contains(html, "&lt;script&gt;") {
		t.Error("the title should appear HTML-escaped in the document")
	}
}
