package domain_test

import (
	"strings"
	"testing"

	"gogo/internal/features/auth/domain"
)

func TestValidatePIN(t *testing.T) {
	for _, tc := range []struct {
		name string
		pin  string
		ok   bool
	}{
		{"six digits", "123456", true},
		{"too short", "12345", false},
		{"too long", "1234567", false},
		{"non digit", "12a456", false},
		{"empty", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := domain.ValidatePIN(tc.pin)
			if tc.ok && err != nil {
				t.Fatalf("ValidatePIN(%q) = %v, want nil", tc.pin, err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("ValidatePIN(%q) = nil, want error", tc.pin)
			}
		})
	}
}

func TestNewEmployeeStoresCredentialHashOpaquely(t *testing.T) {
	hash := strings.Repeat("x", 60)

	employee, err := domain.NewEmployee("default", "emp-1", "Budi", "budi@toko.com", hash, domain.RoleSeller)
	if err != nil {
		t.Fatalf("NewEmployee with a credential hash: %v", err)
	}
	if employee.PINHash != hash {
		t.Fatalf("PINHash = %q, want the provided credential hash %q", employee.PINHash, hash)
	}

	if _, err := domain.NewEmployee("default", "emp-2", "Budi", "budi@toko.com", "", domain.RoleSeller); err == nil {
		t.Fatal("NewEmployee with an empty credential = nil error, want error")
	}
}
