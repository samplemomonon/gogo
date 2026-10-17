// Package domain holds the auth aggregates: employees, sessions, merchants,
// merchant memberships, and invitations.
package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ReservedRole is a code-owned, account-level authority label. It is not the
// legacy gRPC/HTTP role value (admin/seller/viewer) nor a tenant RBAC role: it
// names which kind of account an employee holds inside the identity module.
//
// The set is derived, never stored: an employee who owns at least one merchant
// is a penjual, an employee who is a non-owner member of a merchant is a
// karyawan of it, and every account starts as a pembeli.

// Merchant is a seller's shop: the aggregate the auth module owns as the
// authoritative owner/relationship model the product platform can reference
// (products carry a merchant_id). One employee — the penjual — owns it; other
// employees join through invitations from that owner.
type Merchant struct {
	ID              string
	TenantID        string
	OwnerEmployeeID string
	Name            string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

var (
	ErrMerchantAlreadyExists = errors.New("merchant already exists")
)

// NewMerchant validates and creates a merchant owned by ownerEmployeeID.
func NewMerchant(tenant Tenant, id, ownerEmployeeID, name string) (*Merchant, error) {
	tenant = Tenant(strings.TrimSpace(string(tenant)))
	id = strings.TrimSpace(id)
	ownerEmployeeID = strings.TrimSpace(ownerEmployeeID)
	name = strings.TrimSpace(name)

	if tenant == "" {
		return nil, ErrInvalidTenant
	}
	if id == "" {
		return nil, fmt.Errorf("%w: id is required", ErrInvalidMerchant)
	}
	if ownerEmployeeID == "" {
		return nil, fmt.Errorf("%w: owner is required", ErrInvalidMerchant)
	}
	if name == "" {
		return nil, fmt.Errorf("%w: name is required", ErrInvalidMerchant)
	}

	now := time.Now()
	return &Merchant{
		ID:              id,
		TenantID:        string(tenant),
		OwnerEmployeeID: ownerEmployeeID,
		Name:            name,
		CreatedAt:       now,
		UpdatedAt:       now,
	}, nil
}

// Rename updates the merchant's display name.
func (m *Merchant) Rename(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidMerchant)
	}
	m.Name = name
	m.UpdatedAt = time.Now()
	return nil
}
