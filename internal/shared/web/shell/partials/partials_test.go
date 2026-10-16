package partials

import (
	"context"
	"io"
	"testing"

	"github.com/a-h/templ"

	"gogo/internal/shared/web/navigation"
)

func icon(marker string) templ.Component {
	return templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		_, err := io.WriteString(w, marker)
		return err
	})
}

func TestNavItems(t *testing.T) {
	t.Parallel()

	homeIcon := icon("home-icon")
	items := []navigation.Item{
		{Label: "Beranda", Path: "/", Icon: homeIcon},
		{Label: "Tagihan", Path: "/billing"},
	}

	got := navItems(items)

	if len(got) != len(items) {
		t.Fatalf("navItems returned %d items, want %d", len(got), len(items))
	}
	// The shell renders files in the order the gateway aggregated them, so the
	// adapter must not reorder: a manifest that registers after another stays
	// after it.
	for i, want := range items {
		if got[i].Href != want.Path || got[i].Label != want.Label {
			t.Errorf("item %d = {%q, %q}, want {%q, %q}", i, got[i].Href, got[i].Label, want.Path, want.Label)
		}
	}
	if got[0].Icon == nil {
		t.Error("item 0 lost its icon in the mapping")
	}
	if got[1].Icon != nil {
		t.Error("item 1 gained an icon in the mapping")
	}
}

func TestBottomNavItems(t *testing.T) {
	t.Parallel()

	items := []navigation.Item{
		{Label: "Beranda", Path: "/", Icon: icon("home")},
		{Label: "Tagihan", Path: "/billing"}, // text-only: stays in the top bar
		{Label: "Akun", Path: "/profile", Icon: icon("user")},
	}

	got := bottomNavItems(items)

	// The tab bar renders only icon-led destinations; a text-only item that
	// slipped into it would push the primary tabs off a narrow screen.
	if len(got) != 2 {
		t.Fatalf("bottomNavItems kept %d items, want the 2 icon-led ones", len(got))
	}
	if got[0].Label != "Beranda" || got[1].Label != "Akun" {
		t.Errorf("bottomNavItems = [%q, %q], want [Beranda, Akun]", got[0].Label, got[1].Label)
	}
	for _, item := range got {
		if item.Icon == nil {
			t.Errorf("item %q has no icon and must not have passed the filter", item.Label)
		}
	}
}
