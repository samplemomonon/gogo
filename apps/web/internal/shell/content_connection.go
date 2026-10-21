// Content connection wiring for the gateway.
package shell

import (
	"context"
	"errors"

	contentcmscompose "gogo/internal/features/contentcms/compose"
	homeports "gogo/internal/features/home/ui/application/ports"
	"gogo/internal/features/home/ui/application/results"
)

// newBannerSlot adapts contentcms' published-page reader to the home page's
// declared banner-slot port. The adapter lives in the composition root because
// no vertical imports another (D-023): home depends only on ports.BannerSlot,
// contentcms only on contentui.Reader, and the shell is the one layer allowed
// to see both (docs/DECISION_CONTENT_SURFACES.md §5 step 2).
func newBannerSlot(reader contentcmscompose.Reader) homeports.BannerSlot {
	return bannerSlot{reader: reader}
}

type bannerSlot struct {
	reader contentcmscompose.Reader
}

// ReadBanner maps the slot's published page to the storefront's banner: the
// first hero/banner block with any content in it, in document order. An absent
// or empty page is ErrNoBanner — an expected state the handler renders without
// — while an editorial-source failure is returned as-is for the handler to log
// and degrade from.
func (b bannerSlot) ReadBanner(ctx context.Context, slot string) (results.Banner, error) {
	page, err := b.reader.ReadPublished(ctx, slot)
	if errors.Is(err, contentcmscompose.ErrNotFound) {
		return results.Banner{}, homeports.ErrNoBanner
	}
	if err != nil {
		return results.Banner{}, err
	}
	for _, block := range page.EditorialBlocks {
		if block.Type != "hero" && block.Type != "banner" {
			continue
		}
		banner := results.Banner{Title: block.Title, ImageURL: block.ImageURL, ImageAlt: block.ImageAlt}
		if banner.Title != "" || banner.ImageURL != "" {
			return banner, nil
		}
	}
	return results.Banner{}, homeports.ErrNoBanner
}
