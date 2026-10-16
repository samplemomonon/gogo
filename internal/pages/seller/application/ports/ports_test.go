// Package ports_test pins the console's own port interfaces.
//
// The page owns these interfaces; the adapters under infrastructure/ own the
// implementations. The compile-time assertions below are the conformance pin: a
// port that gains a method, or an adapter that loses one, fails to build here
// rather than at the composition root. The reflection test additionally pins the
// boundary rule the package doc states — no wire contract and no module type may
// appear in a port signature, because the page must not depend on either.
package ports_test

import (
	"reflect"
	"strings"
	"testing"

	"gogo/internal/pages/seller/application/ports"
	"gogo/internal/pages/seller/infrastructure/catalogclient"
	"gogo/internal/pages/seller/infrastructure/inventoryclient"
	"gogo/internal/pages/seller/infrastructure/salesclient"
)

var (
	_ ports.ProductCatalog = (*catalogclient.Client)(nil)
	_ ports.Inventory      = (*inventoryclient.Client)(nil)
	_ ports.Sales          = (*salesclient.Client)(nil)
)

// TestPortSignaturesStayInsideThePage pins the one-way dependency: a port is
// implemented by an adapter over a remote contract, so no parameter or result may
// name a generated contract type or a module type. If one did, the page's
// application layer would compile against the platform it is supposed to consume
// only through its infrastructure adapters.
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

// assertPageOwned rejects a type whose named package is a wire contract or a
// module. Container types are unwrapped so a []*productv1.Product is caught as
// surely as a bare one.
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
		for _, forbidden := range []string{"gogo/contracts/", "gogo/internal/modules/"} {
			if strings.HasPrefix(selector, forbidden) {
				t.Errorf("ports.%s.%s carries %s from %s: a port must not name a wire or module type",
					port, method, ty, selector)
			}
		}
	}
}
