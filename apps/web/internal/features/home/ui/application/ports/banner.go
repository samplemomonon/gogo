package ports

import (
	"context"
	"errors"

	"gogo/internal/features/home/ui/application/results"
)

// ErrNoBanner reports a slot with nothing published in it. It is an expected
// storefront state — the page renders without a banner — and never a failure.
var ErrNoBanner = errors.New("no banner published in the slot")

// BannerSlot is the editorial banner capability the home page consumes for its
// one banner slot. The editorial source lives behind contentcms' published-read
// contract (docs/CMS_CONTENT_MODEL.md); the composition root adapts that reader
// into this port, so home code depends on this interface and never on
// contentcms or a wire contract (docs/DECISION_CONTENT_SURFACES.md §5 step 2).
type BannerSlot interface {
	// ReadBanner returns the published banner for the named slot. ErrNoBanner
	// means the slot is blank; any other error means the editorial source
	// failed. The storefront degrades to "no banner" either way — the
	// distinction exists so the handler can log a failure without pretending an
	// empty slot is one.
	ReadBanner(ctx context.Context, slot string) (results.Banner, error)
}
