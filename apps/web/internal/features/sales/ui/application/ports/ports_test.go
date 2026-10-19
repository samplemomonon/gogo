// Package ports_test pins the console's own port interfaces.
//
// The page owns these interfaces; the adapters under infrastructure/ own the
// implementations. The compile-time assertions below are the conformance pin: a
// port that gains a method, or an adapter that loses one, fails to build here
// rather than at the composition root. The reflection test additionally pins the
// boundary rule the package doc states — no wire contract and no other
// vertical's type may appear in a port signature, because the read-side must
// depend on neither.
package ports_test

import (
	"reflect"
	"strings"
	"testing"

	"gogo/internal/features/sales/ui/application/ports"
	"gogo/internal/features/sales/ui/infrastructure/catalogclient"
	"gogo/internal/features/sales/ui/infrastructure/inventoryclient"
	"gogo/internal/features/sales/ui/infrastructure/salesclient"
)

// ownUI is the console's own subtree: a port may name the read-side's own types
// (results DTOs, its ports), and nothing else under internal/features.
const ownUI = "gogo/internal/features/sales/ui/"

var (
	_ ports.ProductCatalog = (*catalogclient.Client)(nil)
	_ ports.Inventory      = (*inventoryclient.Client)(nil)
	_ ports.Sales          = (*salesclient.Client)(nil)
)

// TestPortSignaturesStayInsideThePage pins the one-way dependency: a port is
// implemented by an adapter over a remote contract, so no parameter or result may
// name a generated contract type or another vertical's type. If one did, the
// read-side's application layer would compile against code it is supposed to
// consume only through its infrastructure adapters.
func TestPortSignaturesStayInsideThePage(t *testing.T) {
	t.Parallel()

	for _, port := range []reflect.Type{
		reflect.TypeOf((*ports.ProductCatalog)(nil)).Elem(),
		reflect.TypeOf((*ports.Inventory)(nil)).Elem(),
		reflect.TypeOf((*ports.Sales)(nil)).Elem(),
	} {
		for i := 0; i < port.NumMethod(); i++ {
			method := port.Method(i)
			for j := 0; j < method.Type.NumIn(); j++ {
				assertPageOwned(t, port.Name(), method.Name, method.Type.In(j))
			}
			for j := 0; j < method.Type.NumOut(); j++ {
				assertPageOwned(t, port.Name(), method.Name, method.Type.Out(j))
			}
		}
	}
}

// assertPageOwned rejects a type whose named package is a wire contract or
// another vertical. Container types are unwrapped so a []*productv1.Product is
// caught as surely as a bare one.
func assertPageOwned(t *testing.T, port, method string, ty reflect.Type) {
	t.Helper()

	for {
		switch ty.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array:
			ty = ty.Elem()
			continue
		}
		break
	}
	if selector := ty.PkgPath(); selector != "" {
		switch {
		case strings.HasPrefix(selector, "gogo/contracts/"):
			t.Errorf("ports.%s.%s carries %s from %s: a port must not name a wire type",
				port, method, ty, selector)
		case strings.HasPrefix(selector, "gogo/internal/features/") && !strings.HasPrefix(selector, ownUI):
			t.Errorf("ports.%s.%s carries %s from %s: a port must not name another vertical's type",
				port, method, ty, selector)
		}
	}
}
