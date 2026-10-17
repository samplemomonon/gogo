package handlers_test

import (
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"gogo/internal/pages/seller/application/results"
	"gogo/internal/shared/productconsumer"
)

// TestProductCreateForwardsTheTrimmedFormAndRedirects pins the create contract end
// to end at the handler boundary: every writable field is read, surrounding
// whitespace is normalized away, blank numbers read as zero, an omitted status
// defaults to active, and a committed write answers with a 303 to the list rather
// than rendering the row (the write side's read model may lag it).
func TestProductCreateForwardsTheTrimmedFormAndRedirects(t *testing.T) {
	t.Parallel()

	router, catalog, _, _ := newConsole(t)

	rec := do(t, router, postReq("/seller/produk",
		"productTypeId=general&schemaVersion=1&name=++Kopi+Susu++&slug=+kopi-susu+&description=+Enak+&brand=+Gogo+&price=+25000+&stock=+3+&categoryId=+10+&status="))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create status = %d, want 303", rec.Code)
	}
	if got := rec.Header().Get("Location"); !strings.HasPrefix(got, "/seller/produk?created=") {
		t.Errorf("Location = %q, want a redirect carrying the created product", got)
	}

	want := results.ProductInput{
		Name: "Kopi Susu", Slug: "kopi-susu", Description: "Enak", Brand: "Gogo",
		Price: 25000, Currency: "IDR", Stock: 3, Status: "active", CategoryID: "10",
		ProductTypeID: "general", SchemaVersion: 1, Attributes: map[string]string{},
		MerchantID: "merchant-1",
	}
	if !reflect.DeepEqual(catalog.lastCreate, want) {
		t.Errorf("create input = %+v, want %+v", catalog.lastCreate, want)
	}
}

// TestProductCreateReRendersAt200OnAWriteFailure pins the failure posture for
// writes: a rejected create keeps the cashier on the form with the message inline
// and the status at 200. It is deliberately not the read path's error page — the
// entered values must stay on screen so nothing they typed is lost.
func TestProductCreateReRendersAt200OnAWriteFailure(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		err     error
		message string
	}{
		{"a duplicate identity", productconsumer.ErrConflict, "Produk dengan identitas tersebut sudah ada"},
		{"an invalid submission", productconsumer.ErrInvalid, "Data produk tidak valid"},
		{"an unreachable platform", productconsumer.ErrProductPlatformUnavailable, "Layanan sedang tidak tersedia, coba lagi sebentar lagi"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			router, catalog, _, _ := newConsole(t)
			catalog.createErr = fmt.Errorf("%w: upstream says something secret", tc.err)

			rec := do(t, router, postReq("/seller/produk", "productTypeId=general&schemaVersion=1&name=Kopi&price=25000&status=active"))

			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200 (the form re-renders)", rec.Code)
			}
			body := rec.Body.String()
			if !strings.Contains(body, tc.message) {
				t.Errorf("body does not show %q", tc.message)
			}
			if strings.Contains(body, "something secret") {
				t.Error("the re-rendered form leaked the upstream's error text")
			}
		})
	}
}

// TestProductUpdateReadsTheIDFromThePath pins the update contract: the id comes
// from the URL, never from the form, so a tampered form cannot retarget an edit.
func TestProductUpdateReadsTheIDFromThePath(t *testing.T) {
	t.Parallel()

	router, catalog, _, _ := newConsole(t)

	rec := do(t, router, postReq("/seller/produk/1",
		"id=99&name=Kopi+Susu&slug=kopi-susu&price=26000&stock=9&status=active&categoryId=10"))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("update status = %d, want 303", rec.Code)
	}
	if catalog.lastUpdateID != "1" {
		t.Errorf("update id = %q, want the path's 1, not the form's id", catalog.lastUpdateID)
	}
	if got := rec.Header().Get("Location"); got != "/seller/produk" {
		t.Errorf("Location = %q, want /seller/produk", got)
	}
}

func TestProductUpdateFailureReRendersWithTheMappedMessage(t *testing.T) {
	t.Parallel()

	router, catalog, _, _ := newConsole(t)
	catalog.updateErr = productconsumer.ErrInvalid

	rec := do(t, router, postReq("/seller/produk/1", "name=Kopi&price=26000&status=active"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (the form re-renders)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Data produk tidak valid") {
		t.Error("the re-rendered form does not show the mapped rejection")
	}
}

func TestProductDeleteForwardsTheIDAndRedirects(t *testing.T) {
	t.Parallel()

	router, catalog, _, _ := newConsole(t)

	rec := do(t, router, postReq("/seller/produk/1/delete", ""))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("delete status = %d, want 303", rec.Code)
	}
	if catalog.lastDeleteID != "1" {
		t.Errorf("delete id = %q, want 1 from the path", catalog.lastDeleteID)
	}
	if got := rec.Header().Get("Location"); got != "/seller/produk" {
		t.Errorf("Location = %q, want /seller/produk", got)
	}
}

// TestProductMutationForbiddenForNonOwner pins the merchant write gate: an
// employee who is not a member of the product's merchant (or who holds no
// merchant-scoped grant) is refused 403 before the write reaches the platform.
func TestProductMutationForbiddenForNonOwner(t *testing.T) {
	t.Parallel()

	router, catalog, _, _ := newConsoleWithSessions(t, fakeEmployeeSessions{denyManage: true})

	rec := do(t, router, postReq("/seller/produk", "productTypeId=general&schemaVersion=1&name=Kopi&price=1000&status=active"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("create status = %d, want 403", rec.Code)
	}
	if catalog.lastCreate.Name != "" {
		t.Errorf("create reached the catalog despite a deny: %q", catalog.lastCreate.Name)
	}

	rec = do(t, router, postReq("/seller/produk/1", "name=Kopi&price=1000&status=active"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("update status = %d, want 403", rec.Code)
	}
	if catalog.lastUpdateID != "" {
		t.Errorf("update reached the catalog despite a deny: id = %q", catalog.lastUpdateID)
	}

	rec = do(t, router, postReq("/seller/produk/1/delete", ""))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("delete status = %d, want 403", rec.Code)
	}
	if catalog.lastDeleteID != "" {
		t.Errorf("delete reached the catalog despite a deny: id = %q", catalog.lastDeleteID)
	}
}
func TestProductDeleteOfAnAlreadyGoneRowShowsNotFound(t *testing.T) {
	t.Parallel()

	router, catalog, _, _ := newConsole(t)
	catalog.deleteErr = productconsumer.ErrNotFound

	rec := do(t, router, postReq("/seller/produk/1/delete", ""))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Produk tidak ditemukan") {
		t.Error("a delete of an already-gone product did not say so")
	}
}
