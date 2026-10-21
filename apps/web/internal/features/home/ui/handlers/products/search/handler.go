// Package homesearch serves the storefront's live search fragment. Typing in
// the header's search box fires an htmx GET to /search/suggest; this handler
// returns a bare fragment of product rows that the drawer sheet swaps into
// place, so search answers before the shopper commits to the full results page.
//
// It is deliberately a fragment endpoint — no page shell, no SEO, no island
// cache. Live search answers a keystroke, so it must be cheap and fresh: the
// read is one listing call against the platform's search projection, and the
// response is rendered straight to the wire. A media outage degrades rows to
// placeholder tiles, never to a broken drawer, and a listing outage shows the
// quiet "keep typing" state rather than an error page the shopper did not ask
// for.
package homesearch

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/a-h/templ"

	"gogo/internal/features/home/ui/application/ports"
	"gogo/internal/features/home/ui/views/partials"
)

// minTermLen is the shortest term the platform search projection can answer
// (a trigram index cannot accelerate anything shorter). A term beneath it is
// answered with the "keep typing" state instead of an empty round trip. It
// mirrors listingclient's own guard so this endpoint never asks the projection
// for something it cannot do.
const minTermLen = 3

// Handler serves the storefront's live-search fragment.
type Handler struct {
	listing ports.Listing
	logger  *slog.Logger
}

// NewHandler wires the live-search handler around the remote listing port.
func NewHandler(listing ports.Listing, logger *slog.Logger) *Handler {
	return &Handler{listing: listing, logger: logger}
}

// Handle answers GET /search/suggest?q=term with a fragment of live results.
// The term is trimmed and lower-bounded: an empty or too-short term returns the
// "keep typing" state, a listing outage returns the same state with a logged
// warning, and a healthy platform returns up to a small page of matching rows.
func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	term := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(term) < minTermLen {
		writeFragment(w, r.Context(), partials.LiveSearchPrompt(term))
		return
	}

	page, err := h.listing.ListPage(r.Context(), ports.Query{Term: term, Limit: 8})
	if err != nil {
		h.logger.Warn("live search failed",
			"error", err,
			"term", term,
		)
		writeFragment(w, r.Context(), partials.LiveSearchUnavailable())
		return
	}

	writeFragment(w, r.Context(), partials.LiveSearchResults(page.Products, term))
}

// writeFragment renders a bare HTML fragment. Live search swaps into a
// container with hx-swap="innerHTML", so there is no page shell and no
// negotiation — the body is the fragment itself.
func writeFragment(w http.ResponseWriter, ctx context.Context, component templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = component.Render(ctx, w)
}
