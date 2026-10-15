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
func Classify(err error) Kind {
	switch {
	case err == nil:
		return KindInternal
	case errors.Is(err, ErrEmployeeNotFound), errors.Is(err, ErrSessionNotFound):
		return KindNotFound
	case errors.Is(err, ErrInvalidEmployee), errors.Is(err, ErrInvalidSession):
		return KindInvalid
	case errors.Is(err, ErrInvalidPIN), errors.Is(err, ErrExpired):
		return KindFailedPrecondition
	default:
		return KindInternal
	}
}

// Message returns a stable, client-facing message for err.
func Message(err error) string {
	switch {
	case errors.Is(err, ErrEmployeeNotFound):
		return "employee not found"
	case errors.Is(err, ErrSessionNotFound):
		return "session not found"
	case errors.Is(err, ErrInvalidEmployee):
		return "invalid employee"
	case errors.Is(err, ErrInvalidSession):
		return "invalid session"
	case errors.Is(err, ErrInvalidTenant):
		return "tenant is required"
	case errors.Is(err, ErrInvalidID):
		return "id is required"
	case errors.Is(err, ErrInvalidName):
		return "name is required"
	case errors.Is(err, ErrInvalidPIN):
		return "PIN must be 6 digits"
	case errors.Is(err, ErrInvalidRole):
		return "role is required"
	case errors.Is(err, ErrDuplicateEmail):
		return "email already exists"
	case errors.Is(err, ErrExpired):
		return "session has expired"
	default:
		return "something went wrong"
	}
}
