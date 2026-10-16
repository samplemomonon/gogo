package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"gogo/internal/modules/auth/application"
	"gogo/internal/modules/auth/application/ports"
	"gogo/internal/modules/auth/domain"
	"gogo/internal/modules/auth/infrastructure"
)

func newService() *application.Service {
	store := infrastructure.NewStore()
	return application.NewService(store, store, 0, time.Hour)
}

func TestRegisterEmployeeHashesPINAndAllowsLogin(t *testing.T) {
	svc := newService()
	ctx := context.Background()

	employee, err := svc.RegisterEmployee(ctx, "default", "emp-1", "Budi", "budi@toko.com", "123456", domain.RoleSeller)
	if err != nil {
		t.Fatalf("RegisterEmployee: %v", err)
	}
	if employee.PINHash == "123456" {
		t.Fatal("PIN stored in plaintext")
	}

	if _, _, err := svc.Login(ctx, "default", "budi@toko.com", "123456"); err != nil {
		t.Fatalf("Login with the correct PIN: %v", err)
	}
	if _, _, err := svc.Login(ctx, "default", "budi@toko.com", "654321"); !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("Login with the wrong PIN = %v, want ErrInvalidCredentials", err)
	}
}

func TestRegisterEmployeeRejectsMalformedPIN(t *testing.T) {
	svc := newService()

	_, err := svc.RegisterEmployee(context.Background(), "default", "emp-1", "Budi", "budi@toko.com", "12ab", domain.RoleSeller)
	if !errors.Is(err, domain.ErrInvalidPIN) {
		t.Fatalf("RegisterEmployee with a malformed PIN = %v, want ErrInvalidPIN", err)
	}
}

type failingEmployeeStore struct {
	ports.EmployeeStore
	err error
}

func (f failingEmployeeStore) GetByEmail(context.Context, domain.Tenant, string) (*domain.Employee, error) {
	return nil, f.err
}

func TestLoginSurfacesStoreFailure(t *testing.T) {
	boom := errors.New("database down")
	store := infrastructure.NewStore()
	svc := application.NewService(failingEmployeeStore{EmployeeStore: store, err: boom}, store, 0, time.Hour)

	_, _, err := svc.Login(context.Background(), "default", "budi@toko.com", "123456")
	if !errors.Is(err, boom) {
		t.Fatalf("Login = %v, want the store failure surfaced", err)
	}
	if errors.Is(err, domain.ErrEmployeeNotFound) {
		t.Fatal("Login masked a store failure as ErrEmployeeNotFound")
	}
}

func TestSetEmployeePINReplacesCredential(t *testing.T) {
	svc := newService()
	ctx := context.Background()

	if _, err := svc.RegisterEmployee(ctx, "default", "emp-1", "Budi", "budi@toko.com", "123456", domain.RoleSeller); err != nil {
		t.Fatalf("RegisterEmployee: %v", err)
	}

	if err := svc.SetEmployeePIN(ctx, "default", "emp-1", "111111"); err != nil {
		t.Fatalf("SetEmployeePIN: %v", err)
	}
	if _, _, err := svc.Login(ctx, "default", "budi@toko.com", "111111"); err != nil {
		t.Fatalf("Login with the new PIN: %v", err)
	}
	if err := svc.SetEmployeePIN(ctx, "default", "emp-1", "abc"); !errors.Is(err, domain.ErrInvalidPIN) {
		t.Fatalf("SetEmployeePIN with a malformed PIN = %v, want ErrInvalidPIN", err)
	}
}
