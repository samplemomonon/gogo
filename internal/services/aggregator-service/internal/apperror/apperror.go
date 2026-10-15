// Package apperror defines the sentinel errors the aggregator uses to describe
// failures in terms a GraphQL client can act on.
//
// The distinction that matters most here is ErrUnavailable. An aggregator fans
// out to five services, so a single dependency being down must not be reported as
// a client mistake: the request was well formed and retrying it is reasonable.
// Collapsing that into ErrInvalidInput would tell callers to stop retrying
// something that has already succeeded once.
package apperror

import "errors"

var (
	// ErrNotFound indicates the requested entity does not exist in the owning
	// service.
	ErrNotFound = errors.New("not found")

	// ErrInvalidInput indicates the request was malformed or semantically invalid,
	// such as a negative price or an unusable slug. Retrying will not help.
	ErrInvalidInput = errors.New("invalid input")

	// ErrConflict indicates the request collided with current state, such as a
	// slug or SKU that is already taken.
	ErrConflict = errors.New("conflict")

	// ErrUnavailable indicates a downstream service could not be reached or
	// reported a fault. It is kept distinct from a validation failure precisely
	// so a client can tell "your request was wrong" from "try again".
	ErrUnavailable = errors.New("dependency unavailable")

	// ErrRejected indicates the request failed a hardening limit: query
	// complexity, depth, or size. It is a client error, but a different one from
	// ErrInvalidInput, because the fix is to send a smaller query rather than a
	// corrected one.
	ErrRejected = errors.New("rejected")
)

// IsNotFound reports whether err is, or wraps, the not-found sentinel.
//
// It exists because the read contracts answer 404 per id, so a batch caller needs
// to distinguish "this id is absent" from "the call failed". Those are opposite
// responses: the first is a null field, the second is an error the caller should
// see and retry.
func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound)
}

// InvalidInput is a client mistake with a message worth showing the client.
//
// It exists because ErrInvalidInput alone carries no explanation: a resolver
// knows *what* was wrong ("the search term is too short") and that is the single
// most useful thing in the response. Returning the bare sentinel would produce
// "invalid input", which tells the caller nothing they can act on.
//
// The distinction that matters is that the detail is client-safe by construction.
// Only messages written here, or extracted from a downstream InvalidArgument,
// become InvalidInput, so a message reaching a client through this type has been
// reviewed at the point it was written.
type InvalidInput struct {
	// Message explains the problem in terms the caller can fix.
	Message string
	// Field names the offending input when one is known, for clients that render
	// the error next to a form control.
	Field string
}

func (e *InvalidInput) Error() string { return e.Message }

// Unwrap reports ErrInvalidInput so errors.Is keeps working for callers that
// branch on the sentinel rather than the type.
func (e *InvalidInput) Unwrap() error { return ErrInvalidInput }
