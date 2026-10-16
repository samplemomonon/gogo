package render

import "net/http"

// HTMX header names and values. Two of them matter to the render decision, and
// getting either wrong produces a visibly broken page rather than a subtle one.
const (
	headerHXRequest     = "HX-Request"
	headerHXRequestType = "HX-Request-Type"
	headerHXHistory     = "HX-History-Restore-Request"
)

// Request type values carried by HX-Request-Type (htmx 2+).
const (
	hxTypePartial = "partial"
	hxTypeFull    = "full"
)

// IsHTMX reports whether the request was made by htmx at all.
//
// It answers the narrow question only: "is htmx driving this?". It is not the
// question Respond should ask, because an htmx request is not automatically a
// fragment request — see IsFragment.
func IsHTMX(r *http.Request) bool {
	return r.Header.Get(headerHXRequest) == "true"
}

// IsFragment reports whether the response should be a swapped fragment rather
// than a complete document.
//
// Checking HX-Request alone is wrong in two documented cases:
//
//   - hx-boost. A boosted link sets hx-target="body" implicitly, and htmx 4
//     signals that with "HX-Request-Type: full". Answering such a request with
//     a fragment injects a bare <section> straight into <body>.
//   - History restore. After a history-cache miss the browser re-requests with
//     "HX-History-Restore-Request: true" and expects the whole page.
//
// So: an explicit request type wins, then the history-restore signal, and only
// then the legacy boolean. HX-Request-Type is the right signal to prefer — it
// says what shape is wanted, where HX-Request only says who is asking.
func IsFragment(r *http.Request) bool {
	switch r.Header.Get(headerHXRequestType) {
	case hxTypePartial:
		return true
	case hxTypeFull:
		return false
	}
	if r.Header.Get(headerHXHistory) == "true" {
		return false
	}
	return IsHTMX(r)
}

// SetVary declares that this response varies on the HTMX request headers.
//
// Without it a shared cache is free to serve a fragment to an ordinary
// navigation, or a full document into a target element. htmx's own docs call
// this out as mandatory for the full-page-versus-fragment pattern, and RFC 9110
// §12.4.1 makes a field's absence meaningful, so varying on a header that is
// sometimes missing is correct rather than merely harmless.
//
// Call it from any handler that answers with IsFragment-dependent output.
func SetVary(w http.ResponseWriter) {
	w.Header().Add("Vary", headerHXRequest)
	w.Header().Add("Vary", headerHXRequestType)
}

// ContextFromRequest builds the rendering context from an HTTP request so
// handlers can treat HTMX as a property of rendering instead of reaching
// into the protocol themselves.
func ContextFromRequest(r *http.Request) RenderingContext {
	return RenderingContext{
		Path:   r.URL.Path,
		IsHTMX: IsHTMX(r),
	}
}
