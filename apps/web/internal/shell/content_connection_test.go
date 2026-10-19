// This file covers the composition root's banner-slot adapter: the one place
// that sees both the home page's port and the contentcms reader (D-023). It
// maps a published hero/banner block onto the storefront's banner and
// translates an absent page into the port's expected-empty sentinel.
package shell

import (
	"context"
	"errors"
	"testing"

	contentui "gogo/internal/features/contentcms/ui"
	homeports "gogo/internal/features/home/ui/application/ports"
	"gogo/internal/features/home/ui/application/results"
)

// stubContentReader is an in-memory contentui.Reader, so the adapter's mapping
// can be asserted without Builder.io.
type stubContentReader struct {
	page contentui.Page
	err  error
}

func (s stubContentReader) ReadPublished(context.Context, string) (contentui.Page, error) {
	return s.page, s.err
}

// TestBannerSlotMapsThePublishedBlock pins the mapping: the adapter walks the
// published page in document order, skips non-banner blocks, and projects the
// first hero/banner block with content into the storefront's read model.
func TestBannerSlotMapsThePublishedBlock(t *testing.T) {
	t.Parallel()

	reader := stubContentReader{page: contentui.Page{
		Slug:  "home-banner",
		Title: "Beranda",
		Blocks: []contentui.Block{
			{Type: "markdown", Body: "not a banner"},
			{Type: "hero"},
			{Type: "hero", Title: "Promo Kemerdekaan", ImageURL: "/assets/img/promo.webp", ImageAlt: "Bendera"},
		},
	}}

	banner, err := newBannerSlot(reader).ReadBanner(context.Background(), "home-banner")
	if err != nil {
		t.Fatalf("ReadBanner: %v", err)
	}
	want := results.Banner{Title: "Promo Kemerdekaan", ImageURL: "/assets/img/promo.webp", ImageAlt: "Bendera"}
	if banner != want {
		t.Errorf("banner = %+v, want %+v", banner, want)
	}
}

// TestBannerSlotAnswersNoBannerForAnAbsentPage pins the expected-empty
// translation: the reader's not-found — including the disabled reader's — is
// the port's ErrNoBanner, not a failure.
func TestBannerSlotAnswersNoBannerForAnAbsentPage(t *testing.T) {
	t.Parallel()

	_, err := newBannerSlot(stubContentReader{err: contentui.ErrNotFound}).
		ReadBanner(context.Background(), "home-banner")
	if !errors.Is(err, homeports.ErrNoBanner) {
		t.Fatalf("err = %v, want ErrNoBanner for an absent page", err)
	}
}

// TestBannerSlotAnswersNoBannerForAPageWithoutABannerBlock pins the other
// empty state: a published page whose blocks carry no renderable banner is the
// same "no banner" as no page at all.
func TestBannerSlotAnswersNoBannerForAPageWithoutABannerBlock(t *testing.T) {
	t.Parallel()

	reader := stubContentReader{page: contentui.Page{
		Slug:   "home-banner",
		Title:  "Beranda",
		Blocks: []contentui.Block{{Type: "markdown", Body: "long copy"}},
	}}

	_, err := newBannerSlot(reader).ReadBanner(context.Background(), "home-banner")
	if !errors.Is(err, homeports.ErrNoBanner) {
		t.Fatalf("err = %v, want ErrNoBanner for a page without a banner block", err)
	}
}

// TestBannerSlotReturnsSourceFailuresAsIs pins the distinction the port
// promises: an editorial-source failure is not disguised as an empty slot, so
// the handler can log it while still degrading to no banner.
func TestBannerSlotReturnsSourceFailuresAsIs(t *testing.T) {
	t.Parallel()

	_, err := newBannerSlot(stubContentReader{err: contentui.ErrUnavailable}).
		ReadBanner(context.Background(), "home-banner")
	if errors.Is(err, homeports.ErrNoBanner) {
		t.Fatal("a source failure was reported as an empty slot")
	}
	if !errors.Is(err, contentui.ErrUnavailable) {
		t.Errorf("err = %v, want the source's ErrUnavailable preserved", err)
	}
}
