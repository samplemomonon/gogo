// Package postgres provides the PostgreSQL implementation of the auth ports.
package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"gogo/internal/modules/auth/application/ports"
	"gogo/internal/modules/auth/domain"
)

var _ ports.EmployeeStore = (*EmployeeStore)(nil)
var _ ports.SessionStore = (*SessionStore)(nil)

// EmployeeStore is the PostgreSQL employee store.
type EmployeeStore struct {
	pool *pgxpool.Pool
}

// NewEmployeeStore creates a new PostgreSQL employee store.
func NewEmployeeStore(pool *pgxpool.Pool) *EmployeeStore {
	return &EmployeeStore{pool: pool}
}

func (s *EmployeeStore) Create(ctx context.Context, tenant domain.Tenant, employee *domain.Employee) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO employees (tenant_id, id, name, email, role, pin_hash, active, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (tenant_id, id) DO NOTHING
	`, tenant, employee.ID, employee.Name, employee.Email, employee.Role, employee.PINHash, employee.Active, employee.CreatedAt, employee.UpdatedAt)
	if err != nil {
		return err
	}
	return nil
}

func (s *EmployeeStore) Get(ctx context.Context, tenant domain.Tenant, id string) (*domain.Employee, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT tenant_id, id, name, email, role, pin_hash, active, created_at, updated_at
		FROM employees
		WHERE tenant_id = $1 AND id = $2
	`, tenant, id)

	var e domain.Employee
	if err := row.Scan(&e.TenantID, &e.ID, &e.Name, &e.Email, &e.Role, &e.PINHash, &e.Active, &e.CreatedAt, &e.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrEmployeeNotFound
		}
		return nil, err
	}
	return &e, nil
}

func (s *EmployeeStore) GetByEmail(ctx context.Context, tenant domain.Tenant, email string) (*domain.Employee, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT tenant_id, id, name, email, role, pin_hash, active, created_at, updated_at
		FROM employees
		WHERE tenant_id = $1 AND email = $2
	`, tenant, email)

	var e domain.Employee
	if err := row.Scan(&e.TenantID, &e.ID, &e.Name, &e.Email, &e.Role, &e.PINHash, &e.Active, &e.CreatedAt, &e.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrEmployeeNotFound
		}
		return nil, err
	}
	return &e, nil
}

func (s *EmployeeStore) Update(ctx context.Context, tenant domain.Tenant, employee *domain.Employee) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE employees
		SET name = $3, email = $4, role = $5, pin_hash = $6, active = $7, updated_at = $8
		WHERE tenant_id = $1 AND id = $2
	`, tenant, employee.ID, employee.Name, employee.Email, employee.Role, employee.PINHash, employee.Active, employee.UpdatedAt)
	return err
}

func (s *EmployeeStore) List(ctx context.Context, tenant domain.Tenant, limit int) ([]*domain.Employee, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT tenant_id, id, name, email, role, pin_hash, active, created_at, updated_at
		FROM employees
		WHERE tenant_id = $1
		ORDER BY created_at DESC
		LIMIT $2
	`, tenant, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*domain.Employee
	for rows.Next() {
		var e domain.Employee
		if err := rows.Scan(&e.TenantID, &e.ID, &e.Name, &e.Email, &e.Role, &e.PINHash, &e.Active, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}

// SessionStore is the PostgreSQL session store.
type SessionStore struct {
	pool *pgxpool.Pool
}

// NewSessionStore creates a new PostgreSQL session store.
func NewSessionStore(pool *pgxpool.Pool) *SessionStore {
	return &SessionStore{pool: pool}
}

func (s *SessionStore) CreateSession(ctx context.Context, session *domain.Session) error {
	var revokedAt interface{}
	if session.RevokedAt != nil {
		revokedAt = *session.RevokedAt
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO sessions (id, tenant_id, employee_id, created_at, expires_at, revoked_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (id) DO NOTHING
	`, session.ID, session.TenantID, session.EmployeeID, session.CreatedAt, session.ExpiresAt, revokedAt)
	return err
}

func (s *SessionStore) GetSession(ctx context.Context, id string) (*domain.Session, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, tenant_id, employee_id, created_at, expires_at, revoked_at
		FROM sessions
		WHERE id = $1
	`, id)

	var sess domain.Session
	var revokedAt *time.Time
	if err := row.Scan(&sess.ID, &sess.TenantID, &sess.EmployeeID, &sess.CreatedAt, &sess.ExpiresAt, &revokedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrSessionNotFound
		}
		return nil, err
	}
	if revokedAt != nil {
		sess.RevokedAt = revokedAt
	}
	return &sess, nil
}

func (s *SessionStore) RevokeSession(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE sessions
		SET revoked_at = $1
		WHERE id = $2
	`, time.Now(), id)
	return err
}

func (s *SessionStore) ExtendSession(ctx context.Context, id string, ttl time.Duration) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE sessions
		SET expires_at = $1
		WHERE id = $2
	`, time.Now().Add(ttl), id)
	return err
}
