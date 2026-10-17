// Package islandkeys centralizes the name of the home page island so that
// the navigation handler (which renders it) and the page's bus subscriber
// (which invalidates its cached HTML) agree on the key without duplicating
// a magic string. The home page owns this island — it was lifted out of the
// catalog module along with the page, so catalog must not know about it.
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
