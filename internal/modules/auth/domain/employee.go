// Package domain holds the auth aggregate: employees and their credentials.
package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Tenant names the website an employee belongs to.
type Tenant string

var (
	ErrEmployeeNotFound = errors.New("employee not found")

	ErrInvalidEmployee   = errors.New("invalid employee")
	ErrInvalidTenant     = fmt.Errorf("%w: tenant is required", ErrInvalidEmployee)
	ErrInvalidID         = fmt.Errorf("%w: id is required", ErrInvalidEmployee)
	ErrInvalidName       = fmt.Errorf("%w: name is required", ErrInvalidEmployee)
	ErrInvalidPIN        = fmt.Errorf("%w: PIN must be 6 digits", ErrInvalidEmployee)
	ErrInvalidRole       = fmt.Errorf("%w: role is required", ErrInvalidEmployee)
	ErrDuplicateEmail    = fmt.Errorf("%w: email already exists", ErrInvalidEmployee)
	ErrMissingCredential = fmt.Errorf("%w: credential is required", ErrInvalidEmployee)

	// ErrInvalidCredentials is a sign-in refusal: the address or PIN presented
	// did not match. It deliberately does not wrap ErrInvalidEmployee — that
	// bucket means the request was malformed, while this one means the caller
	// is not authenticated, and the adapters answer the two differently.
	ErrInvalidCredentials = errors.New("invalid credentials")
)

// Role represents an employee's role.
type Role string

const (
	RoleAdmin  Role = "admin"
	RoleSeller Role = "seller"
	RoleViewer Role = "viewer"
)

// Employee represents an employee who can log into the seller console.
type Employee struct {
	TenantID  string
	ID        string
	Name      string
	Email     string
	Role      Role
	PINHash   string // bcrypt hash of 6-digit PIN
	Active    bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewEmployee validates and creates a new employee. credentialHash is the
// already-hashed credential: hashing is an infrastructure concern, so the
// domain stores it opaquely and never re-interprets it as a PIN. Validate the
// plaintext with ValidatePIN before hashing it.
func NewEmployee(tenant Tenant, id, name, email, credentialHash string, role Role) (*Employee, error) {
	tenant = Tenant(strings.TrimSpace(string(tenant)))
	name = strings.TrimSpace(name)
	email = strings.ToLower(strings.TrimSpace(email))

	if tenant == "" {
		return nil, ErrInvalidTenant
	}
	if strings.TrimSpace(id) == "" {
		return nil, ErrInvalidID
	}
	if name == "" {
		return nil, ErrInvalidName
	}
	if email == "" || !strings.Contains(email, "@") {
		return nil, ErrInvalidName
	}
	if strings.TrimSpace(credentialHash) == "" {
		return nil, ErrMissingCredential
	}
	if role == "" {
		return nil, ErrInvalidRole
	}

	return &Employee{
		TenantID:  string(tenant),
		ID:        id,
		Name:      name,
		Email:     email,
		Role:      role,
		PINHash:   credentialHash,
		Active:    true,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}, nil
}

// ValidatePIN reports whether pin is a well-formed credential (exactly six
// digits). It validates the plaintext a user typed; the stored value is an
// opaque hash and is never passed here.
func ValidatePIN(pin string) error {
	if !isValidPIN(pin) {
		return ErrInvalidPIN
	}
	return nil
}

// isValidPIN checks if the PIN is exactly 6 digits.
func isValidPIN(pin string) bool {
	if len(pin) != 6 {
		return false
	}
	for _, c := range pin {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// Update updates the employee's mutable fields.
func (e *Employee) Update(name, email string, role Role) error {
	name = strings.TrimSpace(name)
	email = strings.ToLower(strings.TrimSpace(email))

	if name == "" {
		return ErrInvalidName
	}
	if email == "" || !strings.Contains(email, "@") {
		return ErrInvalidName
	}
	if role == "" {
		return ErrInvalidRole
	}

	e.Name = name
	e.Email = email
	e.Role = role
	e.UpdatedAt = time.Now()
	return nil
}

// SetPIN replaces the stored credential with an already-hashed one. Validate
// the plaintext with ValidatePIN before hashing it.
func (e *Employee) SetPIN(credentialHash string) error {
	if strings.TrimSpace(credentialHash) == "" {
		return ErrMissingCredential
	}
	e.PINHash = credentialHash
	e.UpdatedAt = time.Now()
	return nil
}

// Activate activates the employee.
func (e *Employee) Activate() {
	e.Active = true
	e.UpdatedAt = time.Now()
}

// Deactivate deactivates the employee.
func (e *Employee) Deactivate() {
	e.Active = false
	e.UpdatedAt = time.Now()
}
