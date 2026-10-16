// Package pages_test covers the home document template: the shell it composes
// (document type, language, title, landmarks) and the hero count and region it
// embeds. The page-level copy and routing are asserted in internal/pages/home's
// page_test.go; this file pins what the template owns — its document skeleton
// and its escaping — by rendering the component directly.
package pages_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/a-h/templ"

	"gogo/internal/pages/home/application/results"
	"gogo/internal/pages/home/views/pages"
	"gogo/internal/pages/home/views/partials"
)

func renderHome(t *testing.T, region templ.Component, count int, term string) string {
	t.Helper()

	var buf bytes.Buffer
	if err := pages.Home(region, count, term).Render(context.Background(), &buf); err != nil {
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
