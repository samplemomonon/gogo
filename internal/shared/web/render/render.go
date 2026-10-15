// Package render provides small helpers for rendering templ components
// and detecting HTMX requests.
package render

import (
	"net/http"

	"github.com/a-h/templ"
)

// HTML renders a templ component with status 200.
func HTML(w http.ResponseWriter, r *http.Request, component templ.Component) error {
	return HTMLStatus(w, r, component, http.StatusOK)
}

// HTMLStatus renders a templ component with the given status code.
func HTMLStatus(
	w http.ResponseWriter,
	r *http.Request,
	component templ.Component,
	status int,
) error {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	return component.Render(r.Context(), w)
}

// Error writes a plain-text HTTP error response.
func Error(w http.ResponseWriter, status int, message string) {
	http.Error(w, message, status)
}
