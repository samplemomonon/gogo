// Package islandkeys centralizes the name of the home page island so that
// the navigation handler (which renders it) and the page's bus subscriber
// (which invalidates its cached HTML) agree on the key without duplicating
// a magic string. The home page owns this island — it was lifted out of the
// catalog module along with the page, so catalog must not know about it.
package islandkeys

// HomeGrid is the cache-key prefix of the public product grid on "/". The
// grid is paginated (/?page=N), so each page caches under
// HomeGrid + ":p" + N; an event invalidates the whole prefix so no page ever
// serves stale markup. Rendered with revalidation (ISR); invalidated whenever
// catalog.product.* events are observed on the shared bus.
const HomeGrid = "home.product-grid"
