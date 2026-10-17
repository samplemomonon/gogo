package render

import (
	"context"
	"time"

	"github.com/a-h/templ"
)

// Component is one island of a page: a self-contained region rendered
// with its own strategy.
//
// Two loaders produce an island's data, and the names now say which side of
// the wire each runs on: Load runs on the server, inside the composition,
// while FetchURL is fetched by the browser for Client-mode islands. An island
// uses one or the other, never both.
type Component struct {
	// Name uniquely identifies the island, and is the cache key for
	// cached islands (Static/Revalidate) and the id of the placeholder
	// element for Client islands.
	Name string

	// Contract declares the rendering characteristics of the island.
	Contract RenderingContract

	// Body is the server-rendered HTML used by Static, Revalidate and
	// Dynamic modes. Ignored when Load is set.
	Body templ.Component

	// Load performs the island's server-side data loading. Optional: an
	// island whose Body closes over already-loaded data does not need
	// it. When set, it replaces Body as the rendered component, runs
	// inside the island's timeout context, and must pass ctx to its I/O
	// so a timeout cancels the real work instead of just abandoning a
	// goroutine. Ignored for Client-mode islands, which render a
	// placeholder and fetch FetchURL from the browser instead.
	//
	// Load is not called on a cache hit: a Static/Revalidate island is
	// served from its cached HTML without paying for the data fetch.
	Load func(ctx context.Context) (templ.Component, error)

	// Timeout bounds the island's render (Load plus the component). Zero
	// means no timeout for buffered renders; streamed islands default to
	// DefaultIslandTimeout, because a streamed page's later islands wait
	// on their predecessors' flushes and an unbounded island would hold
	// the whole document. Explicitly set values are honoured in every
	// mode.
	Timeout time.Duration

	// FetchURL is fetched by the browser for Client-mode islands
	// (htmx lazy-load pattern: hx-get + hx-trigger="load").
	FetchURL string

	// Fallback has two honest meanings, one per side:
	//
	//   - Client mode: the placeholder rendered while the browser loads
	//     FetchURL.
	//   - Server modes: the substitute rendered when Load or the render
	//     fails or times out, so one island's failure degrades to its
	//     fallback instead of aborting the page.
	//
	// A fallback is never cached: it is transient failure state, and the
	// next request must retry the real render.
	Fallback templ.Component
}

// Island builds a server-rendered component. The name must carry its owner
// namespace (ValidateIslandName); an invalid name is a wiring mistake that
// would silently alias another unit's cache namespace, so it panics here
// instead.
func Island(name string, body templ.Component, contract RenderingContract) *Component {
	if err := ValidateIslandName(name); err != nil {
		panic("render: " + err.Error())
	}
	return &Component{Name: name, Contract: contract, Body: body}
}

// ClientIsland builds a CSR island: the server sends a placeholder that
// the browser fills by fetching fetchURL. As with Island, the name must
// carry its owner namespace.
func ClientIsland(
	name string,
	body templ.Component,
	fetchURL string,
	fallback templ.Component,
	contract RenderingContract,
) *Component {
	if err := ValidateIslandName(name); err != nil {
		panic("render: " + err.Error())
	}
	return &Component{
		Name:     name,
		Contract: contract,
		Body:     body,
		FetchURL: fetchURL,
		Fallback: fallback,
	}
}

// Page declares the islands that make up a document and how the composed
// region is wrapped in the page shell.
type Page struct {
	// Layout wraps the composed islands in the page shell (head, chrome,
	// header, footer). When nil the islands are written bare — useful
	// for HTMX fragment responses.
	Layout func(inner templ.Component) templ.Component

	// Components are the islands of the page, in document order.
	Components []*Component

	// Streaming delivers the document progressively: the shell and each
	// island are flushed to the client as soon as they are rendered,
	// instead of being written once at the end. Full documents only —
	// needs a Layout, is ignored for HTMX fragment responses (their swap
	// is atomic by contract), and needs a flush-capable writer. The ADR
	// docs/ADR_FRAGMENT_DELIVERY_AND_MEASUREMENT.md records the trade-offs.
	Streaming bool

	// StatusCode is the HTTP status written for the document; zero leaves
	// the default (200) to the writer. Set it on pages that are rendered
	// at a non-200 status, e.g. a not-found product page.
	StatusCode int
}
