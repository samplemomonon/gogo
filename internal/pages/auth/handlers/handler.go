// Package handlers serves the auth page's routes.
package handlers

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/a-h/templ"

	"gogo/internal/pages/auth/application/ports"
	"gogo/internal/pages/auth/views/pages"
	"gogo/internal/shared/authconsumer"
	"gogo/internal/shared/grpcconsumer"
	"gogo/internal/shared/tenant"
	"gogo/internal/shared/web/render"
	"gogo/internal/shared/web/session"
	"gogo/internal/shared/web/shell"
)

// Handler serves the auth page's routes.
type Handler struct {
	auth   ports.Auth
	logger *slog.Logger
	cookie session.Policy
}

// NewHandler wires the handler around its remote auth peer. cookie carries the
// deployment's cookie policy, decided by the composition root.
func NewHandler(auth ports.Auth, logger *slog.Logger, cookie session.Policy) *Handler {
	return &Handler{auth: auth, logger: logger, cookie: cookie}
}

// Login renders the login page.
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, pages.Login("", ""), http.StatusOK)
}

// LoginPost handles the login form submission.
func (h *Handler) LoginPost(w http.ResponseWriter, r *http.Request) {
	email := r.PostFormValue("email")
	pin := r.PostFormValue("pin")

	if email == "" || pin == "" {
		h.render(w, r, pages.Login(email, "Email dan PIN wajib diisi"), http.StatusBadRequest)
		return
	}

	tenantID := tenant.FromContextOrDefault(r.Context()).String()
	signedIn, err := h.auth.Login(r.Context(), tenantID, email, pin)
	if err != nil {
		h.reject(w, r, email, err)
		return
	}

	h.cookie.Write(w, signedIn.ID, signedIn.CreatedAt, signedIn.ExpiresAt)
	http.Redirect(w, r, "/seller", http.StatusSeeOther)
}

// Logout revokes the session and clears the cookie.
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	// A revocation that fails must not strand the user inside the session they
	// asked to leave, so the cookie is cleared either way and the failure is
	// logged rather than surfaced.
	if id := session.Read(r); id != "" {
		if err := h.auth.Logout(r.Context(), id); err != nil {
			h.logger.Error("logout revocation failed", "error", err)
		}
	}
	h.cookie.Clear(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// reject answers a failed sign-in.
//
// A refusal and an outage are different failures with different responses: a
// refusal is the service working correctly and its reason is shown to the user,
// while an outage is logged and answered generically, because the transport's
// own text belongs in the log and a user cannot act on "connection refused".
func (h *Handler) reject(w http.ResponseWriter, r *http.Request, email string, err error) {
	if errors.Is(err, authconsumer.ErrAuthUnavailable) {
		h.logger.Error("login unavailable", "error", err)
		h.render(w, r, pages.Login(email, "Masuk sedang tidak tersedia. Silakan coba lagi."), http.StatusServiceUnavailable)
		return
	}

	reason := grpcconsumer.Detail(err)
	if reason == "" {
		reason = "Email atau PIN salah."
	}
	h.render(w, r, pages.Login(email, reason), http.StatusUnauthorized)
}

func (h *Handler) render(w http.ResponseWriter, r *http.Request, inner templ.Component, status int) {
	if err := render.HTMLStatus(w, r, shell.Page("Login", inner), status); err != nil {
		h.logger.Error("render login page", "error", err)
	}
}
