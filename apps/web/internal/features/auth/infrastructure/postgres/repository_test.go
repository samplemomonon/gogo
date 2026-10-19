package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"gogo/internal/features/auth/domain"
	authpostgres "gogo/internal/features/auth/infrastructure/postgres"
)

// TestEmployeeStoreSurfacesDialErrors pins the repository error contract on a
// dead database: every method returns the dial error and none of them masks
// it as a domain "not found" — only a real pgx.ErrNoRows may map to
// ErrEmployeeNotFound. Nothing here panics on the nil-with-error path.
func TestEmployeeStoreSurfacesDialErrors(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	store := authpostgres.NewEmployeeStore(unreachablePool(t))
	emp := &domain.Employee{
		TenantID:  "tenant-a",
		ID:        "emp-1",
		Name:      "Budi",
		Email:     "budi@toko.com",
		Role:      domain.RoleSeller,
		PINHash:   "hash",
		Active:    true,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	if err := store.Create(ctx, "tenant-a", emp); err == nil {
		t.Error("Create against a dead database = nil, want a dial error")
	}

	if _, err := store.Get(ctx, "tenant-a", "emp-1"); err == nil {
		t.Error("Get against a dead database = nil, want a dial error")
	} else if errors.Is(err, domain.ErrEmployeeNotFound) {
		t.Error("Get masked a dial error as ErrEmployeeNotFound")
	}

	if _, err := store.GetByEmail(ctx, "tenant-a", "budi@toko.com"); err == nil {
		t.Error("GetByEmail against a dead database = nil, want a dial error")
	} else if errors.Is(err, domain.ErrEmployeeNotFound) {
		t.Error("GetByEmail masked a dial error as ErrEmployeeNotFound")
	}

	if _, err := store.List(ctx, "tenant-a", 10); err == nil {
		t.Error("List against a dead database = nil, want a dial error")
	}

	if err := store.Update(ctx, "tenant-a", emp); err == nil {
		t.Error("Update against a dead database = nil, want a dial error")
	}
}

// TestSessionStoreSurfacesDialErrors pins the same contract for sessions:
// every method returns the dial error, and GetSession does not mask it as
// ErrSessionNotFound — that sentinel is reserved for pgx.ErrNoRows.
func TestSessionStoreSurfacesDialErrors(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	store := authpostgres.NewSessionStore(unreachablePool(t))
	session, err := domain.NewSession("tenant-a", "sess-1", "emp-1", time.Hour)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	if err := store.CreateSession(ctx, session); err == nil {
		t.Error("CreateSession against a dead database = nil, want a dial error")
	}
	if _, err := store.GetSession(ctx, "sess-1"); err == nil {
		t.Error("GetSession against a dead database = nil, want a dial error")
	} else if errors.Is(err, domain.ErrSessionNotFound) {
		t.Error("GetSession masked a dial error as ErrSessionNotFound")
	}
	if err := store.RevokeSession(ctx, "sess-1"); err == nil {
		t.Error("RevokeSession against a dead database = nil, want a dial error")
	}
	if err := store.ExtendSession(ctx, "sess-1", time.Hour); err == nil {
		t.Error("ExtendSession against a dead database = nil, want a dial error")
	}
}
