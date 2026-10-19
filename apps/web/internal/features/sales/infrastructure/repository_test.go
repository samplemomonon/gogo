// Package infrastructure_test covers the in-memory sales store: the tenant
// scoping every read applies, duplicate-ID rejection on create, the newest-first
// default-limited list, and the void-save rules. It mirrors the inventory
// store's test style.
package infrastructure_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"gogo/internal/features/sales/domain"
	"gogo/internal/features/sales/infrastructure"
)

// newTransaction builds a stored-shaped transaction with a controlled
// CreatedAt, so list-order assertions are deterministic rather than racing the
// wall clock.
func newTransaction(tenant domain.Tenant, id string, createdAt time.Time) *domain.Transaction {
	return &domain.Transaction{
		TenantID:      tenant,
		ID:            id,
		Number:        "TRX-" + id,
		Total:         1000,
		Paid:          1000,
		PaymentMethod: "Tunai",
		CreatedAt:     createdAt,
		Items: []domain.Item{
			{ID: id + ":1", ProductID: "p1", Name: "Kopi", Quantity: 1, Price: 1000, Subtotal: 1000},
		},
	}
}

// TestCreateAndGetRoundTrip pins that the store preserves every persisted field
// and returns a copy, not the caller's pointer.
func TestCreateAndGetRoundTrip(t *testing.T) {
	t.Parallel()

	store := infrastructure.NewStore()
	created := newTransaction("acme", "tx-1", time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))

	if err := store.Create(t.Context(), "acme", created); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := store.Get(t.Context(), "acme", "tx-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == created {
		t.Error("Get returned the caller's pointer, want a stored snapshot")
	}
	if got.Number != "TRX-tx-1" || got.Total != 1000 || got.TenantID != "acme" {
		t.Errorf("Get = %+v, want the created transaction", got)
	}
	if !got.CreatedAt.Equal(created.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, created.CreatedAt)
	}
	if len(got.Items) != 1 || got.Items[0].ProductID != "p1" || got.Items[0].Subtotal != 1000 {
		t.Errorf("Items = %+v, want the created line", got.Items)
	}
}

// TestStoreIsTenantScoped pins the tenancy boundary: another tenant must not
// read or list a transaction, and the owner still can.
func TestStoreIsTenantScoped(t *testing.T) {
	t.Parallel()

	store := infrastructure.NewStore()
	if err := store.Create(t.Context(), "acme", newTransaction("acme", "tx-1", time.Now())); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := store.Get(t.Context(), "other", "tx-1"); !errors.Is(err, domain.ErrTransactionNotFound) {
		t.Fatalf("Get as other = %v, want ErrTransactionNotFound", err)
	}

	found, err := store.List(t.Context(), "other", 0)
	if err != nil {
		t.Fatalf("List as other: %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("other sees %d transactions, want 0", len(found))
	}

	got, err := store.Get(t.Context(), "acme", "tx-1")
	if err != nil {
		t.Fatalf("Get as acme: %v", err)
	}
	if got.TenantID != "acme" {
		t.Fatalf("acme got tenant %q, want acme", got.TenantID)
	}
}

// TestCreateRejectsADuplicateID pins that transaction IDs are globally unique
// in the store (matching the Postgres primary key on id): a second create of
// the same ID fails, even from another tenant.
func TestCreateRejectsADuplicateID(t *testing.T) {
	t.Parallel()

	store := infrastructure.NewStore()
	if err := store.Create(t.Context(), "acme", newTransaction("acme", "tx-1", time.Now())); err != nil {
		t.Fatalf("first Create: %v", err)
	}

	if err := store.Create(t.Context(), "acme", newTransaction("acme", "tx-1", time.Now())); !errors.Is(err, domain.ErrInvalidTransaction) {
		t.Errorf("duplicate Create = %v, want ErrInvalidTransaction", err)
	}
	if err := store.Create(t.Context(), "other", newTransaction("other", "tx-1", time.Now())); !errors.Is(err, domain.ErrInvalidTransaction) {
		t.Errorf("duplicate ID from another tenant = %v, want ErrInvalidTransaction", err)
	}
}

// TestGetUnknownIDReturnsNotFound pins the not-found sentinel for a missing id.
func TestGetUnknownIDReturnsNotFound(t *testing.T) {
	t.Parallel()

	if _, err := infrastructure.NewStore().Get(t.Context(), "acme", "missing"); !errors.Is(err, domain.ErrTransactionNotFound) {
		t.Errorf("Get = %v, want ErrTransactionNotFound", err)
	}
}

// TestListReturnsTheTenantsNewestFirstAndLimited pins three list rules at once:
// only the tenant's transactions are returned, the order is newest first, and a
// positive limit truncates to the newest entries.
func TestListReturnsTheTenantsNewestFirstAndLimited(t *testing.T) {
	t.Parallel()

	store := infrastructure.NewStore()
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	for i, id := range []string{"tx-a", "tx-b", "tx-c"} {
		if err := store.Create(t.Context(), "acme", newTransaction("acme", id, base.Add(time.Duration(i)*time.Minute))); err != nil {
			t.Fatalf("Create %s: %v", id, err)
		}
	}
	if err := store.Create(t.Context(), "other", newTransaction("other", "tx-z", base.Add(time.Hour))); err != nil {
		t.Fatalf("Create cross-tenant: %v", err)
	}

	found, err := store.List(t.Context(), "acme", 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(found) != 3 {
		t.Fatalf("List returned %d transactions, want 3 (tenant scoped)", len(found))
	}
	for i, want := range []string{"tx-c", "tx-b", "tx-a"} {
		if found[i].ID != want {
			t.Errorf("List[%d].ID = %q, want %q (newest first)", i, found[i].ID, want)
		}
	}

	limited, err := store.List(t.Context(), "acme", 2)
	if err != nil {
		t.Fatalf("List with limit: %v", err)
	}
	if len(limited) != 2 {
		t.Fatalf("List(limit 2) returned %d transactions, want 2", len(limited))
	}
	if limited[0].ID != "tx-c" || limited[1].ID != "tx-b" {
		t.Errorf("List(limit 2) = [%s %s], want the two newest [tx-c tx-b]", limited[0].ID, limited[1].ID)
	}
}

// TestListDefaultsToOneHundred pins the non-positive-limit default: a limit at
// or below zero means 100, and the truncation keeps the newest entries.
func TestListDefaultsToOneHundred(t *testing.T) {
	t.Parallel()

	store := infrastructure.NewStore()
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	for i := 0; i <= 100; i++ {
		id := fmt.Sprintf("tx-%03d", i)
		if err := store.Create(t.Context(), "acme", newTransaction("acme", id, base.Add(time.Duration(i)*time.Second))); err != nil {
			t.Fatalf("Create %s: %v", id, err)
		}
	}

	for _, limit := range []int{0, -1} {
		found, err := store.List(t.Context(), "acme", limit)
		if err != nil {
			t.Fatalf("List(limit %d): %v", limit, err)
		}
		if len(found) != 100 {
			t.Fatalf("List(limit %d) returned %d transactions, want the 100 default", limit, len(found))
		}
		if found[0].ID != "tx-100" || found[99].ID != "tx-001" {
			t.Errorf("List(limit %d) spans [%s..%s], want the newest 100 [tx-100..tx-001]",
				limit, found[0].ID, found[99].ID)
		}
	}
}

// TestSaveVoidPersistsInPlaceAndIsTenantScoped pins the void-save contract: the
// owner's void is persisted, while another tenant's attempt is refused with the
// not-found sentinel and leaves the record untouched.
func TestSaveVoidPersistsInPlaceAndIsTenantScoped(t *testing.T) {
	t.Parallel()

	store := infrastructure.NewStore()
	if err := store.Create(t.Context(), "acme", newTransaction("acme", "tx-1", time.Now())); err != nil {
		t.Fatalf("Create: %v", err)
	}

	voided, err := store.Get(t.Context(), "acme", "tx-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if err := voided.Void("Supervisor", "Barang rusak"); err != nil {
		t.Fatalf("Void: %v", err)
	}
	if err := store.SaveVoid(t.Context(), "acme", voided); err != nil {
		t.Fatalf("SaveVoid: %v", err)
	}

	saved, err := store.Get(t.Context(), "acme", "tx-1")
	if err != nil {
		t.Fatalf("Get after SaveVoid: %v", err)
	}
	if saved.VoidedAt == nil {
		t.Fatal("the void was not persisted")
	}
	if saved.VoidedBy != "Supervisor" || saved.VoidReason != "Barang rusak" {
		t.Errorf("void metadata = (%q, %q), want (Supervisor, Barang rusak)", saved.VoidedBy, saved.VoidReason)
	}

	if err := store.SaveVoid(t.Context(), "other", voided); !errors.Is(err, domain.ErrTransactionNotFound) {
		t.Errorf("SaveVoid as other = %v, want ErrTransactionNotFound", err)
	}
	untouched, err := store.Get(t.Context(), "acme", "tx-1")
	if err != nil {
		t.Fatalf("Get after refused SaveVoid: %v", err)
	}
	if !untouched.VoidedAt.Equal(*saved.VoidedAt) {
		t.Error("a refused cross-tenant SaveVoid changed the stored transaction")
	}
}

// TestSaveVoidRejectsAnUnknownTransaction pins the not-found sentinel for an id
// that was never created.
func TestSaveVoidRejectsAnUnknownTransaction(t *testing.T) {
	t.Parallel()

	err := infrastructure.NewStore().SaveVoid(t.Context(), "acme", newTransaction("acme", "missing", time.Now()))
	if !errors.Is(err, domain.ErrTransactionNotFound) {
		t.Errorf("SaveVoid = %v, want ErrTransactionNotFound", err)
	}
}
