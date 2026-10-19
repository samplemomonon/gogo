// Package pages_test covers the home document template: the shell it composes
// (document type, language, title, landmarks) and the hero count and region it
// embeds. The page-level copy and routing are asserted in
// internal/features/home/ui/page_test.go; this file pins what the template owns —
// its document skeleton and its escaping — by rendering the component directly.
package pages_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/a-h/templ"

	"gogo/internal/features/home/ui/application/results"
	"gogo/internal/features/home/ui/views/pages"
	"gogo/internal/features/home/ui/views/partials"
)

func renderHome(t *testing.T, region templ.Component, count int, term string) string {
	t.Helper()
	return renderHomeCtx(t, context.Background(), region, count, term)
}

// renderHomeCtx renders the home document under a caller-supplied context, so
// request-scoped inputs — today the editorial banner — can be attached the way
// the handler attaches them (pages.WithBanner).
func renderHomeCtx(t *testing.T, ctx context.Context, region templ.Component, count int, term string) string {
	t.Helper()

	var buf bytes.Buffer
	if err := pages.Home(region, count, term).Render(ctx, &buf); err != nil {
		t.Fatalf("Home.Render: %v", err)
	}
	return buf.String()
}

// TestHomeRendersTheDocumentShell pins the shell markers the home document owns
// by choosing layouts.Base and its title: a lowercase doctype (quirks mode
// otherwise), the Indonesian lang attribute, the page's own title, and the main
// and status landmarks the shell's scripts and accessibility depend on.
func TestHomeRendersTheDocumentShell(t *testing.T) {
	t.Parallel()

	body := renderHome(t, templ.Raw(`<span id="region-marker">ok</span>`), 25, "")

	for _, want := range []string{
		"<!doctype html>",
		`<html lang="id"`,
		"<title>Gogo — Beranda</title>",
		`id="main"`,
		`id="page-status"`,
		`id="region-marker"`,
		">25<",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("home document missing %q", want)
		}
	}
}

// TestHomeEscapesHostileProductNames pins the composition of page and partial:
// a product name is platform data rendered inside the page's region, and it
// must reach the document escaped rather than as markup.
func TestHomeEscapesHostileProductNames(t *testing.T) {
	t.Parallel()

	region := partials.ProductCard(&results.Product{
		ID:    "1",
		Name:  `<script>alert("x")</script>`,
		Price: 1000,
		Stock: 1,
	})
	body := renderHome(t, region, 1, "")

	if strings.Contains(body, "<script>alert") {
		t.Error("a hostile product name produced executable markup")
	}
	if !strings.Contains(body, "&lt;script&gt;alert") {
		t.Error("the escaped product name should still be visible as text")
	}
}

// TestHomeRendersTheEditorialBanner pins the banner slot's happy path in the
// document: a banner attached to the request renders between the page hero and
// the listing region, carrying its image and title.
func TestHomeRendersTheEditorialBanner(t *testing.T) {
	t.Parallel()

	ctx := pages.WithBanner(context.Background(), results.Banner{
		Title:    "Promo Kemerdekaan",
		ImageURL: "/assets/img/promo.webp",
		ImageAlt: "Bendera dan diskon",
	})
	body := renderHomeCtx(t, ctx, templ.Raw(`<span id="region-marker">ok</span>`), 1, "")

	for _, want := range []string{
		`data-slot="home-banner"`,
		"Promo Kemerdekaan",
		`src="/assets/img/promo.webp"`,
		`alt="Bendera dan diskon"`,
		`id="region-marker"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("home document missing %q", want)
		}
	}
}

// TestHomeWithoutABannerRendersNoSlot pins the degrade default at the document
// level: nothing attached to the request means no banner section at all — no
// empty frame, no copy.
func TestHomeWithoutABannerRendersNoSlot(t *testing.T) {
	t.Parallel()

	body := renderHome(t, templ.Raw(`<span id="region-marker">ok</span>`), 0, "")

	if strings.Contains(body, `data-slot="home-banner"`) {
		t.Error("a document rendered without a banner carried a banner section")
	}
}

// TestHomeEscapesHostileBannerTitles pins the banner as untrusted input: an
// editorial title is CMS data and must reach the document escaped, like product
// names do.
func TestHomeEscapesHostileBannerTitles(t *testing.T) {
	t.Parallel()

	ctx := pages.WithBanner(context.Background(), results.Banner{
		Title: `<script>alert("x")</script>`,
	})
	body := renderHomeCtx(t, ctx, templ.Raw(`<span id="region-marker">ok</span>`), 1, "")

	if strings.Contains(body, "<script>alert") {
		t.Error("a hostile banner title produced executable markup")
	}
	if !strings.Contains(body, "&lt;script&gt;alert") {
		t.Error("the escaped banner title should still be visible as text")
	}
}
