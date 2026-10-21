package domain

import (
	"testing"
)

func TestDefaultSiteSettingsValidateAndNormalize(t *testing.T) {
	t.Parallel()
	settings := DefaultSiteSettings()
	if err := settings.Validate(); err != nil {
		t.Fatalf("DefaultSiteSettings().Validate() error = %v", err)
	}
	empty := SiteSettings{HeaderLinks: nil}
	normalized := empty.Normalized()
	if normalized.SchemaVersion != SiteSchemaVersion || normalized.SiteName != "Gogo" || normalized.HomePageSlug != "home" {
		t.Fatalf("Normalized() = %+v, want schema/name/home defaults", normalized)
	}
	if normalized.FooterText != "" {
		t.Fatalf("Normalized().FooterText = %q, want explicit empty footer preserved", normalized.FooterText)
	}
}

func TestSiteSettingsRejectUnsafeOrInvalidContent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*SiteSettings)
	}{
		{name: "external menu", mutate: func(s *SiteSettings) {
			s.HeaderLinks = []SiteLink{{Label: "Evil", Href: "https://attacker.test/"}}
		}},
		{name: "javascript menu", mutate: func(s *SiteSettings) {
			s.HeaderLinks = []SiteLink{{Label: "Evil", Href: "javascript:alert(1)"}}
		}},
		{name: "duplicate menu", mutate: func(s *SiteSettings) {
			s.HeaderLinks = []SiteLink{{Label: "One", Href: "/one"}, {Label: "Two", Href: "/one"}}
		}},
		{name: "javascript logo", mutate: func(s *SiteSettings) {
			s.LogoURL = "javascript:alert(1)"
		}},
		{name: "bad color", mutate: func(s *SiteSettings) {
			s.Theme.Primary = "red; background:url(evil)"
		}},
		{name: "bad home slug", mutate: func(s *SiteSettings) {
			s.HomePageSlug = "../admin"
		}},
		{name: "too many footer links", mutate: func(s *SiteSettings) {
			for i := 0; i < 9; i++ {
				s.FooterLinks = append(s.FooterLinks, SiteLink{Label: "Link", Href: "/link-" + string(rune('a'+i))})
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			settings := DefaultSiteSettings()
			test.mutate(&settings)
			if err := settings.Validate(); err == nil {
				t.Fatalf("Validate() accepted invalid settings: %+v", settings)
			}
		})
	}
}

func TestBindingValuesUsesPublishedSiteName(t *testing.T) {
	t.Parallel()
	values := BindingValuesWithSite("About", "about", "tenant-1", "Toko Baru")
	if values["site.name"] != "Toko Baru" {
		t.Fatalf("site.name = %q, want Toko Baru", values["site.name"])
	}
	if values["page.title"] != "About" || values["tenant.id"] != "tenant-1" {
		t.Fatalf("binding values = %+v", values)
	}
}
