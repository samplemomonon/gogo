package domain

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// SiteSchemaVersion is the only site-shell document version accepted by the
// editor and the store. Site settings are versioned independently from page
// documents because they have a different lifecycle and a different shape.
const SiteSchemaVersion = 1

// SiteLink is one authored public-shell destination. Hrefs are deliberately
// same-origin paths: a content editor cannot turn the global menu into an
// open redirect.
type SiteLink struct {
	Label string `json:"label"`
	Href  string `json:"href"`
}

// SiteTheme holds optional brand-colour overrides. Empty colours inherit the
// built-in gogo and gogo-dark themes.
type SiteTheme struct {
	Primary   string `json:"primary,omitempty"`
	Secondary string `json:"secondary,omitempty"`
	Accent    string `json:"accent,omitempty"`
}

// SiteSettings is the tenant-scoped public shell document. It controls only
// presentation: brand, menu, footer, homepage selection, and safe colour
// tokens. It cannot contain HTML, CSS, JavaScript, or arbitrary template code.
type SiteSettings struct {
	SchemaVersion int        `json:"schemaVersion"`
	SiteName      string     `json:"siteName"`
	Tagline       string     `json:"tagline,omitempty"`
	LogoURL       string     `json:"logoUrl,omitempty"`
	LogoAlt       string     `json:"logoAlt,omitempty"`
	HomePageSlug  string     `json:"homePageSlug"`
	HeaderLinks   []SiteLink `json:"headerLinks,omitempty"`
	FooterText    string     `json:"footerText,omitempty"`
	FooterLinks   []SiteLink `json:"footerLinks,omitempty"`
	Theme         SiteTheme  `json:"theme,omitempty"`
}

// DefaultSiteSettings is the safe shell used before a tenant publishes site
// settings. It matches the shell's previous hard-coded brand exactly.
func DefaultSiteSettings() SiteSettings {
	return SiteSettings{
		SchemaVersion: SiteSchemaVersion,
		SiteName:      "Gogo",
		HomePageSlug:  "home",
		FooterText:    "Gogo — Product Catalog",
	}
}

// Normalized fills the values a new document may omit without weakening the
// validation rules for explicit values.
func (s SiteSettings) Normalized() SiteSettings {
	if s.SchemaVersion == 0 {
		s.SchemaVersion = SiteSchemaVersion
	}
	if strings.TrimSpace(s.SiteName) == "" {
		s.SiteName = DefaultSiteSettings().SiteName
	}
	if strings.TrimSpace(s.HomePageSlug) == "" {
		s.HomePageSlug = DefaultSiteSettings().HomePageSlug
	}
	return s
}

// Validate is the server-side boundary for every draft save and publish.
func (s SiteSettings) Validate() error {
	if s.SchemaVersion != SiteSchemaVersion {
		return fmt.Errorf("%w: unsupported site schema version %d (want %d)", ErrInvalidData, s.SchemaVersion, SiteSchemaVersion)
	}
	if err := validateSiteText("siteName", s.SiteName, 80, true); err != nil {
		return err
	}
	if err := validateSiteText("tagline", s.Tagline, 160, false); err != nil {
		return err
	}
	if err := validateSiteText("logoAlt", s.LogoAlt, 160, false); err != nil {
		return err
	}
	if s.LogoURL != "" {
		if len(s.LogoURL) > 2_000 || !SafePublicURL(s.LogoURL) {
			return fmt.Errorf("%w: logoUrl must be an HTTPS URL or a same-origin path", ErrInvalidData)
		}
	}
	if !validSlug(s.HomePageSlug) {
		return fmt.Errorf("%w: homePageSlug must contain 1-80 lowercase letters, digits, or hyphens", ErrInvalidData)
	}
	if err := validateSiteLinks("headerLinks", s.HeaderLinks); err != nil {
		return err
	}
	if err := validateSiteText("footerText", s.FooterText, 240, false); err != nil {
		return err
	}
	if err := validateSiteLinks("footerLinks", s.FooterLinks); err != nil {
		return err
	}
	if err := validateSiteColor("theme.primary", s.Theme.Primary); err != nil {
		return err
	}
	if err := validateSiteColor("theme.secondary", s.Theme.Secondary); err != nil {
		return err
	}
	if err := validateSiteColor("theme.accent", s.Theme.Accent); err != nil {
		return err
	}
	return nil
}

func validateSiteText(field, value string, limit int, required bool) error {
	if required && strings.TrimSpace(value) == "" {
		return fmt.Errorf("%w: %s is required", ErrInvalidData, field)
	}
	if len(value) > limit {
		return fmt.Errorf("%w: %s must be at most %d bytes", ErrInvalidData, field, limit)
	}
	if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return fmt.Errorf("%w: %s must be valid UTF-8 without NUL bytes", ErrInvalidData, field)
	}
	return nil
}

func validateSiteLinks(field string, links []SiteLink) error {
	if len(links) > 8 {
		return fmt.Errorf("%w: %s accepts at most 8 links", ErrInvalidData, field)
	}
	seen := make(map[string]struct{}, len(links))
	for i, link := range links {
		if err := validateSiteText(fmt.Sprintf("%s[%d].label", field, i), link.Label, 80, true); err != nil {
			return err
		}
		if len(link.Href) > 2_000 || !SafeSameOriginPath(link.Href) {
			return fmt.Errorf("%w: %s[%d].href must be a same-origin path", ErrInvalidData, field, i)
		}
		if _, exists := seen[link.Href]; exists {
			return fmt.Errorf("%w: %s[%d].href duplicates another destination", ErrInvalidData, field, i)
		}
		seen[link.Href] = struct{}{}
	}
	return nil
}

func validateSiteColor(field, value string) error {
	if value != "" && !hexColorPattern.MatchString(value) {
		return fmt.Errorf("%w: %s must be a #RGB or #RRGGBB color", ErrInvalidData, field)
	}
	return nil
}
