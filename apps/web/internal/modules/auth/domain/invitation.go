package domain

import (
	"fmt"
	"strings"
	"time"
)

// InvitationStatus tracks the lifecycle of a merchant invitation.
type InvitationStatus string

const (
	InvitationPending  InvitationStatus = "pending"
	InvitationAccepted InvitationStatus = "accepted"
	InvitationRevoked  InvitationStatus = "revoked"
	InvitationExpired  InvitationStatus = "expired"
)

// Invitation is how a merchant owner adds a karyawan. It is created by the
// owner, targets an email, carries a one-time token, and expires.
type Invitation struct {
	ID         string
	TenantID   string
	MerchantID string
	Email      string
	Role       MerchantRole
	Status     InvitationStatus
	Token      string
	ExpiresAt  time.Time
	InvitedBy  string
	CreatedAt  time.Time
	AcceptedAt *time.Time
	RevokedAt  *time.Time
}

// NewInvitation validates and creates a pending invitation.
func NewInvitation(tenant Tenant, id, merchantID, email string, role MerchantRole, token string, ttl time.Duration, invitedBy string) (*Invitation, error) {
	tenant = Tenant(strings.TrimSpace(string(tenant)))
	id = strings.TrimSpace(id)
	merchantID = strings.TrimSpace(merchantID)
	email = strings.ToLower(strings.TrimSpace(email))
	token = strings.TrimSpace(token)
	invitedBy = strings.TrimSpace(invitedBy)

	if tenant == "" {
		return nil, ErrInvalidTenant
	}
	if id == "" {
		return nil, fmt.Errorf("%w: id is required", ErrInvalidMerchant)
	}
	if merchantID == "" {
		return nil, fmt.Errorf("%w: merchant id is required", ErrInvalidMerchant)
	}
	if email == "" || !strings.Contains(email, "@") {
		return nil, fmt.Errorf("%w: a valid email is required", ErrInvalidMerchant)
	}
	if role != MerchantRoleOwner && role != MerchantRoleEmployee {
		return nil, fmt.Errorf("%w: invited role must be owner or karyawan", ErrInvalidMerchant)
	}
	if token == "" {
		return nil, fmt.Errorf("%w: token is required", ErrInvalidMerchant)
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("%w: invitation ttl must be positive", ErrInvalidMerchant)
	}
	if invitedBy == "" {
		return nil, fmt.Errorf("%w: inviter is required", ErrInvalidMerchant)
	}

	now := time.Now()
	return &Invitation{
		ID:         id,
		TenantID:   string(tenant),
		MerchantID: merchantID,
		Email:      email,
		Role:       role,
		Status:     InvitationPending,
		Token:      token,
		ExpiresAt:  now.Add(ttl),
		InvitedBy:  invitedBy,
		CreatedAt:  now,
	}, nil
}

// ValidFor reports whether the invitation can still be accepted: it must be
// pending, not revoked, and not expired.
func (i *Invitation) ValidFor() bool {
	if i.Status != InvitationPending {
		return false
	}
	if i.RevokedAt != nil {
		return false
	}
	return time.Now().Before(i.ExpiresAt)
}

// Accept transitions the invitation to accepted and records when.
func (i *Invitation) Accept() error {
	if i.Status == InvitationAccepted {
		return ErrInvitationAlreadyAccepted
	}
	if i.Status == InvitationRevoked {
		return ErrInvitationAlreadyRevoked
	}
	if !time.Now().Before(i.ExpiresAt) {
		i.Status = InvitationExpired
		return ErrInvitationExpired
	}
	i.Status = InvitationAccepted
	now := time.Now()
	i.AcceptedAt = &now
	return nil
}

// Revoke withdraws a pending invitation.
func (i *Invitation) Revoke() error {
	if i.Status == InvitationAccepted {
		return ErrInvitationAlreadyAccepted
	}
	if i.Status == InvitationRevoked {
		return ErrInvitationAlreadyRevoked
	}
	i.Status = InvitationRevoked
	now := time.Now()
	i.RevokedAt = &now
	return nil
}
