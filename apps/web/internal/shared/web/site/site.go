// Package site carries the tenant's published site-shell settings through a
// request. The content vertical owns the authored values and their draft /
// publish lifecycle; the shared shell only knows this small, presentation-safe
// view model. Keeping the context key here lets the shell render a CMS-managed
// brand, menu, footer, and theme without importing the content vertical.
package site

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"gogo/internal/shared/web/navigation"
)

// Link is one navigation destination in the public shell.
type Link struct {
	Label string `json:"label"`
	Href  string `json:"href"`
}

// Theme is the public shell's brand colour overlay. Empty values inherit the
// built-in gogo / gogo-dark themes.
type Theme struct {
	Primary   string `json:"primary,omitempty"`
	Secondary string `json:"secondary,omitempty"`
	Accent    string `json:"accent,omitempty"`
}

// Settings is the published site shell used by the shared layout. Zero values
// deliberately fall back to the current built-in shell, so a database outage
// or an unconfigured tenant keeps the storefront serving with its old brand.
type Settings struct {
	SiteName     string `json:"siteName"`
	Tagline      string `json:"tagline,omitempty"`
	LogoURL      string `json:"logoUrl,omitempty"`
	LogoAlt      string `json:"logoAlt,omitempty"`
	HomePageSlug string `json:"homePageSlug"`
	HeaderLinks  []Link `json:"headerLinks,omitempty"`
	FooterText   string `json:"footerText,omitempty"`
	FooterLinks  []Link `json:"footerLinks,omitempty"`
	Theme        Theme  `json:"theme,omitempty"`

	// UseDefaultNavigation keeps the gateway's federated menu on application
	// surfaces such as the dashboard. The CMS menu is for the public site; it
	// must not hide the links operators use to move around the admin app.
	// This is request-scoped policy, not authored content, so it is not stored.
	UseDefaultNavigation bool `json:"-"`
}

// Default is the shell used before a tenant publishes site settings, and the
// fail-safe when the content store is unavailable.
func Default() Settings {
	return Settings{
		SiteName:     "Gogo",
		HomePageSlug: "home",
		FooterText:   "Gogo — Product Catalog",
	}
}

// WithSettings attaches the resolved published site to a request context.
func WithSettings(ctx context.Context, settings Settings) context.Context {
	return context.WithValue(ctx, settingsKey{}, settings)
}

// FromContext returns the request's site settings, or the built-in default
// when the request did not pass through the gateway middleware.
func FromContext(ctx context.Context) Settings {
	settings, ok := ctx.Value(settingsKey{}).(Settings)
	if !ok {
		return Default()
	}
	if strings.TrimSpace(settings.SiteName) == "" {
		settings.SiteName = Default().SiteName
	}
	if strings.TrimSpace(settings.HomePageSlug) == "" {
		settings.HomePageSlug = Default().HomePageSlug
	}
	return settings
}

type settingsKey struct{}

// Description is the safe meta description shown by the shared layout. A
// tenant can override it with a tagline; the default keeps the storefront's
// previous copy.
func (s Settings) Description() string {
	if tagline := strings.TrimSpace(s.Tagline); tagline != "" {
		return tagline
	}
	return s.SiteName + " — a federated modular-monolith product catalog built with Go, Chi, Templ and HTMX."
}

// LogoText is the accessible name for an authored logo. The site name remains
// the fallback so a missing alt field never leaves the brand image unnamed.
func (s Settings) LogoText() string {
	if alt := strings.TrimSpace(s.LogoAlt); alt != "" {
		return alt
	}
	return s.SiteName
}

// Navigation adapts authored header links to the shell's navigation model. A
// nil or empty authored menu keeps the federated menu, so a tenant that has
// not configured a menu does not lose its registered destinations.
func (s Settings) Navigation(fallback []navigation.Item) []navigation.Item {
	if len(s.HeaderLinks) == 0 {
		return fallback
	}
	items := make([]navigation.Item, 0, len(s.HeaderLinks))
	for _, link := range s.HeaderLinks {
		items = append(items, navigation.Item{Label: link.Label, Path: link.Href})
	}
	return items
}

var hexColorPattern = regexp.MustCompile(`^#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)

// ThemeCSS returns a safe custom-property override for the validated brand
// colours. It is generated here, never from free-form CSS. Empty colours are
// omitted so the built-in light/dark themes continue to supply them.
func (s Settings) ThemeCSS() string {
	var declarations []string
	appendColor := func(token, colour string) {
		if colour == "" || !hexColorPattern.MatchString(colour) {
			return
		}
		declarations = append(declarations,
			fmt.Sprintf("--color-%s:%s", token, colour),
			fmt.Sprintf("--color-%s-content:%s", token, contentColor(colour)),
		)
	}
	appendColor("primary", s.Theme.Primary)
	appendColor("secondary", s.Theme.Secondary)
	appendColor("accent", s.Theme.Accent)
	if len(declarations) == 0 {
		return ""
	}
	return ":root,html[data-theme=\"gogo-dark\"]{" + strings.Join(declarations, ";") + "}"
}

// contentColor picks black or white for readable text on a validated hex
// background using a simple sRGB luminance threshold.
func contentColor(hex string) string {
	value := strings.TrimPrefix(hex, "#")
	if len(value) == 3 {
		value = string([]byte{value[0], value[0], value[1], value[1], value[2], value[2]})
	}
	var channels [3]int
	for i := 0; i < 3; i++ {
		var component int
		for j := 0; j < 2; j++ {
			component *= 16
			switch c := value[i*2+j]; {
			case c >= '0' && c <= '9':
				component += int(c - '0')
			case c >= 'a' && c <= 'f':
				component += int(c-'a') + 10
			case c >= 'A' && c <= 'F':
				component += int(c-'A') + 10
			}
		}
		channels[i] = component
	}
	// sRGB luminance, using integer weights for a small deterministic routine.
	luminance := 2126*channels[0] + 7152*channels[1] + 722*channels[2]
	if luminance > 1275000 {
		return "#111827"
	}
	return "#ffffff"
}
