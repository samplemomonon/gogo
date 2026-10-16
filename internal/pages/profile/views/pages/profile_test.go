// Package pages_test covers the profile page's rendered contract: the account
// fields the read model carries are the fields the document shows, hostile
// account data arrives escaped, the signed-out document still renders, and the
// role the view displays is the presentation label, not the raw wire value.
package pages_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/a-h/templ"

	"gogo/internal/pages/profile/application/results"
	"gogo/internal/pages/profile/views/pages"
)

func render(t *testing.T, component templ.Component) string {
	t.Helper()

	var buf bytes.Buffer
	if err := component.Render(context.Background(), &buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	return buf.String()
}

// TestProfileRendersTheAccountOverview pins that the read model's fields reach
// the document, and that the read model's internal ID does not: the account
// hero shows the employee's name, email and role label, while the ID is a
// lookup key the page never displays.
func TestProfileRendersTheAccountOverview(t *testing.T) {
	t.Parallel()

	account := &results.Account{
		ID:    "emp-007",
		Name:  "Budi Santoso",
		Email: "budi@toko.com",
		Role:  "seller",
	}

	html := render(t, pages.Profile(account, pages.SectionOverview))

	for _, want := range []string{"<!doctype html>", "Budi Santoso", "budi@toko.com", "Seller", "Ringkasan", `href="/seller"`} {
		if !strings.Contains(html, want) {
			t.Errorf("account overview missing %q", want)
		}
	}
	if strings.Contains(html, account.ID) {
		t.Errorf("the internal account ID %q is user-visible; it is a lookup key, not display data", account.ID)
	}
}

// TestProfileRendersTheSignedOutState pins the nil-account render: the page
// still renders (the seller link stays reachable) with the sign-in prompt
// instead of an account header, and no account data.
func TestProfileRendersTheSignedOutState(t *testing.T) {
	t.Parallel()

	html := render(t, pages.Profile(nil, pages.SectionOverview))

	for _, want := range []string{"Belum masuk", `href="/login"`, `href="/seller"`} {
		if !strings.Contains(html, want) {
			t.Errorf("signed-out profile missing %q", want)
		}
	}
}

// TestProfileEscapesHostileAccountData pins the escaping contract at the view
// boundary: account fields are remote, untrusted text, so markup must render
// as text on every surface the account appears (the overview hero and the
// settings fact list).
func TestProfileEscapesHostileAccountData(t *testing.T) {
	t.Parallel()

	account := &results.Account{
		Name:  `Budi <script>alert(1)</script>`,
		Email: `budi@toko.com"><img src=x onerror=alert(1)>`,
		Role:  "seller",
	}

	for _, section := range []pages.Section{pages.SectionOverview, pages.SectionSettings} {
		html := render(t, pages.Profile(account, section))

		for _, raw := range []string{"<script>alert(1)</script>", "<img src=x"} {
			if strings.Contains(html, raw) {
				t.Errorf("section %q rendered hostile account data unescaped: %q", section, raw)
			}
		}
		for _, escaped := range []string{"&lt;script&gt;", "&lt;img src=x"} {
			if !strings.Contains(html, escaped) {
				t.Errorf("section %q missing the escaped account text %q", section, escaped)
			}
		}
	}
}

// TestSettingsSectionLabelstheRole pins the read-model-to-view mapping the page
// owns: a known wire role renders its display label, and an unknown role
// renders the neutral fallback rather than leaking the raw value.
func TestSettingsSectionLabelsTheRole(t *testing.T) {
	t.Parallel()

	html := render(t, pages.SettingsSection(&results.Account{Name: "Budi", Email: "budi@toko.com", Role: "seller"}))
	if !strings.Contains(html, ">Seller</dd>") {
		t.Error("the seller role should render its display label in the fact list")
	}

	html = render(t, pages.SettingsSection(&results.Account{Name: "Budi", Role: "superuser"}))
	if strings.Contains(html, "superuser") {
		t.Error("an unknown role leaked its raw value into the document")
	}
	if !strings.Contains(html, ">—</dd>") {
		t.Error("an unknown role should render the neutral fallback label")
	}
}

// TestSectionFor pins the path-to-section mapping the handler relies on:
// each account route names its own section, and anything else is the overview
// rather than an empty page.
func TestSectionFor(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		path string
		want pages.Section
	}{
		{"/profile", pages.SectionOverview},
		{"/profile/pesanan", pages.SectionOrders},
		{"/profile/alamat", pages.SectionAddresses},
		{"/profile/pengaturan", pages.SectionSettings},
		{"/profile/sesuatu-yang-lain", pages.SectionOverview},
	} {
		if got := pages.SectionFor(tt.path); got != tt.want {
			t.Errorf("SectionFor(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}
