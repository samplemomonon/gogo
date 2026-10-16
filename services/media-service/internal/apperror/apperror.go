// Package apperror defines the sentinel errors used to describe failures in
// domain terms. Layers wrap these with context using fmt.Errorf and %w so that
// callers can classify a failure with errors.Is instead of comparing error
// strings.
package apperror

import "errors"

var (
	// ErrNotFound indicates the requested media record does not exist.
	ErrNotFound = errors.New("not found")

	// ErrInvalidInput indicates the request was malformed or semantically
	// invalid, for example a missing filename or an unsupported content type.
	ErrInvalidInput = errors.New("invalid input")

	// ErrConflict indicates the request collided with the current state, such as
	// reusing an object key that is already taken.
	ErrConflict = errors.New("conflict")

	// ErrStorage indicates the object store rejected or could not serve the
	// operation. It is deliberately distinct from ErrNotFound: a missing object
	// in the bucket is an upload that never completed, not a server fault.
	ErrStorage = errors.New("object store error")

	// ErrNotReady indicates the media record exists but its object has not been
	// uploaded yet, so no download URL can be issued.
	ErrNotReady = errors.New("media is not ready")
)
