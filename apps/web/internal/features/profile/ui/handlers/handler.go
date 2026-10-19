// Package handlers serves the profile page's routes.
package handlers

import (
	"log/slog"
	"net/http"

	"gogo/internal/features/profile/ui/application/ports"
	"gogo/internal/features/profile/ui/views/pages"
	"gogo/internal/shared/web/render"
	"gogo/internal/shared/web/session"
)

// Handler serves the storefront account page and its section routes.
type Handler struct {
	auth   ports.Accounts
	logger *slog.Logger
}

// NewHandler wires the handler around its remote auth peer.
func NewHandler(auth ports.Accounts, logger *slog.Logger) *Handler {
	return &Handler{auth: auth, logger: logger}
}

// Section renders one account section (overview, orders, addresses, settings).
// It is public: a valid session cookie shows the account, anything else shows
// the signed-out state with a sign-in CTA.
func (h *Handler) Section(w http.ResponseWriter, r *http.Request) {
	account, err := h.auth.Current(r.Context(), session.Read(r))
	if err != nil {
		// An outage is not a signed-out state: still render the page (so the
		// seller link stays reachable) but log the degraded read.
		h.logger.Error("profile account lookup failed", "error", err)
	}

	section := pages.SectionFor(r.URL.Path)
	if err := render.HTML(w, r, pages.Profile(account, section)); err != nil {
		h.logger.Error("render profile page", "error", err)
	}
}
