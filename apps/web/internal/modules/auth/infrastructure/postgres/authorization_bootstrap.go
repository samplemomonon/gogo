package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const bootstrapPolicyRoleID = "system:policy-admin"

var ErrPolicyAdminAlreadyBootstrapped = errors.New("policy administrator already bootstrapped")

// BootstrapPolicyAdmin grants the three policy-administration capabilities to
// one active employee. The database session principal is recorded as the actor;
// callers cannot supply an actor name. Use an individually attributable
// operator database login for this one-time operation.
func BootstrapPolicyAdmin(ctx context.Context, pool *pgxpool.Pool, tenantID, employeeID, changeReference string) error {
	tenantID = strings.TrimSpace(tenantID)
	employeeID = strings.TrimSpace(employeeID)
	changeReference = strings.TrimSpace(changeReference)
	if pool == nil || tenantID == "" || employeeID == "" || changeReference == "" {
		return errors.New("authorization bootstrap requires database, tenant, employee, and change reference")
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin authorization bootstrap: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Serialize bootstrap attempts for this tenant, including attempts that
	// target different employees.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 814723))`, tenantID); err != nil {
		return fmt.Errorf("lock authorization bootstrap: %w", err)
	}
	var active bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM employees WHERE tenant_id = $1 AND id = $2 AND active
	)`, tenantID, employeeID).Scan(&active); err != nil {
		return fmt.Errorf("check bootstrap employee: %w", err)
	}
	if !active {
		return errors.New("bootstrap employee is missing or inactive in tenant")
	}

	var alreadyBootstrapped bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1
		FROM authorization_role_permissions rp
		JOIN authorization_roles r ON r.tenant_id = rp.tenant_id AND r.id = rp.role_id
		WHERE rp.tenant_id = $1
		  AND rp.permission_key = 'authz.role.manage'
		  AND rp.revoked_at IS NULL
		  AND r.active
	)`, tenantID).Scan(&alreadyBootstrapped); err != nil {
		return fmt.Errorf("check existing policy administrator: %w", err)
	}
	if alreadyBootstrapped {
		return ErrPolicyAdminAlreadyBootstrapped
	}

	if _, err := tx.Exec(ctx, `INSERT INTO authorization_roles (tenant_id, id, name, description, created_by)
		VALUES ($1, $2, 'Policy administrator', 'One-time bootstrap role for policy administration', session_user::TEXT)
		ON CONFLICT (tenant_id, id) DO NOTHING`, tenantID, bootstrapPolicyRoleID); err != nil {
		return fmt.Errorf("create policy administrator role: %w", err)
	}
	var roleExists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM authorization_roles WHERE tenant_id = $1 AND id = $2 AND active)`, tenantID, bootstrapPolicyRoleID).Scan(&roleExists); err != nil {
		return fmt.Errorf("verify policy administrator role: %w", err)
	}
	if !roleExists {
		return errors.New("reserved policy administrator role is already present but inactive")
	}

	for _, permission := range []string{"authz.role.manage", "authz.permission.manage", "authz.assignment.manage"} {
		if _, err := tx.Exec(ctx, `INSERT INTO authorization_role_permissions
			(tenant_id, grant_id, role_id, permission_key, granted_by, grant_reason)
			VALUES ($1, $2, $3, $4, session_user::TEXT, $5)`, tenantID, uuid.NewString(), bootstrapPolicyRoleID, permission, "one-time policy administrator bootstrap: "+changeReference); err != nil {
			return fmt.Errorf("grant policy capability %q: %w", permission, err)
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO authorization_employee_roles
		(tenant_id, assignment_id, employee_id, role_id, assigned_by, source, reason)
		VALUES ($1, $2, $3, $4, session_user::TEXT, 'bootstrap', $5)`, tenantID, uuid.NewString(), employeeID, bootstrapPolicyRoleID, "one-time policy administrator bootstrap: "+changeReference); err != nil {
		return fmt.Errorf("assign policy administrator role: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO authorization_admin_events
		(tenant_id, id, action, subject_id, role_id, actor_employee_id, actor_db_user, change_reference)
		VALUES ($1, $2, 'bootstrap-policy-admin', $3, $4, $3, session_user, $5)`, tenantID, uuid.NewString(), employeeID, bootstrapPolicyRoleID, changeReference); err != nil {
		return fmt.Errorf("record policy administrator bootstrap: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit authorization bootstrap: %w", err)
	}
	return nil
}
