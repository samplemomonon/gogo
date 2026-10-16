package infrastructure_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"gogo/internal/modules/auth/domain"
	"gogo/internal/modules/auth/infrastructure"
)

// employee builds an employee value for direct store use. The store indexes
// and lists whatever it is handed (validation lives in the domain), so the
// tests can pin CreatedAt to make ordering deterministic.
func employee(tenant domain.Tenant, id, email string, createdAt time.Time) *domain.Employee {
	return &domain.Employee{
		TenantID:  string(tenant),
		ID:        id,
		Name:      "Employee " + id,
		Email:     email,
		Role:      domain.RoleSeller,
		PINHash:   "hash",
		Active:    true,
		CreatedAt: createdAt,
		UpdatedAt: createdAt,
	}
}

// TestEmployeesAreTenantScoped pins the row-level boundary: the same ID and
// email in two tenants are two employees, and a lookup in a third tenant
// never sees either. This is the multi-tenancy contract every store method
// has to honour.
func TestEmployeesAreTenantScoped(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := infrastructure.NewStore()
	now := time.Now()

	if err := store.Create(ctx, "tenant-a", employee("tenant-a", "emp-1", "budi@toko.com", now)); err != nil {
		t.Fatalf("Create in tenant-a: %v", err)
	}
	if err := store.Create(ctx, "tenant-b", employee("tenant-b", "emp-1", "budi@toko.com", now)); err != nil {
		t.Fatalf("Create the same id and email in tenant-b: %v", err)
	}

	gotA, err := store.Get(ctx, "tenant-a", "emp-1")
	if err != nil {
		t.Fatalf("Get as tenant-a: %v", err)
	}
	if gotA.TenantID != "tenant-a" {
		t.Errorf("tenant-a Get returned TenantID %q", gotA.TenantID)
	}

	gotB, err := store.Get(ctx, "tenant-b", "emp-1")
	if err != nil {
		t.Fatalf("Get as tenant-b: %v", err)
	}
	if gotB.TenantID != "tenant-b" {
		t.Errorf("tenant-b Get returned TenantID %q", gotB.TenantID)
	}

	if _, err := store.Get(ctx, "tenant-c", "emp-1"); !errors.Is(err, domain.ErrEmployeeNotFound) {
		t.Errorf("Get as an unregistered tenant = %v, want ErrEmployeeNotFound", err)
	}
	if _, err := store.GetByEmail(ctx, "tenant-c", "budi@toko.com"); !errors.Is(err, domain.ErrEmployeeNotFound) {
		t.Errorf("GetByEmail as an unregistered tenant = %v, want ErrEmployeeNotFound", err)
	}

	listA, err := store.List(ctx, "tenant-a", 0)
	if err != nil {
		t.Fatalf("List as tenant-a: %v", err)
	}
	if len(listA) != 1 || listA[0].TenantID != "tenant-a" {
		t.Errorf("tenant-a List = %d employees, want exactly its own 1", len(listA))
	}

	listC, err := store.List(ctx, "tenant-c", 0)
	if err != nil {
		t.Fatalf("List as tenant-c: %v", err)
	}
	if len(listC) != 0 {
		t.Errorf("tenant-c sees %d employees, want 0", len(listC))
	}
}

// TestCreateRejectsDuplicatesWithinATenant pins the two uniqueness guards:
// an existing ID is rejected with ErrInvalidEmployee (the store's only
// conflict guard for keys), an existing email with the dedicated
// ErrDuplicateEmail. Neither may overwrite the stored employee.
func TestCreateRejectsDuplicatesWithinATenant(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := infrastructure.NewStore()
	now := time.Now()

	if err := store.Create(ctx, "tenant-a", employee("tenant-a", "emp-1", "budi@toko.com", now)); err != nil {
		t.Fatalf("seed Create: %v", err)
	}

	if err := store.Create(ctx, "tenant-a", employee("tenant-a", "emp-1", "other@toko.com", now)); !errors.Is(err, domain.ErrInvalidEmployee) {
		t.Errorf("duplicate ID Create = %v, want ErrInvalidEmployee", err)
	}
	if err := store.Create(ctx, "tenant-a", employee("tenant-a", "emp-2", "budi@toko.com", now)); !errors.Is(err, domain.ErrDuplicateEmail) {
		t.Errorf("duplicate email Create = %v, want ErrDuplicateEmail", err)
	}

	stored, err := store.Get(ctx, "tenant-a", "emp-1")
	if err != nil {
		t.Fatalf("Get after the rejected creates: %v", err)
	}
	if stored.Email != "budi@toko.com" {
		t.Errorf("stored Email = %q, want the original budi@toko.com", stored.Email)
	}
}

// TestGetReportsNotFound pins the two read lookups' miss sentinels: a missing
// id and a missing email are both ErrEmployeeNotFound, the sentinel the gRPC
// adapter maps to NotFound.
func TestGetReportsNotFound(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := infrastructure.NewStore()

	if _, err := store.Get(ctx, "tenant-a", "emp-404"); !errors.Is(err, domain.ErrEmployeeNotFound) {
		t.Errorf("Get(unknown) = %v, want ErrEmployeeNotFound", err)
	}
	if _, err := store.GetByEmail(ctx, "tenant-a", "nobody@toko.com"); !errors.Is(err, domain.ErrEmployeeNotFound) {
		t.Errorf("GetByEmail(unknown) = %v, want ErrEmployeeNotFound", err)
	}
}

// TestUpdateRequiresAnExistingEmployee pins that Update refuses to create:
// an employee the store has never seen is ErrEmployeeNotFound.
func TestUpdateRequiresAnExistingEmployee(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := infrastructure.NewStore()

	err := store.Update(ctx, "tenant-a", employee("tenant-a", "emp-404", "nobody@toko.com", time.Now()))
	if !errors.Is(err, domain.ErrEmployeeNotFound) {
		t.Errorf("Update(unknown) = %v, want ErrEmployeeNotFound", err)
	}
}

// TestUpdateReindexesAChangedEmail pins the email index maintenance: renaming
// an employee's email retires the old lookup, serves the new one, and a
// colliding rename is rejected with ErrDuplicateEmail without corrupting
// either index. The renamed value is a fresh employee, not the pointer Get
// returned: a database-backed read yields a value, and Update keys its
// email-change branch on the stored employee differing from the argument.
func TestUpdateReindexesAChangedEmail(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := infrastructure.NewStore()
	now := time.Now()

	if err := store.Create(ctx, "tenant-a", employee("tenant-a", "emp-1", "a@toko.com", now)); err != nil {
		t.Fatalf("seed emp-1: %v", err)
	}
	if err := store.Create(ctx, "tenant-a", employee("tenant-a", "emp-2", "b@toko.com", now)); err != nil {
		t.Fatalf("seed emp-2: %v", err)
	}

	renamed := employee("tenant-a", "emp-1", "c@toko.com", now)
	if err := store.Update(ctx, "tenant-a", renamed); err != nil {
		t.Fatalf("Update with a changed email: %v", err)
	}
	if _, err := store.GetByEmail(ctx, "tenant-a", "a@toko.com"); !errors.Is(err, domain.ErrEmployeeNotFound) {
		t.Errorf("old email after rename = %v, want ErrEmployeeNotFound", err)
	}
	got, err := store.GetByEmail(ctx, "tenant-a", "c@toko.com")
	if err != nil {
		t.Fatalf("new email after rename: %v", err)
	}
	if got.ID != "emp-1" {
		t.Errorf("new email resolves to %q, want emp-1", got.ID)
	}

	colliding := employee("tenant-a", "emp-2", "c@toko.com", now)
	if err := store.Update(ctx, "tenant-a", colliding); !errors.Is(err, domain.ErrDuplicateEmail) {
		t.Errorf("renaming onto a taken email = %v, want ErrDuplicateEmail", err)
	}
	if got, err := store.GetByEmail(ctx, "tenant-a", "b@toko.com"); err != nil || got.ID != "emp-2" {
		t.Errorf("emp-2's own email after the rejected rename = (%v, %v), want it untouched", got, err)
	}
}

// TestListIsTenantScopedOrderedAndLimited pins the listing contract: newest
// first, only the named tenant, truncated to the limit, and a non-positive
// limit means the default (100) rather than "none" or "all".
func TestListIsTenantScopedOrderedAndLimited(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := infrastructure.NewStore()
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	for _, e := range []*domain.Employee{
		employee("tenant-a", "emp-1", "one@toko.com", base.Add(1*time.Hour)),
		employee("tenant-a", "emp-2", "two@toko.com", base.Add(2*time.Hour)),
		employee("tenant-a", "emp-3", "three@toko.com", base.Add(3*time.Hour)),
		employee("tenant-b", "emp-9", "nine@toko.com", base.Add(4*time.Hour)),
	} {
		if err := store.Create(ctx, domain.Tenant(e.TenantID), e); err != nil {
			t.Fatalf("Create %s: %v", e.ID, err)
		}
	}

	all, err := store.List(ctx, "tenant-a", 0)
	if err != nil {
		t.Fatalf("List(limit 0): %v", err)
	}
	wantOrder := []string{"emp-3", "emp-2", "emp-1"}
	if len(all) != len(wantOrder) {
		t.Fatalf("List(limit 0) returned %d employees, want %d", len(all), len(wantOrder))
	}
	for i, id := range wantOrder {
		if all[i].ID != id {
			t.Errorf("List(limit 0)[%d] = %q, want %q (newest first)", i, all[i].ID, id)
		}
	}

	limited, err := store.List(ctx, "tenant-a", 2)
	if err != nil {
		t.Fatalf("List(limit 2): %v", err)
	}
	if len(limited) != 2 || limited[0].ID != "emp-3" || limited[1].ID != "emp-2" {
		t.Errorf("List(limit 2) = %v, want the two newest: emp-3, emp-2", ids(limited))
	}

	negative, err := store.List(ctx, "tenant-a", -1)
	if err != nil {
		t.Fatalf("List(limit -1): %v", err)
	}
	if len(negative) != len(wantOrder) {
		t.Errorf("List(limit -1) returned %d employees, want the default-limit result (%d)", len(negative), len(wantOrder))
	}

	other, err := store.List(ctx, "tenant-b", 100)
	if err != nil {
		t.Fatalf("List as tenant-b: %v", err)
	}
	if len(other) != 1 || other[0].ID != "emp-9" {
		t.Errorf("tenant-b List = %v, want just emp-9", ids(other))
	}
}

func ids(employees []*domain.Employee) []string {
	out := make([]string, 0, len(employees))
	for _, e := range employees {
		out = append(out, e.ID)
	}
	return out
}

// TestSessionsLifecycle pins create/read/revoke: a created session is
// readable, revoking marks it revoked (visible through the read), and
// revoking twice is not an error — the revocation is a state, not a
// one-shot transition the store needs to guard.
func TestSessionsLifecycle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := infrastructure.NewStore()

	session, err := domain.NewSession("tenant-a", "sess-1", "emp-1", time.Hour)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if err := store.CreateSession(ctx, session); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	got, err := store.GetSession(ctx, "sess-1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.TenantID != "tenant-a" || got.EmployeeID != "emp-1" {
		t.Errorf("GetSession = tenant %q employee %q, want tenant-a/emp-1", got.TenantID, got.EmployeeID)
	}
	if !got.IsValid() {
		t.Error("a fresh session must be valid")
	}

	if err := store.RevokeSession(ctx, "sess-1"); err != nil {
		t.Fatalf("RevokeSession: %v", err)
	}
	if got.RevokedAt == nil {
		t.Error("RevokeSession left RevokedAt unset")
	}
	if got.IsValid() {
		t.Error("a revoked session must not be valid")
	}

	if err := store.RevokeSession(ctx, "sess-1"); err != nil {
		t.Errorf("second RevokeSession = %v, want nil", err)
	}
}

// TestSessionsReportNotFound pins the session miss sentinel on every method
// that addresses a session by id, so a caller can always tell "no such
// session" from a store failure.
func TestSessionsReportNotFound(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := infrastructure.NewStore()

	if _, err := store.GetSession(ctx, "sess-404"); !errors.Is(err, domain.ErrSessionNotFound) {
		t.Errorf("GetSession(unknown) = %v, want ErrSessionNotFound", err)
	}
	if err := store.RevokeSession(ctx, "sess-404"); !errors.Is(err, domain.ErrSessionNotFound) {
		t.Errorf("RevokeSession(unknown) = %v, want ErrSessionNotFound", err)
	}
	if err := store.ExtendSession(ctx, "sess-404", time.Hour); !errors.Is(err, domain.ErrSessionNotFound) {
		t.Errorf("ExtendSession(unknown) = %v, want ErrSessionNotFound", err)
	}
}

// TestExtendSessionMovesTheExpiry pins the successful extension: the stored
// session's ExpiresAt becomes now + ttl (the in-memory store mutates the
// stored session in place), not the old expiry plus ttl.
func TestExtendSessionMovesTheExpiry(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := infrastructure.NewStore()

	session, err := domain.NewSession("tenant-a", "sess-1", "emp-1", time.Hour)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if err := store.CreateSession(ctx, session); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	before := time.Now()
	if err := store.ExtendSession(ctx, "sess-1", 2*time.Hour); err != nil {
		t.Fatalf("ExtendSession: %v", err)
	}
	if !session.ExpiresAt.After(before.Add(time.Hour)) {
		t.Errorf("ExpiresAt after a 2h extension = %v, want at least an hour past the call", session.ExpiresAt)
	}
	if !session.ExpiresAt.Before(before.Add(3 * time.Hour)) {
		t.Errorf("ExpiresAt after a 2h extension = %v, want under three hours past the call", session.ExpiresAt)
	}
}

// TestExtendSessionRejectsExpiredAndRevoked pins the extension guard: an
// expired or already-revoked session answers ErrExpired — the store refuses
// to resurrect a dead session by extending it.
func TestExtendSessionRejectsExpiredAndRevoked(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := infrastructure.NewStore()
	past := time.Now().Add(-time.Hour)

	expired := &domain.Session{
		ID:         "sess-expired",
		TenantID:   "tenant-a",
		EmployeeID: "emp-1",
		CreatedAt:  past.Add(-time.Hour),
		ExpiresAt:  past,
	}
	if err := store.CreateSession(ctx, expired); err != nil {
		t.Fatalf("CreateSession(expired): %v", err)
	}
	if err := store.ExtendSession(ctx, "sess-expired", time.Hour); !errors.Is(err, domain.ErrExpired) {
		t.Errorf("ExtendSession(expired) = %v, want ErrExpired", err)
	}

	revokedAt := past
	revoked := &domain.Session{
		ID:         "sess-revoked",
		TenantID:   "tenant-a",
		EmployeeID: "emp-1",
		CreatedAt:  past.Add(-time.Hour),
		ExpiresAt:  time.Now().Add(time.Hour),
		RevokedAt:  &revokedAt,
	}
	if err := store.CreateSession(ctx, revoked); err != nil {
		t.Fatalf("CreateSession(revoked): %v", err)
	}
	if err := store.ExtendSession(ctx, "sess-revoked", time.Hour); !errors.Is(err, domain.ErrExpired) {
		t.Errorf("ExtendSession(revoked) = %v, want ErrExpired", err)
	}
}
