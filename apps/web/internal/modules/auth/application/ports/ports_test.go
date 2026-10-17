// Package ports_test pins the auth module's ports as a checked contract: every
// concrete adapter must compile against the port it serves, and the in-memory
// pair must work when reached through the interfaces compose and the gRPC
// adapter consume. A method renamed in a port, or dropped from an adapter,
// breaks this test build instead of breaking at runtime.
package ports_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"gogo/internal/modules/auth/application"
	"gogo/internal/modules/auth/application/ports"
	"gogo/internal/modules/auth/domain"
	"gogo/internal/modules/auth/infrastructure"
	authpostgres "gogo/internal/modules/auth/infrastructure/postgres"
)

// Compile-time assertions: each concrete type must satisfy the port it is
// bound to. The PostgreSQL stores are asserted here because no database is
// available to exercise them at runtime — the compiler still checks their
// whole method set.
var (
	_ ports.EmployeeStore = (*infrastructure.Store)(nil)
	_ ports.SessionStore  = (*infrastructure.Store)(nil)
	_ ports.MerchantStore = (*infrastructure.Store)(nil)
	_ ports.AuthService   = (*application.Service)(nil)
	_ ports.EmployeeStore = (*authpostgres.EmployeeStore)(nil)
	_ ports.SessionStore  = (*authpostgres.SessionStore)(nil)
	_ ports.MerchantStore = (*authpostgres.MerchantStore)(nil)
)

// TestInMemoryBackendSatisfiesThePorts drives the in-memory adapters through
// ports.AuthService and the store ports, the way compose hands them to the
// module and the gRPC adapter. It pins that the port is usable at runtime —
// registration, login, validation and revocation all dispatch through the
// interface, not just assign to it.
func TestInMemoryBackendSatisfiesThePorts(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := infrastructure.NewStore()
	var svc ports.AuthService = application.NewService(store, store, bcrypt.MinCost, time.Hour, store)

	employee, err := svc.RegisterEmployee(ctx, "tenant-a", "emp-1", "Budi", "budi@toko.com", "123456", domain.RoleSeller)
	if err != nil {
		t.Fatalf("RegisterEmployee through the port: %v", err)
	}
	if employee.TenantID != "tenant-a" {
		t.Errorf("employee TenantID = %q, want tenant-a", employee.TenantID)
	}

	session, _, err := svc.Login(ctx, "tenant-a", "budi@toko.com", "123456")
	if err != nil {
		t.Fatalf("Login through the port: %v", err)
	}

	validated, validatedEmployee, err := svc.ValidateSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("ValidateSession through the port: %v", err)
	}
	if validated.ID != session.ID || validatedEmployee.ID != "emp-1" {
		t.Errorf("ValidateSession = session %q employee %q, want the logged-in pair", validated.ID, validatedEmployee.ID)
	}

	if err := svc.Logout(ctx, session.ID); err != nil {
		t.Fatalf("Logout through the port: %v", err)
	}
	if _, _, err := svc.ValidateSession(ctx, session.ID); !errors.Is(err, domain.ErrExpired) {
		t.Errorf("ValidateSession after Logout = %v, want ErrExpired", err)
	}
}
