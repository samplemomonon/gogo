package render

import "github.com/a-h/templ"

// Component is one island of a page: a self-contained region rendered
// with its own strategy.
type Component struct {
	// Name uniquely identifies the island, and is the cache key for
	// cached islands (Static/Revalidate) and the id of the placeholder
	// element for Client islands.
	Name string

	// Contract declares the rendering characteristics of the island.
	Contract RenderingContract

	// Body is the server-rendered HTML used by Static, Revalidate and
	// Dynamic modes.
	Body templ.Component

	// FetchURL is fetched by the browser for Client-mode islands
	// (htmx lazy-load pattern: hx-get + hx-trigger="load").
	FetchURL string

	// Fallback is the placeholder content rendered inside a Client-mode
	// island while the browser loads FetchURL.
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
}
