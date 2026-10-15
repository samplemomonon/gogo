// Package domain holds the auth aggregate: sessions for authenticated employees.
package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrSessionNotFound = errors.New("session not found")

	ErrInvalidSession = errors.New("invalid session")
	ErrExpired        = fmt.Errorf("%w: session has expired", ErrInvalidSession)
)

// Session represents an authenticated employee session.
type Session struct {
	ID         string
	TenantID   string
	EmployeeID string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	RevokedAt  *time.Time
}

// NewSession creates a new session for an employee.
func NewSession(tenant Tenant, id, employeeID string, ttl time.Duration) (*Session, error) {
	tenant = Tenant(strings.TrimSpace(string(tenant)))
	id = strings.TrimSpace(id)
	employeeID = strings.TrimSpace(employeeID)

	if tenant == "" {
		return nil, ErrInvalidSession
	}
	if id == "" {
		return nil, ErrInvalidSession
	}
	if employeeID == "" {
		return nil, ErrInvalidSession
	}
	if ttl <= 0 {
		return nil, ErrInvalidSession
	}

	now := time.Now()
	return &Session{
		ID:         id,
		TenantID:   string(tenant),
		EmployeeID: employeeID,
		CreatedAt:  now,
		ExpiresAt:  now.Add(ttl),
	}, nil
}

// IsValid checks if the session is still valid (not expired, not revoked).
func (s *Session) IsValid() bool {
	if s.RevokedAt != nil {
		return false
	}
	return time.Now().Before(s.ExpiresAt)
}

// Revoke marks the session as revoked.
func (s *Session) Revoke() {
	now := time.Now()
	s.RevokedAt = &now
}

// Extend extends the session expiry by the given duration.
func (s *Session) Extend(ttl time.Duration) error {
	if !s.IsValid() {
		return ErrExpired
	}
	s.ExpiresAt = time.Now().Add(ttl)
	return nil
}
