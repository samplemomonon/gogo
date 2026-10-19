// Package islandkeys centralizes the cache-key prefixes of the home page's
// islands, so the handler that renders an island and the cache keys derived
// from its name (page number or product id suffixes) agree on the same string
// without duplicating a magic value. The home page owns these islands — they
// were lifted out of the catalog module along with the page, so catalog must
// not know about them. Nothing invalidates them on events: the write side
// lives in another process, so staleness is bounded by the rendering
// contract's TTL (see home.New).
package islandkeys

// HomeGrid is the cache-key prefix of the public product grid on "/". The
// grid is paginated (/?page=N), so each page caches under
// HomeGrid + ":p" + N. Rendered with revalidation (ISR); the write side
// lives in another process, so staleness is bounded by the rendering
// contract's TTL and no event invalidates it (see home.New).
const HomeGrid = "home.product-grid"

// ProductDetail is the cache-key prefix of the product detail region on
// "/catalog/{id}": each product caches under ProductDetail + ":" + id, keyed
// by the id so one product's entry can never serve another. Staleness is
// bounded by the rendering contract's TTL, like the grid.
const ProductDetail = "home.product-detail"
