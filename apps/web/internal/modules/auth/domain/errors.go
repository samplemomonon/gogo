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
	KindForbidden
	KindConflict
)

// Merchant, membership and invitation sentinels. A merchant is an aggregate the
// auth module owns; a membership is an employee's role inside one merchant; an
// invitation is how an owner adds a karyawan. These answer the merchant-scoped
// authorization questions the tenant-scoped RBAC model cannot (see
// docs/ADR_IDENTITY_AUTHORIZATION.md — the ReBAC relationship provider).

var (
	// Merchant errors
	ErrMerchantNotFound         = errors.New("merchant not found")
	ErrInvalidMerchant          = errors.New("invalid merchant")
	ErrMerchantPermissionDenied = errors.New("merchant permission denied")

	// Membership errors
	ErrMembershipNotFound = errors.New("membership not found")
	ErrMembershipNotOwner = errors.New("merchant owner only")

	// Invitation errors
	ErrInvitationNotFound        = errors.New("invitation not found")
	ErrInvitationExpired         = errors.New("invitation expired")
	ErrInvitationAlreadyAccepted = errors.New("invitation already accepted")
	ErrInvitationAlreadyRevoked  = errors.New("invitation already revoked")
)

// Classify classifies err. The specific sentinels are checked before the
// general ones they wrap (ErrInvalidPIN wraps ErrInvalidEmployee, ErrExpired
// wraps ErrInvalidSession): a general-first order would shadow every specific
// answer with its bucket.
func Classify(err error) Kind {
	switch {
	case err == nil:
		return KindInternal
	case errors.Is(err, ErrEmployeeNotFound), errors.Is(err, ErrSessionNotFound),
		errors.Is(err, ErrPolicyTargetNotFound), errors.Is(err, ErrMerchantNotFound),
		errors.Is(err, ErrMembershipNotFound), errors.Is(err, ErrInvitationNotFound):
		return KindNotFound
	case errors.Is(err, ErrInvalidCredentials):
		return KindUnauthenticated
	case errors.Is(err, ErrInvalidPIN), errors.Is(err, ErrExpired), errors.Is(err, ErrInvitationExpired):
		return KindFailedPrecondition
	case errors.Is(err, ErrInvalidEmployee), errors.Is(err, ErrInvalidSession),
		errors.Is(err, ErrInvalidMerchant), errors.Is(err, ErrMembershipNotOwner),
		errors.Is(err, ErrInvalidPolicyChange):
		return KindInvalid
	case errors.Is(err, ErrPolicyDenied), errors.Is(err, ErrMerchantPermissionDenied):
		return KindForbidden
	case errors.Is(err, ErrPolicyConflict), errors.Is(err, ErrInvitationAlreadyAccepted),
		errors.Is(err, ErrInvitationAlreadyRevoked):
		return KindConflict
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
	case errors.Is(err, ErrMerchantNotFound):
		return "merchant not found"
	case errors.Is(err, ErrMembershipNotFound):
		return "membership not found"
	case errors.Is(err, ErrInvitationNotFound):
		return "invitation not found"
	case errors.Is(err, ErrInvalidCredentials):
		return "invalid credentials"
	case errors.Is(err, ErrInvalidPIN):
		return "PIN must be 6 digits"
	case errors.Is(err, ErrExpired):
		return "session has expired"
	case errors.Is(err, ErrInvitationExpired):
		return "invitation has expired"
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
	case errors.Is(err, ErrInvalidMerchant):
		return "invalid merchant"
	case errors.Is(err, ErrMembershipNotOwner):
		return "merchant owner only"
	case errors.Is(err, ErrPolicyDenied), errors.Is(err, ErrMerchantPermissionDenied):
		return "permission denied"
	case errors.Is(err, ErrPolicyConflict), errors.Is(err, ErrInvitationAlreadyAccepted):
		return "already accepted"
	case errors.Is(err, ErrInvitationAlreadyRevoked):
		return "already revoked"
	case errors.Is(err, ErrPolicyTargetNotFound):
		return "policy target not found"
	case errors.Is(err, ErrInvalidPolicyChange):
		return "invalid policy change"
	default:
		return "something went wrong"
	}
}
