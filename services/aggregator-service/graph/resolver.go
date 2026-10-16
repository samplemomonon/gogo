package graph

// Resolver is the dependency-injection root for the GraphQL layer.
//
// It is passed by value into the per-type resolvers, which keeps their signatures
// to (ctx, parents) with no captured state, and it holds only collaborator
// interfaces rather than concrete clients, so every resolver is testable against a
// stub.
//
// The field names are deliberately suffixed. gqlgen's per-type resolvers embed
// *Resolver and define methods named after the schema's fields, so a method called
// Categories or Media on a resolver shadows any identically named field on the
// embedded Resolver. That shadowing is silent: r.Categories inside queryResolver
// resolves to the method, not the collaborator, and the failure only appears at
// compile time as a missing method on the wrong type. The Store suffix makes the
// collision impossible rather than relying on noticing it.
type Resolver struct {
	// CatalogStore is the browse read model, served by product-listing-service.
	CatalogStore CatalogReader
	// SearchStore is the search read model, served by product-search-service.
	SearchStore SearchReader
	// CategoryStore is the category hierarchy, owned by category-service.
	CategoryStore CategoryReader
	// MediaStore is media-service.
	MediaStore MediaReader
	// ProductStore is the write side of product-service.
	ProductStore ProductWriter
	// ProjectionStore reports read-model progress. It is a separate interface
	// because it is the one collaborator that speaks HTTP: the two read services
	// expose their status endpoints over REST only.
	ProjectionStore ProjectionReporter

	// Observer receives resolver tracing events. Nil installs the no-op observer.
	//
	// Unexported because it is wiring rather than a collaborator: it is set once at
	// startup and never swapped, so exposing it would invite per-request mutation of
	// the root that every resolver shares.
	observer SpanObserver
}

// WithObserver returns the resolver with a tracing observer attached, for use in tests
// and at composition time.
func (r *Resolver) WithObserver(observer SpanObserver) *Resolver {
	r.observer = observer
	return r
}
