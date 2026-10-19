package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"gogo/internal/features/auth/application/ports"
	"gogo/internal/features/auth/domain"
)

var _ ports.MerchantStore = (*MerchantStore)(nil)

// MerchantStore is the PostgreSQL merchant relationship store.
type MerchantStore struct {
	pool *pgxpool.Pool
}

// NewMerchantStore creates a new PostgreSQL merchant store.
func NewMerchantStore(pool *pgxpool.Pool) *MerchantStore {
	return &MerchantStore{pool: pool}
}

func (s *MerchantStore) CreateMerchant(ctx context.Context, merchant *domain.Merchant) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO merchants (tenant_id, id, owner_employee_id, name, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (tenant_id, id) DO NOTHING
	`, merchant.TenantID, merchant.ID, merchant.OwnerEmployeeID, merchant.Name, merchant.CreatedAt, merchant.UpdatedAt)
	return err
}

func (s *MerchantStore) GetMerchant(ctx context.Context, tenant domain.Tenant, id string) (*domain.Merchant, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT tenant_id, id, owner_employee_id, name, created_at, updated_at
		FROM merchants WHERE tenant_id = $1 AND id = $2
	`, tenant, id)

	var m domain.Merchant
	if err := row.Scan(&m.TenantID, &m.ID, &m.OwnerEmployeeID, &m.Name, &m.CreatedAt, &m.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrMerchantNotFound
		}
		return nil, err
	}
	return &m, nil
}

func (s *MerchantStore) ListMerchantsForEmployee(ctx context.Context, tenant domain.Tenant, employeeID string) ([]*domain.Merchant, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT m.tenant_id, m.id, m.owner_employee_id, m.name, m.created_at, m.updated_at
		FROM merchants m
		JOIN merchant_memberships mm ON mm.tenant_id = m.tenant_id AND mm.merchant_id = m.id
		WHERE m.tenant_id = $1 AND mm.employee_id = $2
		ORDER BY m.created_at DESC
	`, tenant, employeeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*domain.Merchant
	for rows.Next() {
		var m domain.Merchant
		if err := rows.Scan(&m.TenantID, &m.ID, &m.OwnerEmployeeID, &m.Name, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, &m)
	}
	return out, rows.Err()
}

func (s *MerchantStore) CountOwnedMerchants(ctx context.Context, tenant domain.Tenant, employeeID string) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM merchant_memberships
		WHERE tenant_id = $1 AND employee_id = $2 AND role = 'owner'
	`, tenant, employeeID).Scan(&count)
	return count, err
}

func (s *MerchantStore) CreateMembership(ctx context.Context, membership *domain.Membership) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO merchant_memberships (tenant_id, merchant_id, employee_id, role, granted_by, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (tenant_id, merchant_id, employee_id) DO NOTHING
	`, membership.TenantID, membership.MerchantID, membership.EmployeeID, membership.Role, membership.GrantedBy, membership.CreatedAt)
	return err
}

func (s *MerchantStore) GetMembership(ctx context.Context, tenant domain.Tenant, merchantID, employeeID string) (*domain.Membership, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT tenant_id, merchant_id, employee_id, role, granted_by, created_at
		FROM merchant_memberships WHERE tenant_id = $1 AND merchant_id = $2 AND employee_id = $3
	`, tenant, merchantID, employeeID)

	var m domain.Membership
	if err := row.Scan(&m.TenantID, &m.MerchantID, &m.EmployeeID, &m.Role, &m.GrantedBy, &m.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrMembershipNotFound
		}
		return nil, err
	}
	return &m, nil
}

func (s *MerchantStore) ListMembers(ctx context.Context, tenant domain.Tenant, merchantID string) ([]*domain.Membership, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT tenant_id, merchant_id, employee_id, role, granted_by, created_at
		FROM merchant_memberships WHERE tenant_id = $1 AND merchant_id = $2
		ORDER BY (role = 'owner') DESC, created_at
	`, tenant, merchantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*domain.Membership
	for rows.Next() {
		var m domain.Membership
		if err := rows.Scan(&m.TenantID, &m.MerchantID, &m.EmployeeID, &m.Role, &m.GrantedBy, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &m)
	}
	return out, rows.Err()
}

func (s *MerchantStore) CountEmployeeMemberships(ctx context.Context, tenant domain.Tenant, employeeID string) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM merchant_memberships
		WHERE tenant_id = $1 AND employee_id = $2 AND role = 'karyawan'
	`, tenant, employeeID).Scan(&count)
	return count, err
}

func (s *MerchantStore) CreateInvitation(ctx context.Context, invitation *domain.Invitation) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO merchant_invitations
			(tenant_id, id, merchant_id, email, role, status, token, expires_at, invited_by, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (tenant_id, id) DO NOTHING
	`, invitation.TenantID, invitation.ID, invitation.MerchantID, invitation.Email, invitation.Role, invitation.Status, invitation.Token, invitation.ExpiresAt, invitation.InvitedBy, invitation.CreatedAt)
	return err
}

func (s *MerchantStore) GetInvitation(ctx context.Context, tenant domain.Tenant, id string) (*domain.Invitation, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT tenant_id, id, merchant_id, email, role, status, token, expires_at, invited_by, created_at, accepted_at, revoked_at
		FROM merchant_invitations WHERE tenant_id = $1 AND id = $2
	`, tenant, id)

	inv, err := scanInvitation(row)
	if err != nil {
		return nil, err
	}
	return inv, nil
}

func (s *MerchantStore) GetInvitationByToken(ctx context.Context, tenant domain.Tenant, token string) (*domain.Invitation, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT tenant_id, id, merchant_id, email, role, status, token, expires_at, invited_by, created_at, accepted_at, revoked_at
		FROM merchant_invitations WHERE tenant_id = $1 AND token = $2
	`, tenant, token)

	inv, err := scanInvitation(row)
	if err != nil {
		return nil, err
	}
	return inv, nil
}

func (s *MerchantStore) UpdateInvitation(ctx context.Context, invitation *domain.Invitation) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE merchant_invitations
		SET status = $3, accepted_at = $4, revoked_at = $5
		WHERE tenant_id = $1 AND id = $2
	`, invitation.TenantID, invitation.ID, invitation.Status, nullableTime(invitation.AcceptedAt), nullableTime(invitation.RevokedAt))
	return err
}

func (s *MerchantStore) GrantMerchantPermission(ctx context.Context, permission *domain.MerchantPermission) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `UPDATE merchant_permissions SET revoked_at = now()
		WHERE tenant_id = $1 AND merchant_id = $2 AND employee_id = $3 AND permission_key = $4 AND revoked_at IS NULL`,
		permission.TenantID, permission.MerchantID, permission.EmployeeID, permission.PermissionKey); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO merchant_permissions
		(tenant_id, merchant_id, employee_id, permission_key, granted_by, granted_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		permission.TenantID, permission.MerchantID, permission.EmployeeID, permission.PermissionKey, permission.GrantedBy, permission.GrantedAt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *MerchantStore) RevokeMerchantPermission(ctx context.Context, tenant domain.Tenant, merchantID, employeeID, permissionKey string) error {
	_, err := s.pool.Exec(ctx, `UPDATE merchant_permissions SET revoked_at = now()
		WHERE tenant_id = $1 AND merchant_id = $2 AND employee_id = $3 AND permission_key = $4 AND revoked_at IS NULL`,
		tenant, merchantID, employeeID, permissionKey)
	return err
}

func (s *MerchantStore) HasMerchantPermission(ctx context.Context, tenant domain.Tenant, merchantID, employeeID, permissionKey string) (bool, error) {
	var allowed bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM merchant_permissions
		WHERE tenant_id = $1 AND merchant_id = $2 AND employee_id = $3 AND permission_key = $4
		  AND revoked_at IS NULL
	)`, tenant, merchantID, employeeID, permissionKey).Scan(&allowed)
	return allowed, err
}

func (s *MerchantStore) ListMerchantPermissions(ctx context.Context, tenant domain.Tenant, merchantID, employeeID string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT permission_key FROM merchant_permissions
		WHERE tenant_id = $1 AND merchant_id = $2 AND employee_id = $3 AND revoked_at IS NULL
		ORDER BY permission_key
	`, tenant, merchantID, employeeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		out = append(out, key)
	}
	return out, rows.Err()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanInvitation(row rowScanner) (*domain.Invitation, error) {
	var inv domain.Invitation
	var acceptedAt, revokedAt *time.Time
	if err := row.Scan(&inv.TenantID, &inv.ID, &inv.MerchantID, &inv.Email, &inv.Role, &inv.Status, &inv.Token, &inv.ExpiresAt, &inv.InvitedBy, &inv.CreatedAt, &acceptedAt, &revokedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrInvitationNotFound
		}
		return nil, err
	}
	inv.AcceptedAt = acceptedAt
	inv.RevokedAt = revokedAt
	return &inv, nil
}

func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return *t
}
