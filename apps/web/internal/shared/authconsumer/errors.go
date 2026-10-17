package authconsumer

import "errors"

// The auth.v1 error vocabulary, in the caller's language rather than gRPC's.
// A consumer branches on these; it never inspects a status code.
var (
	// ErrNotFound is a clean "no such employee or session" answer.
	ErrNotFound = errors.New("auth: not found")
	// ErrInvalid is a rejection of the request itself.
	ErrInvalid = errors.New("auth: invalid request")
	// ErrPrecondition is a refusal by a domain rule — a PIN of the wrong shape,
	// a session that has already expired.
	ErrPrecondition = errors.New("auth: refused")
	// ErrUnauthenticated is a refusal because the credentials did not match.
	ErrUnauthenticated = errors.New("auth: not authenticated")
	// ErrForbidden is an authenticated request without the requested authority.
	ErrForbidden = errors.New("auth: forbidden")
	// ErrPolicyConflict is a duplicate or conflicting policy mutation.
	ErrPolicyConflict = errors.New("auth: policy conflict")
	// ErrAuthUnavailable is "the auth service cannot be reached", whether the
	// circuit is open, the transport is failing, or the service answered with a
	// status this contract does not name. Callers degrade on it instead of
	// telling a user their password was wrong.
	ErrAuthUnavailable = errors.New("auth: unavailable")
)
