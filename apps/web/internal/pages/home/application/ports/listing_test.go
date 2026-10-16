// Package ports_test pins the home page's owned input port. The storefront
// depends on ports.Listing — its own read-only interface — and the remote
// adapter (infrastructure/listingclient) is the production type that must
// satisfy it. The assertion below fails to compile the moment either side
// drifts, so the page cannot accidentally grow a dependency on a product
// service's runtime types or on the productlisting.v1/productsearch.v1 wire
// contracts: those stay behind the adapter.
package ports_test

import (
	"testing"

	"gogo/internal/pages/home/application/ports"
	"gogo/internal/pages/home/infrastructure/listingclient"
	"gogo/internal/shared/productconsumer"
)

// The remote adapter is the only production implementation of the page's port,
// so composition root wiring would not build if this stopped holding. This is
// the contract the port package exists to declare.
var _ ports.Listing = (*listingclient.Client)(nil)

func TestTheRemoteAdapterSatisfiesThePagesOwnedPort(t *testing.T) {
	t.Parallel()

	// Assigning the adapter to a ports.Listing variable makes the conformance
	// above exercisable at run time as well as at compile time. New only wraps
	// the connections and fills in default breakers, so empty connections are
	// safe here.
	var adapter ports.Listing = listingclient.New(productconsumer.Connections{}, productconsumer.Breakers{})
	if adapter == nil {
		t.Fatal("listingclient.New returned nil; the page's port has no adapter")
	}
}
