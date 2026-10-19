// Package ports defines what the home page needs from the product platform: a
// read-only product browse, and free-text search over the same catalogue. It is
// the remote consumer's contract — the home team owns this interface, the
// product platform owns the implementations behind the productlisting.v1 and
// productsearch.v1 gRPC contracts (consumed by infrastructure/listingclient).
//
// Home code depends on this port — never on a product service's internals, and
// never on a wire contract.
package ports

import (
	"context"

	"gogo/internal/features/home/ui/application/results"
)

// Query carries the storefront's paging and search options. Offset is computed by
// the page from its page number; the browse read model pages by offset and has no
// keyset cursor.
type Query struct {
	// Term is the free-text search term. Empty means "browse", which is a
	// different question answered by a different service — see the port's contract.
	Term string
	// Offset skips products before the page.
	Offset int
	// Limit caps the page size.
	Limit int
}

// Listing is the read-only capability the home page consumes over its remote
// boundary. It is deliberately narrow — narrower even than the seller console's
// catalog port: the home page is a read-only storefront, so the port carries
// exactly the reads it needs and nothing it could misuse (no mutations, no
// single-product lookups it never performs).
type Listing interface {
	// ListPage returns one page plus its paging metadata (total count).
	//
	// A non-empty q.Term routes to the search read model, which ranks results and
	// requires a term of at least three characters; a shorter or empty term is
	// answered as a browse, because search cannot serve it and browse can. That
	// fallback is the port's contract rather than an implementation detail, so the
	// home page never has to know which service answered.
	ListPage(ctx context.Context, q Query) (results.Listing, error)
	// GetProduct returns the storefront's read model for a single product.
	GetProduct(ctx context.Context, id string) (*results.Product, error)
}
