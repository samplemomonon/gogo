// Package handlers wires the billing module's HTTP routes: one landing page
// and one HTMX fragment. The fragment renders the module's live activity
// projection — catalog product events this module observed through the
// shared event bus — demonstrating cross-module federation behind hx-get.
package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"gogo/internal/features/billing/activity"
	"gogo/internal/features/billing/views"
	"gogo/internal/shared/web/render"
	"gogo/internal/shared/web/shell"
)

// Handler serves the billing module routes.
type Handler struct {
	feed *activity.Activity
}

// New returns the billing HTTP handler backed by the shared activity
// projection.
func New(feed *activity.Activity) *Handler { return &Handler{feed: feed} }

// RegisterRoutes mounts the module's routes directly on the gateway router.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/billing", h.Index)
	r.Get("/billing/fragment", h.Fragment)
}

// Index serves the module landing page: a full document for plain requests
// and the bare region for HTMX requests (render.Strategy decides, so the
// handler never branches on the HX-Request header itself).
func (h *Handler) Index(w http.ResponseWriter, r *http.Request) {
	entries := h.feed.Recent()
	if err := render.Respond(w, r, render.Strategy{
		Kind:     render.KindNavigation,
		Page:     shell.Page("Tagihan · modul", views.Landing(entries)),
		Fragment: views.Feed(entries),
	}); err != nil {
		http.Error(w, "Gagal merender halaman.", http.StatusInternalServerError)
	}
}

// Fragment is the HTMX fragment endpoint the landing page's button targets.
func (h *Handler) Fragment(w http.ResponseWriter, r *http.Request) {
	if err := render.HTML(w, r, views.Feed(h.feed.Recent())); err != nil {
		http.Error(w, "failed to render fragment", http.StatusInternalServerError)
	}
}
