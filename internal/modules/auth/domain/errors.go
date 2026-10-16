// Package domain holds the auth error classification for presentation adapters.
package domain

import (
	"errors"
)

// Kind classifies an auth error for the presentation adapters.
type Kind int

const (
	KindInternal Kind = iota
	KindNotFound
	KindInvalid
	KindFailedPrecondition
	KindUnauthenticated
)

// Classify classifies err.
//
// The specific sentinels are checked before the general ones they wrap
// (ErrInvalidPIN wraps ErrInvalidEmployee, ErrExpired wraps ErrInvalidSession):
// a general-first order would shadow every specific answer with its bucket.
func Classify(err error) Kind {
	switch {
	case err == nil:
		return KindInternal
	case errors.Is(err, ErrEmployeeNotFound), errors.Is(err, ErrSessionNotFound):
		return KindNotFound
	case errors.Is(err, ErrInvalidCredentials):
		return KindUnauthenticated
	case errors.Is(err, ErrInvalidPIN), errors.Is(err, ErrExpired):
		return KindFailedPrecondition
	case errors.Is(err, ErrInvalidEmployee), errors.Is(err, ErrInvalidSession):
		return KindInvalid
	default:
		return KindInternal
	}
}

// Message returns a stable, client-facing message for err. Like Classify, it
// answers the specific sentinels first and the buckets they wrap last.
func Message(err error) string {
	switch {
	case errors.Is(err, ErrEmployeeNotFound):
		return "employee not found"
	case errors.Is(err, ErrSessionNotFound):
		return "session not found"
	case errors.Is(err, ErrInvalidCredentials):
		return "invalid credentials"
	case errors.Is(err, ErrInvalidPIN):
		return "PIN must be 6 digits"
	case errors.Is(err, ErrExpired):
		return "session has expired"
	case errors.Is(err, ErrInvalidTenant):
		return "tenant is required"
	case errors.Is(err, ErrInvalidID):
		return "id is required"
	case errors.Is(err, ErrInvalidName):
		return "name is required"
	case errors.Is(err, ErrInvalidRole):
		return "role is required"
	case errors.Is(err, ErrDuplicateEmail):
		return "email already exists"
	case errors.Is(err, ErrMissingCredential):
		return "credential is required"
	case errors.Is(err, ErrInvalidEmployee):
		return "invalid employee"
	case errors.Is(err, ErrInvalidSession):
		return "invalid session"
	default:
		return "something went wrong"
	}
}
