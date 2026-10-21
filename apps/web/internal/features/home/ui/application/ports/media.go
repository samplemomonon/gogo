package ports

import "context"

// MediaResolver is what the storefront needs from media-service to render a
// product's primary image: turn a batch of media ids into a batch of presigned
// download URLs, in one round trip. The home team owns this port; the platform
// media-service (via the shared mediaconsumer kit) owns the implementation.
//
// It is deliberately separate from Listing. A product is listed by the product
// platform and imaged by the media platform, and the two can fail independently:
// a media outage must not take the grid down (images degrade to placeholders), so
// the port carries no "required" signal — a caller resolves what it can.
type MediaResolver interface {
	// BatchDownloadURLs returns a map keyed by the decimal-string media id to its
	// presigned download URL. Ids with no READY object (never uploaded, or since
	// removed) are absent rather than mapped to an empty string, so a caller can
	// distinguish "no image" from a URL that is genuinely blank. A failure to
	// reach the media upstream is reported as an error, which is the one case a
	// caller must degrade rather than paint a broken image.
	BatchDownloadURLs(ctx context.Context, ids []string) (map[string]string, error)
}
