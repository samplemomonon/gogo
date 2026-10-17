package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// MerchantRole is an employee's relationship to one merchant.
type MerchantRole string

const (
	// MerchantRoleOwner is the penjual: the merchant's owner. Owners may manage
	// the merchant's products by default.
	MerchantRoleOwner MerchantRole = "owner"
	// MerchantRoleEmployee is a karyawan: invited by the owner, a member with
	// no product authority until the owner grants it.
	MerchantRoleEmployee MerchantRole = "karyawan"
)

// Membership is one employee's role inside one merchant. It is the auth
// module's authoritative store relationship, the ReBAC provider the ADR
// reserves for a domain with a real owner and lifecycle.
type Membership struct {
	TenantID   string
	MerchantID string
	EmployeeID string
	Role       MerchantRole
	GrantedBy  string
	CreatedAt  time.Time
}

var (
	ErrMembershipExists = errors.New("membership already exists")
)

// NewMembership validates and creates a merchant membership.
func NewMembership(tenant Tenant, merchantID, employeeID, grantedBy string, role MerchantRole) (*Membership, error) {
	tenant = Tenant(strings.TrimSpace(string(tenant)))
	merchantID = strings.TrimSpace(merchantID)
	employeeID = strings.TrimSpace(employeeID)
	grantedBy = strings.TrimSpace(grantedBy)

	if tenant == "" {
		return nil, ErrInvalidTenant
	}
	if merchantID == "" {
		return nil, fmt.Errorf("%w: merchant id is required", ErrInvalidMerchant)
	}
	if employeeID == "" {
		return nil, fmt.Errorf("%w: employee id is required", ErrInvalidMerchant)
	}
	if role != MerchantRoleOwner && role != MerchantRoleEmployee {
		return nil, fmt.Errorf("%w: membership role must be owner or karyawan", ErrInvalidMerchant)
	}

	return &Membership{
		TenantID:   string(tenant),
		MerchantID: merchantID,
		EmployeeID: employeeID,
		Role:       role,
		GrantedBy:  grantedBy,
		CreatedAt:  time.Now(),
	}, nil
}

// IsOwner reports whether the membership is the merchant's owner.
func (m *Membership) IsOwner() bool { return m.Role == MerchantRoleOwner }

// MerchantPermission is a merchant-scoped capability granted to one member
// (usually a karyawan). A karyawan holds no product authority until the owner
// grants a capability such as seller.catalog.manage on the merchant.
type MerchantPermission struct {
	TenantID      string
	MerchantID    string
	EmployeeID    string
	PermissionKey string
	GrantedBy     string
	GrantedAt     time.Time
	RevokedAt     *time.Time
}

// NewMerchantPermission validates and creates a merchant-scoped permission
// grant for a member.
func NewMerchantPermission(tenant Tenant, merchantID, employeeID, permissionKey, grantedBy string) (*MerchantPermission, error) {
	tenant = Tenant(strings.TrimSpace(string(tenant)))
	merchantID = strings.TrimSpace(merchantID)
	employeeID = strings.TrimSpace(employeeID)
	permissionKey = strings.TrimSpace(permissionKey)
	grantedBy = strings.TrimSpace(grantedBy)

	if tenant == "" {
		return nil, ErrInvalidTenant
	}
	if merchantID == "" {
		return nil, fmt.Errorf("%w: merchant id is required", ErrInvalidMerchant)
	}
	if employeeID == "" {
		return nil, fmt.Errorf("%w: employee id is required", ErrInvalidMerchant)
	}
	if permissionKey == "" {
		return nil, fmt.Errorf("%w: permission key is required", ErrInvalidMerchant)
	}

	return &MerchantPermission{
		TenantID:      string(tenant),
		MerchantID:    merchantID,
		EmployeeID:    employeeID,
		PermissionKey: permissionKey,
		GrantedBy:     grantedBy,
		GrantedAt:     time.Now(),
	}, nil
}

// Revoke marks the permission grant as revoked.
func (p *MerchantPermission) Revoke() {
	if p.RevokedAt == nil {
		now := time.Now()
		p.RevokedAt = &now
	}
}

// Active reports whether the grant is currently in force.
func (p *MerchantPermission) Active() bool { return p.RevokedAt == nil }
