// Package apperror defines the sentinel errors used to describe failures in
// domain terms. Layers wrap these with context using fmt.Errorf and %w so that
// callers can classify a failure with errors.Is instead of comparing error
// strings.
package apperror

import "errors"

var (
	// ErrNotFound indicates the requested entity does not exist.
	ErrNotFound = errors.New("not found")

	// ErrInvalidInput indicates the request was malformed or semantically invalid.
	ErrInvalidInput = errors.New("invalid input")

	// ErrCircularReference indicates the operation would create a cycle in the
	// category hierarchy.
	ErrCircularReference = errors.New("circular reference detected")
)
