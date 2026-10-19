// Package results_test pins the seller console's read-model purity.
//
// The package doc states the ownership rule: these types belong to the console,
// no backend and no service ever sees them, and they are mapped by the adapters
// in infrastructure/. A reflection walk over every exported field enforces the
// half of that rule a compiler cannot: no field may carry a gogo wire contract
// or another vertical's type, because such a field would couple the console's
// views to a shape it does not own.
package results_test

import (
	"reflect"
	"strings"
	"testing"

	"gogo/internal/features/sales/ui/application/results"
)

// ownUI is the console's own subtree: read models may name each other, and
// nothing else under internal/features.
const ownUI = "gogo/internal/features/sales/ui/"

// models is every exported shape in the package. The list is explicit so adding
// a read model without extending this test is itself visible.
var models = []any{
	results.Product{},
	results.ProductInput{},
	results.Category{},
	results.Stock{},
	results.Movement{},
	results.Transaction{},
	results.TransactionItem{},
}

// TestReadModelsCarryNoContractOrModuleTypes walks each read model's exported
// fields, recursively, and fails on any named type whose package is a wire
// contract or a module. A slice of generated messages and a bare one are both
// caught, since containers are unwrapped first.
func TestReadModelsCarryNoContractOrModuleTypes(t *testing.T) {
	t.Parallel()

	for _, model := range models {
		walkFields(t, reflect.TypeOf(model).Name(), reflect.TypeOf(model), map[reflect.Type]bool{})
	}
}

func walkFields(t *testing.T, owner string, ty reflect.Type, seen map[reflect.Type]bool) {
	t.Helper()

	if seen[ty] {
		return
	}
	seen[ty] = true

	for i := 0; i < ty.NumField(); i++ {
		field := ty.Field(i)
		if !field.IsExported() {
			continue
		}
		fieldType := field.Type
		for {
			switch fieldType.Kind() {
			case reflect.Pointer, reflect.Slice, reflect.Array:
				fieldType = fieldType.Elem()
				continue
			}
			break
		}
		if pkg := fieldType.PkgPath(); pkg != "" && !strings.HasPrefix(pkg, ownUI) {
			switch {
			case strings.HasPrefix(pkg, "gogo/contracts/"):
				t.Errorf("results.%s.%s carries %s from %s: a read model must not carry a wire type",
					owner, field.Name, fieldType, pkg)
			case strings.HasPrefix(pkg, "gogo/internal/features/"):
				t.Errorf("results.%s.%s carries %s from %s: a read model must not carry another vertical's type",
					owner, field.Name, fieldType, pkg)
			}
		}
		if fieldType.Kind() == reflect.Struct {
			walkFields(t, owner+"."+field.Name, fieldType, seen)
		}
	}
}

// TestProductReadModelHasNoStockField pins the package doc's central invariant:
// the sellable quantity belongs to the inventory module, and stock is absent from
// the read model so no view can treat the platform's mirrored column as
// authoritative.
func TestProductReadModelHasNoStockField(t *testing.T) {
	t.Parallel()

	if _, ok := reflect.TypeOf(results.Product{}).FieldByName("Stock"); ok {
		t.Error("results.Product carries a Stock field; stock must be read from the inventory read model")
	}
	if _, ok := reflect.TypeOf(results.ProductInput{}).FieldByName("Stock"); !ok {
		t.Error("results.ProductInput lost its Stock field; the starting quantity is set at creation")
	}
}

// TestProductReadModelFieldList pins the fields the doc says survive, and the
// ones it says are deliberately gone. These are contract, not accident: each
// removed field was removed because no platform contract carries it, and a
// survivor means a view can render what the write side accepts.
func TestProductReadModelFieldList(t *testing.T) {
	t.Parallel()

	product := reflect.TypeOf(results.Product{})
	want := []string{"ID", "ProductTypeID", "SchemaVersion", "Attributes", "Name", "Slug", "Description", "Brand", "Price", "Currency", "Status", "CategoryID", "MerchantID"}
	assertExportedFields(t, product, want)

	// The doc names these as gone from the platform's contracts. A field that
	// reappeared would be rendered as a constant empty and a form that discards
	// what the cashier typed.
	for _, removed := range []string{"SKU", "Barcode", "Unit", "ImageURL", "BuyPrice", "CostExtra", "MinStock", "MaxStock"} {
		if _, ok := product.FieldByName(removed); ok {
			t.Errorf("results.Product gained %s, which no product contract carries", removed)
		}
	}

	// Money travels as minor units, and the write model is a distinct full-form
	// shape: it carries Stock (once, at creation) and defaults nothing else.
	input := reflect.TypeOf(results.ProductInput{})
	assertExportedFields(t, input, []string{"ProductTypeID", "SchemaVersion", "Attributes", "Name", "Slug", "Description", "Brand", "Price", "Currency", "Stock", "Status", "CategoryID", "MerchantID"})
	inputPrice, _ := input.FieldByName("Price")
	if got := inputPrice.Type.Kind(); got != reflect.Int64 {
		t.Errorf("ProductInput.Price is %s, want int64 minor units", got)
	}
	productPrice, _ := product.FieldByName("Price")
	if got := productPrice.Type.Kind(); got != reflect.Int64 {
		t.Errorf("Product.Price is %s, want int64 minor units", got)
	}
}

func assertExportedFields(t *testing.T, ty reflect.Type, want []string) {
	t.Helper()

	var got []string
	for i := 0; i < ty.NumField(); i++ {
		if field := ty.Field(i); field.IsExported() {
			got = append(got, field.Name)
		}
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("%s exported fields = %v, want %v", ty.Name(), got, want)
	}
}
