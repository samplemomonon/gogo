package infrastructure

import (
	"context"
	"sort"

	"gogo/internal/features/auth/domain"
)

// merchantKey names a merchant within a tenant.
func merchantKey(tenant domain.Tenant, id string) string {
	return string(tenant) + ":" + id
}

// membershipKey names an employee's membership in a merchant.
func membershipKey(tenant domain.Tenant, merchantID, employeeID string) string {
	return string(tenant) + ":" + merchantID + ":" + employeeID
}

// merchantPermissionKey names one merchant-scoped capability grant.
func merchantPermissionKey(tenant domain.Tenant, merchantID, employeeID, permissionKey string) string {
	return string(tenant) + ":" + merchantID + ":" + employeeID + ":" + permissionKey
}

// CreateMerchant stores a new merchant.
func (r *Store) CreateMerchant(ctx context.Context, merchant *domain.Merchant) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := merchantKey(domain.Tenant(merchant.TenantID), merchant.ID)
	if _, exists := r.merchants[key]; exists {
		return domain.ErrMerchantAlreadyExists
	}
	r.merchants[key] = merchant
	return nil
}

// GetMerchant returns a merchant by id.
func (r *Store) GetMerchant(ctx context.Context, tenant domain.Tenant, id string) (*domain.Merchant, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	merchant, ok := r.merchants[merchantKey(tenant, id)]
	if !ok {
		return nil, domain.ErrMerchantNotFound
	}
	return merchant, nil
}

// ListMerchantsForEmployee returns the merchants the employee is a member of.
func (r *Store) ListMerchantsForEmployee(ctx context.Context, tenant domain.Tenant, employeeID string) ([]*domain.Merchant, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]*domain.Merchant, 0)
	for _, membership := range r.memberships {
		if membership.TenantID != string(tenant) || membership.EmployeeID != employeeID {
			continue
		}
		if merchant, ok := r.merchants[merchantKey(tenant, membership.MerchantID)]; ok {
			out = append(out, merchant)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// CountOwnedMerchants returns how many merchants the employee owns.
func (r *Store) CountOwnedMerchants(ctx context.Context, tenant domain.Tenant, employeeID string) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	count := 0
	for _, membership := range r.memberships {
		if membership.TenantID == string(tenant) && membership.EmployeeID == employeeID && membership.IsOwner() {
			count++
		}
	}
	return count, nil
}

// CreateMembership stores a new membership.
func (r *Store) CreateMembership(ctx context.Context, membership *domain.Membership) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := membershipKey(domain.Tenant(membership.TenantID), membership.MerchantID, membership.EmployeeID)
	if _, exists := r.memberships[key]; exists {
		return domain.ErrMembershipExists
	}
	r.memberships[key] = membership
	return nil
}

// GetMembership returns an employee's membership in a merchant.
func (r *Store) GetMembership(ctx context.Context, tenant domain.Tenant, merchantID, employeeID string) (*domain.Membership, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	membership, ok := r.memberships[membershipKey(tenant, merchantID, employeeID)]
	if !ok {
		return nil, domain.ErrMembershipNotFound
	}
	return membership, nil
}

// ListMembers returns a merchant's roster, owners first.
func (r *Store) ListMembers(ctx context.Context, tenant domain.Tenant, merchantID string) ([]*domain.Membership, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]*domain.Membership, 0)
	for _, membership := range r.memberships {
		if membership.TenantID == string(tenant) && membership.MerchantID == merchantID {
			out = append(out, membership)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsOwner() != out[j].IsOwner() {
			return out[i].IsOwner()
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

// CountEmployeeMemberships returns how many merchant memberships the employee
// holds (excluding owned merchants' owner memberships).
func (r *Store) CountEmployeeMemberships(ctx context.Context, tenant domain.Tenant, employeeID string) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	count := 0
	for _, membership := range r.memberships {
		if membership.TenantID == string(tenant) && membership.EmployeeID == employeeID && !membership.IsOwner() {
			count++
		}
	}
	return count, nil
}

// CreateInvitation stores a new invitation.
func (r *Store) CreateInvitation(ctx context.Context, invitation *domain.Invitation) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := string(invitation.TenantID) + ":" + invitation.ID
	if _, exists := r.invitations[key]; exists {
		return domain.ErrInvalidMerchant
	}
	r.invitations[key] = invitation
	r.invitationTok[invitation.Token] = key
	return nil
}

// GetInvitation returns an invitation by id.
func (r *Store) GetInvitation(ctx context.Context, tenant domain.Tenant, id string) (*domain.Invitation, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	invitation, ok := r.invitations[string(tenant)+":"+id]
	if !ok {
		return nil, domain.ErrInvitationNotFound
	}
	return invitation, nil
}

// GetInvitationByToken returns a pending invitation by its one-time token.
func (r *Store) GetInvitationByToken(ctx context.Context, tenant domain.Tenant, token string) (*domain.Invitation, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	key, ok := r.invitationTok[token]
	if !ok {
		return nil, domain.ErrInvitationNotFound
	}
	invitation, ok := r.invitations[key]
	if !ok || invitation.TenantID != string(tenant) {
		return nil, domain.ErrInvitationNotFound
	}
	return invitation, nil
}

// UpdateInvitation persists an invitation's lifecycle state.
func (r *Store) UpdateInvitation(ctx context.Context, invitation *domain.Invitation) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := string(invitation.TenantID) + ":" + invitation.ID
	if _, ok := r.invitations[key]; !ok {
		return domain.ErrInvitationNotFound
	}
	r.invitations[key] = invitation
	return nil
}

// GrantMerchantPermission stores a merchant-scoped capability grant for a
// member, closing out any prior active grant for the same key first.
func (r *Store) GrantMerchantPermission(ctx context.Context, permission *domain.MerchantPermission) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := merchantPermissionKey(domain.Tenant(permission.TenantID), permission.MerchantID, permission.EmployeeID, permission.PermissionKey)
	if existing, ok := r.merchantPerms[key]; ok {
		existing.Revoke()
	}
	r.merchantPerms[key] = permission
	return nil
}

// RevokeMerchantPermission closes an active merchant-scoped grant.
func (r *Store) RevokeMerchantPermission(ctx context.Context, tenant domain.Tenant, merchantID, employeeID, permissionKey string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := merchantPermissionKey(tenant, merchantID, employeeID, permissionKey)
	permission, ok := r.merchantPerms[key]
	if !ok {
		return nil
	}
	permission.Revoke()
	return nil
}

// HasMerchantPermission reports whether the member holds an active grant.
func (r *Store) HasMerchantPermission(ctx context.Context, tenant domain.Tenant, merchantID, employeeID, permissionKey string) (bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	permission, ok := r.merchantPerms[merchantPermissionKey(tenant, merchantID, employeeID, permissionKey)]
	if !ok {
		return false, nil
	}
	return permission.Active(), nil
}

// ListMerchantPermissions returns a member's active merchant-scoped grants.
func (r *Store) ListMerchantPermissions(ctx context.Context, tenant domain.Tenant, merchantID, employeeID string) ([]string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]string, 0)
	for _, permission := range r.merchantPerms {
		if permission.TenantID != string(tenant) || permission.MerchantID != merchantID || permission.EmployeeID != employeeID {
			continue
		}
		if permission.Active() {
			out = append(out, permission.PermissionKey)
		}
	}
	sort.Strings(out)
	return out, nil
}
