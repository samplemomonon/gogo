// This file pins BootstrapPolicyAdmin's hermetic contract: it validates every
// input before touching the database, and a bootstrap that cannot reach the
// database fails loudly — it never reports success and never masquerades as an
// already-bootstrapped tenant. The transactional ceremony behind a reachable
// database (advisory lock, active-employee gate, grants, audit event) needs a
// live PostgreSQL and stays covered by the operator procedure, not here.
package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	authpostgres "gogo/internal/features/auth/infrastructure/postgres"
)

func TestBootstrapPolicyAdminValidatesItsInputsBeforeTouchingTheDatabase(t *testing.T) {
	t.Parallel()

	// A non-nil pool over a closed port: if validation failed to stop the
	// call, the next step would be a dial attempt and the error would say so.
	pool := unreachablePool(t)

	tests := []struct {
		name     string
		tenant   string
		employee string
		change   string
	}{
		{name: "empty tenant", tenant: "", employee: "emp-1", change: "CHG-1"},
		{name: "whitespace tenant", tenant: "  ", employee: "emp-1", change: "CHG-1"},
		{name: "empty employee", tenant: "tenant-1", employee: "", change: "CHG-1"},
		{name: "whitespace employee", tenant: "tenant-1", employee: "\t", change: "CHG-1"},
		{name: "empty change reference", tenant: "tenant-1", employee: "emp-1", change: ""},
		{name: "whitespace change reference", tenant: "tenant-1", employee: "emp-1", change: " "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := authpostgres.BootstrapPolicyAdmin(context.Background(), pool, tt.tenant, tt.employee, tt.change)
			if err == nil || !strings.Contains(err.Error(), "authorization bootstrap requires") {
				t.Fatalf("err = %v, want the input-validation refusal", err)
			}
			if strings.Contains(err.Error(), "begin authorization bootstrap") {
				t.Fatal("validation must refuse before the transaction is opened")
			}
		})
	}
}

func TestBootstrapPolicyAdminRequiresAPool(t *testing.T) {
	t.Parallel()

	err := authpostgres.BootstrapPolicyAdmin(context.Background(), nil, "tenant-1", "emp-1", "CHG-1")
	if err == nil || !strings.Contains(err.Error(), "authorization bootstrap requires") {
		t.Fatalf("err = %v, want the input-validation refusal", err)
	}
}

func TestBootstrapPolicyAdminFailsClosedWithoutADatabase(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	err := authpostgres.BootstrapPolicyAdmin(ctx, unreachablePool(t), "tenant-1", "emp-1", "CHG-1")
	if err == nil {
		t.Fatal("a bootstrap against an unreachable database must fail, not silently pass")
	}
	if !strings.Contains(err.Error(), "begin authorization bootstrap") {
		t.Fatalf("err = %v, want the transaction-open failure", err)
	}
	if errors.Is(err, authpostgres.ErrPolicyAdminAlreadyBootstrapped) {
		t.Fatal("a dial failure must not masquerade as an already-bootstrapped tenant")
	}
}
