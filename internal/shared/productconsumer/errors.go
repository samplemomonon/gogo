package productconsumer

import "errors"

// The product platform's error vocabulary. These are the sentinels a surface can
// branch on with errors.Is, without importing any product service to learn what a
// rejection meant. They are deliberately transport-independent: a caller decides
// what to render, not the wire status code that produced it.
//
// They are distinct from the sentinels of gogo's own inventory and sales modules,
// which answer different questions (can this adjustment oversell? was this sale
// already voided?). Keeping the vocabularies separate is what stops a caller from
// treating "product not found" as "insufficient stock".
var (
	// ErrNotFound is a clean "no such product" answer. A read of an id that does
	// not exist, or a write against a row already deleted.
	ErrNotFound = errors.New("product not found")

	// ErrInvalid is a rejection of the request itself: a malformed field, a search
	// term under the minimum length, or a product whose category or media reference
	// the write side could not validate. Retrying an identical request will fail
	// identically.
	ErrInvalid = errors.New("invalid product request")

	// ErrConflict is a rejection because the write would collide with an existing
	// row — a duplicate slug, for instance.
	ErrConflict = errors.New("product conflict")

	// ErrFailedPrecondition is a rejection because a domain rule refused the write.
	// On the write side this includes a reference whose owning service answered but
	// does not have the row.
	ErrFailedPrecondition = errors.New("product precondition failed")

	// ErrProductPlatformUnavailable is "this dependency cannot be reached", whether
	// because its circuit is open, the transport is failing, or the service returned
	// a status this vocabulary does not name. It is the one sentinel the shared
	// convention guarantees, and it survives wrapping by construction.
	//
	// This is the sentinel a page degrades on: a storefront whose browse read model
	// is unreachable should render a stale-but-valid grid or an explicit error page,
	// not treat the outage as a bug in the request.
	ErrProductPlatformUnavailable = errors.New("product platform unavailable")
)
