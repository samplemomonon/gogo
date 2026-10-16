// Package results_test pins the home page's read model as page-local: every
// exported field must be a primitive, a standard-library value, or a type
// declared in this package itself. A type from gogo/contracts/** (the wire) or
// gogo/internal/modules/** (a module's runtime) would make the storefront a
// second consumer of the product platform's contract — exactly what this
// package's doc comment says it exists to prevent. The golden field lists make
// the shape of the read model an explicit decision rather than a drift.
package results_test

import (
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"gogo/internal/pages/home/application/results"
)

// localPkg is the one package a read-model field may name besides the standard
// library: the read model itself. Listing.Products is the only such field.
const localPkg = "gogo/internal/pages/home/application/results"

func TestReadModelIsPageLocal(t *testing.T) {
	t.Parallel()

	assertLocal(t, reflect.TypeOf(results.Product{}), "Product")
	assertLocal(t, reflect.TypeOf(results.Listing{}), "Listing")
}

// TestReadModelShapeIsDeliberate pins the exported fields of the read model.
// Adding a field is a decision — does the grid really render this? — so it must
// be made here too, where the field also has to pass the purity check.
func TestReadModelShapeIsDeliberate(t *testing.T) {
	t.Parallel()

	assertFields(t, reflect.TypeOf(results.Product{}), "Product",
		[]string{"Attributes", "Barcode", "Brand", "Currency", "Description", "ID", "Name", "Price", "ProductTypeID", "SchemaVersion", "Slug", "Stock", "TenantID"})
	assertFields(t, reflect.TypeOf(results.Listing{}), "Listing",
		[]string{"NextCursor", "Products", "Total"})
}

// assertLocal walks a page-local struct's exported fields and rejects anything
// that is not local, primitive, or a standard-library time value.
func assertLocal(t *testing.T, typ reflect.Type, path string) {
	t.Helper()

	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}
		checkType(t, path+"."+field.Name, field.Type)
	}
}

// checkType classifies one field type, recursing through composites so a slice
// of module types is caught as surely as a bare module struct.
func checkType(t *testing.T, path string, typ reflect.Type) {
	t.Helper()

	if typ == reflect.TypeOf(time.Time{}) {
		return
	}

	switch typ.Kind() {
	case reflect.String, reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return
	case reflect.Pointer, reflect.Slice, reflect.Array:
		checkType(t, path+"[]", typ.Elem())
		return
	case reflect.Map:
		checkType(t, path+" (key)", typ.Key())
		checkType(t, path+" (value)", typ.Elem())
		return
	case reflect.Struct:
		pkg := typ.PkgPath()
		if pkg == localPkg {
			assertLocal(t, typ, path)
			return
		}
		switch {
		case strings.HasPrefix(pkg, "gogo/contracts/"):
			t.Errorf("%s is %s: a wire-contract type leaked into the page's read model", path, typ)
		case strings.HasPrefix(pkg, "gogo/internal/modules/"):
			t.Errorf("%s is %s: a module runtime type leaked into the page's read model", path, typ)
		default:
			t.Errorf("%s is %s from %q: the read model must stay page-local or primitive", path, typ, pkg)
		}
	default:
		t.Errorf("%s is %s (%v): unexpected type in the page's read model", path, typ, typ.Kind())
	}
}

// assertFields compares the exported field names of typ against want, sorted,
// so an accidental field is reported by name rather than as a shape mismatch.
func assertFields(t *testing.T, typ reflect.Type, name string, want []string) {
	t.Helper()

	got := make([]string, 0, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		if field := typ.Field(i); field.IsExported() {
			got = append(got, field.Name)
		}
	}
	sort.Strings(got)
	sort.Strings(want)

	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s fields = %v, want %v", name, got, want)
	}
}
