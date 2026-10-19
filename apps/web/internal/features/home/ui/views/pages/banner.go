package pages

import (
	"context"

	"gogo/internal/features/home/ui/application/results"
)

// bannerKey carries the request's editorial banner to the document as it
// renders. The listing handler resolves the banner before delegating to
// pagekit, whose layout hook accepts no request data of its own; the context is
// already the channel the shell's navigation arrives on
// (navigation.NavFromContext), so the page reads one more value from it instead
// of growing a parallel mechanism.
type bannerKey struct{}

// WithBanner attaches the storefront's editorial banner to ctx. The handler
// attaches nothing when the slot is empty or the editorial source failed —
// absence of the key is the "render no banner" signal, and this package's
// Banner renders nothing for a zero banner too, so the degrade path holds
// however the document is assembled (docs/DECISION_CONTENT_SURFACES.md §2).
func WithBanner(ctx context.Context, banner results.Banner) context.Context {
	return context.WithValue(ctx, bannerKey{}, banner)
}

// bannerFromContext returns the banner attached by WithBanner, or the zero
// banner when none is present.
func bannerFromContext(ctx context.Context) results.Banner {
	banner, _ := ctx.Value(bannerKey{}).(results.Banner)
	return banner
}
