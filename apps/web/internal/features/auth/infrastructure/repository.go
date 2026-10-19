// Package infrastructure provides the in-memory auth store.
package infrastructure

import (
	"context"
	"sort"
	"sync"
	"time"

	"gogo/internal/features/auth/application/ports"
	"gogo/internal/features/auth/domain"
)

var _ ports.EmployeeStore = (*Store)(nil)
var _ ports.SessionStore = (*Store)(nil)
var _ ports.MerchantStore = (*Store)(nil)

// Store is the in-memory auth store.
type Store struct {
	mu        sync.RWMutex
	employees map[string]*domain.Employee
	sessions  map[string]*domain.Session
	byEmail   map[string]string // email -> employeeID

	merchants     map[string]*domain.Merchant           // tenant:merchantID
	memberships   map[string]*domain.Membership         // tenant:merchantID:employeeID
	invitations   map[string]*domain.Invitation         // tenant:invitationID
	invitationTok map[string]string                     // token -> tenant:invitationID
	merchantPerms map[string]*domain.MerchantPermission // tenant:merchantID:employeeID:permissionKey
}

func NewStore() *Store {
	return &Store{
		employees:     make(map[string]*domain.Employee),
		sessions:      make(map[string]*domain.Session),
		byEmail:       make(map[string]string),
		merchants:     make(map[string]*domain.Merchant),
		memberships:   make(map[string]*domain.Membership),
		invitations:   make(map[string]*domain.Invitation),
		invitationTok: make(map[string]string),
		merchantPerms: make(map[string]*domain.MerchantPermission),
	}
}

// EmployeeStore methods

func (r *Store) Create(ctx context.Context, tenant domain.Tenant, employee *domain.Employee) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := r.employeeKey(tenant, employee.ID)
	if _, exists := r.employees[key]; exists {
		return domain.ErrInvalidEmployee
	}
	if _, exists := r.byEmail[string(tenant)+":"+employee.Email]; exists {
		return domain.ErrDuplicateEmail
	}
	r.employees[key] = employee
	r.byEmail[string(tenant)+":"+employee.Email] = employee.ID
	return nil
}

func (r *Store) Get(ctx context.Context, tenant domain.Tenant, id string) (*domain.Employee, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	key := r.employeeKey(tenant, id)
	employee, ok := r.employees[key]
	if !ok {
		return nil, domain.ErrEmployeeNotFound
	}
	return employee, nil
}

func (r *Store) GetByEmail(ctx context.Context, tenant domain.Tenant, email string) (*domain.Employee, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	employeeID, ok := r.byEmail[string(tenant)+":"+email]
	if !ok {
		return nil, domain.ErrEmployeeNotFound
	}
	return r.employees[r.employeeKey(tenant, employeeID)], nil
}

func (r *Store) Update(ctx context.Context, tenant domain.Tenant, employee *domain.Employee) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := r.employeeKey(tenant, employee.ID)
	existing, ok := r.employees[key]
	if !ok {
		return domain.ErrEmployeeNotFound
	}
	// Check email uniqueness if changed
	if existing.Email != employee.Email {
		if _, exists := r.byEmail[string(tenant)+":"+employee.Email]; exists {
			return domain.ErrDuplicateEmail
		}
		delete(r.byEmail, string(tenant)+":"+existing.Email)
		r.byEmail[string(tenant)+":"+employee.Email] = employee.ID
	}
	r.employees[key] = employee
	return nil
}

func (r *Store) List(ctx context.Context, tenant domain.Tenant, limit int) ([]*domain.Employee, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if limit <= 0 {
		limit = 100
	}
	out := make([]*domain.Employee, 0, len(r.employees))
	for id := range r.employees {
		employee := r.employees[id]
		if employee.TenantID != string(tenant) {
			continue
		}
		out = append(out, employee)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// SessionStore methods

func (r *Store) CreateSession(ctx context.Context, session *domain.Session) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.sessions[session.ID] = session
	return nil
}

func (r *Store) GetSession(ctx context.Context, id string) (*domain.Session, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	session, ok := r.sessions[id]
	if !ok {
		return nil, domain.ErrSessionNotFound
	}
	return session, nil
}

func (r *Store) RevokeSession(ctx context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	session, ok := r.sessions[id]
	if !ok {
		return domain.ErrSessionNotFound
	}
	session.Revoke()
	return nil
}

func (r *Store) ExtendSession(ctx context.Context, id string, ttl time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	session, ok := r.sessions[id]
	if !ok {
		return domain.ErrSessionNotFound
	}
	if err := session.Extend(ttl); err != nil {
		return err
	}
	return nil
}

func (r *Store) employeeKey(tenant domain.Tenant, id string) string {
	return string(tenant) + ":" + id
}
