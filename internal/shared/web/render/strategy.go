package render

import (
	"fmt"
	"net/http"

	"github.com/a-h/templ"
)

// Kind classifies a business use case by its rendering requirement, so a
// handler can negotiate the right HTML representation per request.
//
// This mirrors the full-page vs fragment pattern described in the HTMX
// documentation: HTMX requests always carry an "HX-Request: true" header,
// letting the server return a complete document for normal navigation and
// only the swapped region for in-page interactions.
type Kind int

const (
	// KindNavigation covers page-level use cases (listing, detail). They
	// return a complete HTML document for normal navigation — deep links,
	// bookmarks, SEO, browser history — and only the swapped region for
	// HTMX requests.
	KindNavigation Kind = iota

	// KindMutation covers interaction-level use cases (create, update,
	// delete). They return an HTML fragment for HTMX requests and fall
	// back to the Post/Redirect/Get pattern for plain requests, so the
	// UI keeps working even without JavaScript.
	KindMutation
)

// Strategy describes how a use case renders its outcome for a given
// request. Handlers declare the strategy next to the use case they serve,
// keeping the rendering decision explicit and consistent.
type Strategy struct {
	// Kind selects the rendering policy.
	Kind Kind

	// Page is the full document rendered for plain requests of a
	// KindNavigation use case.
	Page templ.Component

	// Fragment is the HTML swapped into the page for HTMX requests.
	Fragment templ.Component

	// Redirect is the PRG destination for plain requests of a
	// KindMutation use case.
	Redirect string
}

// Respond renders the appropriate representation for the request according
// to the strategy of the business use case being served. An unexpected
// strategy kind is a programming error, surfaced as an error instead of a
// silently-empty or mis-rendered response.
//
// Every response here is a negotiated representation: the same URL yields a
// document for an ordinary navigation and a fragment for an in-page swap. That
// is only safe behind a shared cache if Vary is declared, so Respond sets it —
// a handler that forgets gets the bug, not the fix.
func Respond(
	w http.ResponseWriter,
	r *http.Request,
	strategy Strategy,
) error {
	SetVary(w)

	switch strategy.Kind {
	case KindNavigation:
		if IsFragment(r) {
			return HTML(w, r, strategy.Fragment)
		}
		return HTML(w, r, strategy.Page)

	case KindMutation:
		if IsFragment(r) {
			return HTML(w, r, strategy.Fragment)
		}
		http.Redirect(w, r, strategy.Redirect, http.StatusSeeOther)
		return nil

	default:
		return fmt.Errorf("render: unknown strategy kind %d", strategy.Kind)
	}
}
