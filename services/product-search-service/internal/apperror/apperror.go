// Package apperror defines the sentinel errors used to describe failures in
// domain terms. Layers wrap these with context using fmt.Errorf and %w so that
// callers can classify a failure with errors.Is instead of comparing error
// strings.
package apperror

import "errors"

var (
	// ErrNotFound indicates the requested product or variant does not exist.
	ErrNotFound = errors.New("not found")

	// ErrInvalidInput indicates the request was malformed or semantically invalid,
	// such as a negative price or an unusable slug.
	ErrInvalidInput = errors.New("invalid input")

	// ErrConflict indicates the request collided with the current state, such as
	// a slug or SKU that is already taken.
	ErrConflict = errors.New("conflict")

	// ErrUnavailable indicates a service this one depends on, such as
	// category-service, could not be reached or reported a fault. It is kept
	// distinct from a validation failure so a caller can retry rather than
	// treat the request as permanently bad.
	ErrUnavailable = errors.New("dependency unavailable")
)
