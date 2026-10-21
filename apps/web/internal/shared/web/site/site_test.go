package site

import (
	"strings"
	"testing"

	"gogo/internal/shared/web/navigation"
)

func TestNavigationFallsBackOnlyWhenNoCustomLinks(t *testing.T) {
	t.Parallel()
	fallback := []navigation.Item{navigation.Home, navigation.Profile}
	settings := Default()
	if got := settings.Navigation(fallback); len(got) != len(fallback) || got[0].Label != fallback[0].Label {
		t.Fatalf("default Navigation() = %+v, want fallback", got)
	}
	settings.HeaderLinks = []Link{{Label: "About", Href: "/pages/about"}}
	got := settings.Navigation(fallback)
	if len(got) != 1 || got[0].Label != "About" || got[0].Path != "/pages/about" {
		t.Fatalf("custom Navigation() = %+v, want authored menu", got)
	}
}

func TestThemeCSSOnlyEmitsValidatedColors(t *testing.T) {
	t.Parallel()
	settings := Default()
	settings.Theme = Theme{Primary: "#4f46e5", Secondary: "red;}</style><script>", Accent: "#abc"}
	css := settings.ThemeCSS()
	if !strings.Contains(css, "--color-primary:#4f46e5") {
		t.Fatalf("ThemeCSS() missing primary: %q", css)
	}
	if !strings.Contains(css, "--color-accent:#abc") {
		t.Fatalf("ThemeCSS() missing accent: %q", css)
	}
	if strings.Contains(css, "script") || strings.Contains(css, "red;") {
		t.Fatalf("ThemeCSS() included an invalid color: %q", css)
	}
	if settings := (Default()); settings.ThemeCSS() != "" {
		t.Fatalf("default ThemeCSS() = %q, want empty", settings.ThemeCSS())
	}
}

func TestFromContextFallsBackToDefaults(t *testing.T) {
	t.Parallel()
	settings := FromContext(t.Context())
	if settings.SiteName != "Gogo" || settings.HomePageSlug != "home" || !strings.Contains(settings.FooterText, "Gogo") {
		t.Fatalf("FromContext(zero) = %+v, want defaults", settings)
	}
}
