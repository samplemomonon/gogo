// Package application holds the auth facade: it composes the employee and session stores.
package application

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"golang.org/x/crypto/bcrypt"

	"gogo/internal/modules/auth/application/ports"
	"gogo/internal/modules/auth/domain"
)

var _ ports.AuthService = (*Service)(nil)

// Service implements ports.AuthService.
type Service struct {
	employees ports.EmployeeStore
	sessions  ports.SessionStore
	pinCost   int
	ttl       time.Duration
}

// NewService creates a new auth service.
func NewService(employees ports.EmployeeStore, sessions ports.SessionStore, pinCost int, ttl time.Duration) *Service {
	if pinCost <= 0 {
		pinCost = bcrypt.DefaultCost
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return &Service{employees: employees, sessions: sessions, pinCost: pinCost, ttl: ttl}
}

// generateID generates a random ID.
func generateID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// hashPIN hashes a PIN using bcrypt.
func (s *Service) hashPIN(pin string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(pin), s.pinCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// checkPIN verifies a PIN against a bcrypt hash.
func (s *Service) checkPIN(hash, pin string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pin))
}

// RegisterEmployee registers a new employee.
func (s *Service) RegisterEmployee(ctx context.Context, tenant domain.Tenant, id, name, email, pin string, role domain.Role) (*domain.Employee, error) {
	// Check for duplicate email
	existing, err := s.employees.GetByEmail(ctx, tenant, email)
	if err == nil && existing != nil {
		return nil, domain.ErrDuplicateEmail
	}

	if err := domain.ValidatePIN(pin); err != nil {
		return nil, err
	}

	hash, err := s.hashPIN(pin)
	if err != nil {
		return nil, err
	}

	employee, err := domain.NewEmployee(tenant, id, name, email, hash, role)
	if err != nil {
		return nil, err
	}

	if err := s.employees.Create(ctx, tenant, employee); err != nil {
		return nil, err
	}
	return employee, nil
}

// GetEmployee returns an employee by ID.
func (s *Service) GetEmployee(ctx context.Context, tenant domain.Tenant, id string) (*domain.Employee, error) {
	return s.employees.Get(ctx, tenant, id)
}

// GetEmployeeByEmail returns an employee by email.
func (s *Service) GetEmployeeByEmail(ctx context.Context, tenant domain.Tenant, email string) (*domain.Employee, error) {
	return s.employees.GetByEmail(ctx, tenant, email)
}

// UpdateEmployee updates an employee.
func (s *Service) UpdateEmployee(ctx context.Context, tenant domain.Tenant, id, name, email string, role domain.Role) (*domain.Employee, error) {
	employee, err := s.employees.Get(ctx, tenant, id)
	if err != nil {
		return nil, err
	}
	if err := employee.Update(name, email, role); err != nil {
		return nil, err
	}
	if err := s.employees.Update(ctx, tenant, employee); err != nil {
		return nil, err
	}
	return employee, nil
}

// ListEmployees lists employees.
func (s *Service) ListEmployees(ctx context.Context, tenant domain.Tenant, limit int) ([]*domain.Employee, error) {
	return s.employees.List(ctx, tenant, limit)
}

// SetEmployeePIN updates an employee's PIN.
func (s *Service) SetEmployeePIN(ctx context.Context, tenant domain.Tenant, id, pin string) error {
	employee, err := s.employees.Get(ctx, tenant, id)
	if err != nil {
		return err
	}
	if err := domain.ValidatePIN(pin); err != nil {
		return err
	}
	hash, err := s.hashPIN(pin)
	if err != nil {
		return err
	}
	if err := employee.SetPIN(hash); err != nil {
		return err
	}
	return s.employees.Update(ctx, tenant, employee)
}

// ActivateEmployee activates an employee.
func (s *Service) ActivateEmployee(ctx context.Context, tenant domain.Tenant, id string) error {
	employee, err := s.employees.Get(ctx, tenant, id)
	if err != nil {
		return err
	}
	employee.Activate()
	return s.employees.Update(ctx, tenant, employee)
}

// DeactivateEmployee deactivates an employee.
func (s *Service) DeactivateEmployee(ctx context.Context, tenant domain.Tenant, id string) error {
	employee, err := s.employees.Get(ctx, tenant, id)
	if err != nil {
		return err
	}
	employee.Deactivate()
	return s.employees.Update(ctx, tenant, employee)
}

// Login authenticates an employee and creates a session.
func (s *Service) Login(ctx context.Context, tenant domain.Tenant, email, pin string) (*domain.Session, *domain.Employee, error) {
	employee, err := s.employees.GetByEmail(ctx, tenant, email)
	if err != nil {
		return nil, nil, err
	}
	if !employee.Active {
		return nil, nil, domain.ErrInvalidEmployee
	}
	if err := s.checkPIN(employee.PINHash, pin); err != nil {
		return nil, nil, domain.ErrInvalidPIN
	}

	session, err := domain.NewSession(tenant, generateID(), employee.ID, s.ttl)
	if err != nil {
		return nil, nil, err
	}
	if err := s.sessions.CreateSession(ctx, session); err != nil {
		return nil, nil, err
	}
	return session, employee, nil
}

// Logout revokes a session.
func (s *Service) Logout(ctx context.Context, sessionID string) error {
	return s.sessions.RevokeSession(ctx, sessionID)
}

// ValidateSession validates a session and returns it with the employee.
func (s *Service) ValidateSession(ctx context.Context, sessionID string) (*domain.Session, *domain.Employee, error) {
	session, err := s.sessions.GetSession(ctx, sessionID)
	if err != nil {
		return nil, nil, err
	}
	if !session.IsValid() {
		return nil, nil, domain.ErrExpired
	}
	employee, err := s.employees.Get(ctx, domain.Tenant(session.TenantID), session.EmployeeID)
	if err != nil {
		return nil, nil, err
	}
	if !employee.Active {
		return nil, nil, domain.ErrInvalidEmployee
	}
	return session, employee, nil
}

// ExtendSession extends a session's TTL.
func (s *Service) ExtendSession(ctx context.Context, sessionID string) (*domain.Session, error) {
	session, err := s.sessions.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if !session.IsValid() {
		return nil, domain.ErrExpired
	}
	if err := session.Extend(s.ttl); err != nil {
		return nil, err
	}
	if err := s.sessions.ExtendSession(ctx, sessionID, s.ttl); err != nil {
		return nil, err
	}
	return session, nil
}
