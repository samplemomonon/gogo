// Package results_test pins the profile page's read-model boundary: the account
// the views render carries no auth wire type and no module type, so a contract
// or module refactor cannot leak into the page's templates.
package results_test

import (
	"reflect"
	"strings"
	"testing"

	"gogo/internal/pages/profile/application/results"
)

// forbiddenPackages are the namespaces the read model must stay free of: the
// wire contracts it is projected from, and the module runtime a page must not
// import. The package doc states this as the package's whole reason to exist.
var forbiddenPackages = []string{
	"gogo/contracts/",
	"gogo/internal/modules/",
}

// TestAccountFieldsStayFreeOfWireAndModuleTypes walks the exported fields of
// Account by reflection and fails when any of them can carry a type from a
// forbidden namespace, directly or through a pointer, slice, map or function.
// A string-ified read model may lose data on purpose; it must never regain a
// wire dependency by accident.
func TestAccountFieldsStayFreeOfWireAndModuleTypes(t *testing.T) {
	t.Parallel()

	account := reflect.TypeOf(results.Account{})
	if account.NumField() == 0 {
		t.Fatal("results.Account has no fields; the read model lost its shape")
	}
	for i := 0; i < account.NumField(); i++ {
		field := account.Field(i)
		if !field.IsExported() {
			continue
		}
		assertNoForbiddenType(t, "results.Account."+field.Name, field.Type)
	}
}

// assertNoForbiddenType inspects typ and the types it contains, failing when a
// named type comes from a forbidden namespace.
func assertNoForbiddenType(t *testing.T, path string, typ reflect.Type) {
	t.Helper()

	if pkg := typ.PkgPath(); pkg != "" {
		for _, forbidden := range forbiddenPackages {
			if strings.HasPrefix(pkg, forbidden) {
				t.Errorf("%s carries %s from %s; the read model must project wire and module types away", path, typ, pkg)
			}
		}
	}

	switch typ.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Chan:
		assertNoForbiddenType(t, path+" element", typ.Elem())
	case reflect.Map:
		assertNoForbiddenType(t, path+" key", typ.Key())
		assertNoForbiddenType(t, path+" value", typ.Elem())
	case reflect.Func:
		for i := 0; i < typ.NumIn(); i++ {
			assertNoForbiddenType(t, path+" argument", typ.In(i))
		}
		for i := 0; i < typ.NumOut(); i++ {
			assertNoForbiddenType(t, path+" result", typ.Out(i))
		}
	case reflect.Struct:
		// A named struct's package was checked above; only an anonymous struct
		// has no package path of its own and needs its fields walked.
		if typ.PkgPath() == "" {
			for i := 0; i < typ.NumField(); i++ {
				field := typ.Field(i)
				assertNoForbiddenType(t, path+"."+field.Name, field.Type)
			}
		}
	}
}
