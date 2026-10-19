package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"gogo/internal/features/auth/application/ports"
	"gogo/internal/features/auth/domain"
)

// AuthorizationRepository loads current tenant-scoped grants for the PDP.
type AuthorizationRepository struct {
	pool *pgxpool.Pool
}

func NewAuthorizationRepository(pool *pgxpool.Pool) *AuthorizationRepository {
	return &AuthorizationRepository{pool: pool}
}

func (r *AuthorizationRepository) HasPermission(ctx context.Context, tenantID, employeeID, permissionKey string) (bool, error) {
	var allowed bool
	err := r.pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1
		FROM authorization_employee_roles er
		JOIN employees e ON e.tenant_id = er.tenant_id AND e.id = er.employee_id AND e.active
		JOIN authorization_roles role ON role.tenant_id = er.tenant_id AND role.id = er.role_id AND role.active
		JOIN authorization_role_permissions rp ON rp.tenant_id = er.tenant_id AND rp.role_id = er.role_id
		JOIN authorization_permissions p ON p.permission_key = rp.permission_key
		WHERE er.tenant_id = $1
		  AND er.employee_id = $2
		  AND p.permission_key = $3
		  AND er.revoked_at IS NULL
		  AND er.valid_from <= now()
		  AND (er.expires_at IS NULL OR er.expires_at > now())
		  AND rp.revoked_at IS NULL
		  AND rp.granted_at <= now()
		  AND (rp.expires_at IS NULL OR rp.expires_at > now())
	)`, tenantID, employeeID, permissionKey).Scan(&allowed)
	return allowed, err
}

func (r *AuthorizationRepository) beginAuthorized(ctx context.Context, tenantID, actorID, permissionKey string) (pgx.Tx, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	rollback := func(err error) (pgx.Tx, error) {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 814723))`, tenantID); err != nil {
		return rollback(err)
	}
	var allowed bool
	err = tx.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM authorization_employee_roles er
		JOIN employees e ON e.tenant_id = er.tenant_id AND e.id = er.employee_id AND e.active
		JOIN authorization_roles r ON r.tenant_id = er.tenant_id AND r.id = er.role_id AND r.active
		JOIN authorization_role_permissions rp ON rp.tenant_id = er.tenant_id AND rp.role_id = er.role_id
		WHERE er.tenant_id = $1 AND er.employee_id = $2
		  AND rp.permission_key = $3
		  AND er.revoked_at IS NULL AND er.valid_from <= now()
		  AND (er.expires_at IS NULL OR er.expires_at > now())
		  AND rp.revoked_at IS NULL AND rp.granted_at <= now()
		  AND (rp.expires_at IS NULL OR rp.expires_at > now())
	)`, tenantID, actorID, permissionKey).Scan(&allowed)
	if err != nil {
		return rollback(err)
	}
	if !allowed {
		return rollback(domain.ErrPolicyDenied)
	}
	return tx, nil
}

func finishPolicyTx(ctx context.Context, tx pgx.Tx, tenantID, actorID, action, subjectID, roleID, reason, changeReference string) error {
	if err := recordPolicyEvent(ctx, tx, tenantID, actorID, action, subjectID, roleID, reason, changeReference); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func recordPolicyEvent(ctx context.Context, tx pgx.Tx, tenantID, actorID, action, subjectID, roleID, reason, changeReference string) error {
	if _, err := tx.Exec(ctx, `INSERT INTO authorization_admin_events
		(tenant_id, id, action, subject_id, role_id, actor_employee_id, actor_db_user, reason, change_reference)
		VALUES ($1, $2, $3, $4, $5, $6, session_user, $7, $8)`, tenantID, uuid.NewString(), action, subjectID, roleID, actorID, reason, changeReference); err != nil {
		return fmt.Errorf("record authorization administration event: %w", err)
	}
	return nil
}

func rowsToStrings(rows pgx.Rows) ([]string, error) {
	defer rows.Close()
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func ensurePolicyAdminContinuity(ctx context.Context, tx pgx.Tx, tenantID string) error {
	var intact bool
	err := tx.QueryRow(ctx, `SELECT NOT EXISTS (
		SELECT permission_key FROM authorization_permissions
		WHERE permission_key IN ('authz.role.manage','authz.permission.manage','authz.assignment.manage')
		EXCEPT
		SELECT DISTINCT rp.permission_key
		FROM authorization_role_permissions rp
		JOIN authorization_roles r ON r.tenant_id=rp.tenant_id AND r.id=rp.role_id AND r.active
		JOIN authorization_employee_roles er ON er.tenant_id=rp.tenant_id AND er.role_id=rp.role_id
		JOIN employees e ON e.tenant_id=er.tenant_id AND e.id=er.employee_id AND e.active
		WHERE rp.tenant_id=$1 AND rp.permission_key LIKE 'authz.%'
		  AND rp.revoked_at IS NULL AND rp.granted_at <= now() AND (rp.expires_at IS NULL OR rp.expires_at > now())
		  AND er.revoked_at IS NULL AND er.valid_from <= now() AND (er.expires_at IS NULL OR er.expires_at > now())
	)`, tenantID).Scan(&intact)
	if err != nil {
		return err
	}
	if !intact {
		return domain.ErrPolicyConflict
	}
	return nil
}

func mapPolicyDBError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return domain.ErrPolicyConflict
		case "23503":
			return domain.ErrPolicyTargetNotFound
		case "23514":
			return domain.ErrInvalidPolicyChange
		}
	}
	return err
}

func (r *AuthorizationRepository) CreateRole(ctx context.Context, tenantID, actorID, roleID, name, description, changeReference string) error {
	tx, err := r.beginAuthorized(ctx, tenantID, actorID, "authz.role.manage")
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `INSERT INTO authorization_roles (tenant_id, id, name, description, created_by)
		VALUES ($1, $2, $3, $4, $5)`, tenantID, roleID, name, description, actorID); err != nil {
		return mapPolicyDBError(err)
	}
	return finishPolicyTx(ctx, tx, tenantID, actorID, "create-role", "", roleID, "", changeReference)
}

func (r *AuthorizationRepository) GrantPermission(ctx context.Context, tenantID, actorID, roleID, permissionKey string, expiresAt *time.Time, reason, changeReference string) (string, error) {
	tx, err := r.beginAuthorized(ctx, tenantID, actorID, "authz.permission.manage")
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var roleActive, permissionExists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM authorization_roles WHERE tenant_id=$1 AND id=$2 AND active),
		EXISTS(SELECT 1 FROM authorization_permissions WHERE permission_key=$3)`, tenantID, roleID, permissionKey).Scan(&roleActive, &permissionExists); err != nil {
		return "", err
	}
	if !roleActive || !permissionExists {
		return "", domain.ErrPolicyTargetNotFound
	}
	// Expired grants are closed out before regranting so the partial unique
	// index remains an accurate history-preserving constraint.
	expiredRows, err := tx.Query(ctx, `UPDATE authorization_role_permissions
		SET revoked_at=now(), revoked_by=$4
		WHERE tenant_id=$1 AND role_id=$2 AND permission_key=$3 AND revoked_at IS NULL
		  AND expires_at IS NOT NULL AND expires_at <= now() RETURNING grant_id`, tenantID, roleID, permissionKey, actorID)
	if err != nil {
		return "", err
	}
	expiredIDs, err := rowsToStrings(expiredRows)
	if err != nil {
		return "", err
	}
	for _, expiredID := range expiredIDs {
		if err := recordPolicyEvent(ctx, tx, tenantID, actorID, "close-expired-permission-grant", expiredID, roleID, "Automatically closed an expired grant before regranting.", changeReference); err != nil {
			return "", err
		}
	}
	grantID := uuid.NewString()
	if _, err := tx.Exec(ctx, `INSERT INTO authorization_role_permissions
		(tenant_id, grant_id, role_id, permission_key, expires_at, granted_by, grant_reason)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`, tenantID, grantID, roleID, permissionKey, expiresAt, actorID, reason); err != nil {
		return "", mapPolicyDBError(err)
	}
	if err := finishPolicyTx(ctx, tx, tenantID, actorID, "grant-permission:"+permissionKey, "", roleID, reason, changeReference); err != nil {
		return "", err
	}
	return grantID, nil
}

func (r *AuthorizationRepository) RevokePermission(ctx context.Context, tenantID, actorID, grantID, reason, changeReference string) error {
	tx, err := r.beginAuthorized(ctx, tenantID, actorID, "authz.permission.manage")
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var roleID, permissionKey string
	err = tx.QueryRow(ctx, `UPDATE authorization_role_permissions SET revoked_at=now(), revoked_by=$3,
		grant_reason=concat_ws(chr(10), grant_reason, 'Revoke reason: ' || $4)
		WHERE tenant_id=$1 AND grant_id=$2 AND revoked_at IS NULL RETURNING role_id, permission_key`, tenantID, grantID, actorID, reason).Scan(&roleID, &permissionKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrPolicyTargetNotFound
	}
	if err != nil {
		return err
	}
	if len(permissionKey) > 6 && permissionKey[:6] == "authz." {
		if err := ensurePolicyAdminContinuity(ctx, tx, tenantID); err != nil {
			return err
		}
	}
	return finishPolicyTx(ctx, tx, tenantID, actorID, "revoke-permission", grantID, roleID, reason, changeReference)
}

func (r *AuthorizationRepository) AssignRole(ctx context.Context, tenantID, actorID, employeeID, roleID string, validFrom, expiresAt *time.Time, reason, changeReference string) (string, error) {
	tx, err := r.beginAuthorized(ctx, tenantID, actorID, "authz.assignment.manage")
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var targetActive, roleActive bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM employees WHERE tenant_id=$1 AND id=$2 AND active),
		EXISTS(SELECT 1 FROM authorization_roles WHERE tenant_id=$1 AND id=$3 AND active)`, tenantID, employeeID, roleID).Scan(&targetActive, &roleActive); err != nil {
		return "", err
	}
	if !targetActive || !roleActive {
		return "", domain.ErrPolicyTargetNotFound
	}
	expiredRows, err := tx.Query(ctx, `UPDATE authorization_employee_roles SET revoked_at=now(), revoked_by=$4
		WHERE tenant_id=$1 AND employee_id=$2 AND role_id=$3 AND revoked_at IS NULL
		  AND expires_at IS NOT NULL AND expires_at <= now() RETURNING assignment_id`, tenantID, employeeID, roleID, actorID)
	if err != nil {
		return "", err
	}
	expiredIDs, err := rowsToStrings(expiredRows)
	if err != nil {
		return "", err
	}
	for _, expiredID := range expiredIDs {
		if err := recordPolicyEvent(ctx, tx, tenantID, actorID, "close-expired-role-assignment", employeeID, roleID, "Automatically closed an expired assignment before reassignment.", changeReference+":"+expiredID); err != nil {
			return "", err
		}
	}
	assignmentID := uuid.NewString()
	if _, err := tx.Exec(ctx, `INSERT INTO authorization_employee_roles
		(tenant_id, assignment_id, employee_id, role_id, valid_from, expires_at, assigned_by, source, reason)
		VALUES ($1,$2,$3,$4,COALESCE($5,now()),$6,$7,'admin',$8)`, tenantID, assignmentID, employeeID, roleID, validFrom, expiresAt, actorID, reason); err != nil {
		return "", mapPolicyDBError(err)
	}
	if err := finishPolicyTx(ctx, tx, tenantID, actorID, "assign-role", employeeID, roleID, reason, changeReference); err != nil {
		return "", err
	}
	return assignmentID, nil
}

func (r *AuthorizationRepository) RevokeRoleAssignment(ctx context.Context, tenantID, actorID, assignmentID, reason, changeReference string) error {
	tx, err := r.beginAuthorized(ctx, tenantID, actorID, "authz.assignment.manage")
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var employeeID, roleID string
	err = tx.QueryRow(ctx, `UPDATE authorization_employee_roles SET revoked_at=now(), revoked_by=$3,
		reason=concat_ws(chr(10), reason, 'Revoke reason: ' || $4)
		WHERE tenant_id=$1 AND assignment_id=$2 AND revoked_at IS NULL RETURNING employee_id, role_id`, tenantID, assignmentID, actorID, reason).Scan(&employeeID, &roleID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrPolicyTargetNotFound
	}
	if err != nil {
		return err
	}
	if err := ensurePolicyAdminContinuity(ctx, tx, tenantID); err != nil {
		return err
	}
	return finishPolicyTx(ctx, tx, tenantID, actorID, "revoke-role-assignment", employeeID, roleID, reason, changeReference)
}

func (r *AuthorizationRepository) ListRoles(ctx context.Context, tenantID string) ([]ports.AuthorizationRole, error) {
	rows, err := r.pool.Query(ctx, `SELECT r.id, r.name, r.description, rp.permission_key
		FROM authorization_roles r
		LEFT JOIN authorization_role_permissions rp
		  ON rp.tenant_id=r.tenant_id AND rp.role_id=r.id AND rp.revoked_at IS NULL
		 AND rp.granted_at <= now() AND (rp.expires_at IS NULL OR rp.expires_at > now())
		WHERE r.tenant_id=$1 AND r.active
		ORDER BY r.name, rp.permission_key`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	roles := make([]ports.AuthorizationRole, 0)
	indices := make(map[string]int)
	for rows.Next() {
		var id, name, description string
		var permission *string
		if err := rows.Scan(&id, &name, &description, &permission); err != nil {
			return nil, err
		}
		index, ok := indices[id]
		if !ok {
			index = len(roles)
			indices[id] = index
			roles = append(roles, ports.AuthorizationRole{ID: id, Name: name, Description: description, Permissions: make([]string, 0)})
		}
		if permission != nil {
			roles[index].Permissions = append(roles[index].Permissions, *permission)
		}
	}
	return roles, rows.Err()
}

func (r *AuthorizationRepository) ListAssignments(ctx context.Context, tenantID, employeeID string, includeHistory bool) ([]ports.RoleAssignment, error) {
	var employeeExists bool
	if err := r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM employees WHERE tenant_id=$1 AND id=$2)`, tenantID, employeeID).Scan(&employeeExists); err != nil {
		return nil, err
	}
	if !employeeExists {
		return nil, domain.ErrPolicyTargetNotFound
	}
	rows, err := r.pool.Query(ctx, `SELECT assignment_id, employee_id, role_id, reason,
		assigned_at, valid_from, expires_at, revoked_at, COALESCE(assigned_by, ''), source
		FROM authorization_employee_roles
		WHERE tenant_id=$1 AND employee_id=$2
		  AND ($3 OR (revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now())))
		ORDER BY assigned_at DESC, assignment_id`, tenantID, employeeID, includeHistory)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	assignments := make([]ports.RoleAssignment, 0)
	for rows.Next() {
		var assignment ports.RoleAssignment
		if err := rows.Scan(&assignment.ID, &assignment.EmployeeID, &assignment.RoleID, &assignment.Reason,
			&assignment.AssignedAt, &assignment.ValidFrom, &assignment.ExpiresAt, &assignment.RevokedAt, &assignment.AssignedBy, &assignment.Source); err != nil {
			return nil, err
		}
		assignments = append(assignments, assignment)
	}
	return assignments, rows.Err()
}

// ListTenantAssignments returns the tenant's complete assignment history in a stable keyset order.
func (r *AuthorizationRepository) ListTenantAssignments(ctx context.Context, tenantID string, limit int, afterAssignedAt *time.Time, afterID string) ([]ports.RoleAssignment, bool, error) {
	if limit < 1 {
		limit = 1
	}
	rows, err := r.pool.Query(ctx, `SELECT assignment_id, employee_id, role_id, reason, assigned_at, valid_from, expires_at, revoked_at, COALESCE(assigned_by, ''), source
		FROM authorization_employee_roles WHERE tenant_id=$1
		AND ($2::timestamptz IS NULL OR (assigned_at, assignment_id) < ($2, $3))
		ORDER BY assigned_at DESC, assignment_id DESC LIMIT $4`, tenantID, afterAssignedAt, afterID, limit)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	items := make([]ports.RoleAssignment, 0, limit)
	for rows.Next() {
		var item ports.RoleAssignment
		if err := rows.Scan(&item.ID, &item.EmployeeID, &item.RoleID, &item.Reason, &item.AssignedAt, &item.ValidFrom, &item.ExpiresAt, &item.RevokedAt, &item.AssignedBy, &item.Source); err != nil {
			return nil, false, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return items, len(items) == limit, nil
}

func (r *AuthorizationRepository) ListAdminEvents(ctx context.Context, tenantID string, limit int, afterTime *time.Time, afterID string) ([]ports.AuthorizationAdminEvent, bool, error) {
	if limit < 1 {
		limit = 1
	}
	rows, err := r.pool.Query(ctx, `SELECT id, action, subject_id, role_id, actor_employee_id, actor_db_user, reason, change_reference, occurred_at
		FROM authorization_admin_events WHERE tenant_id=$1
		AND ($2::timestamptz IS NULL OR (occurred_at, id) < ($2, $3))
		ORDER BY occurred_at DESC, id DESC LIMIT $4`, tenantID, afterTime, afterID, limit)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	events := make([]ports.AuthorizationAdminEvent, 0, limit)
	for rows.Next() {
		var event ports.AuthorizationAdminEvent
		if err := rows.Scan(&event.ID, &event.Action, &event.SubjectID, &event.RoleID, &event.ActorEmployeeID, &event.ActorDatabaseUser, &event.Reason, &event.ChangeReference, &event.OccurredAt); err != nil {
			return nil, false, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return events, len(events) == limit, nil
}
