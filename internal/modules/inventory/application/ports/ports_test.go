// Package ports_test pins the inventory application port from the outside. The
// port is the seam the gRPC adapter and both persistence adapters meet on: a
// concrete store that drops or drifts from a method the port promises must fail
// to build, not fail at request time. Declaring the assertions in an external
// test package is what makes this file the check: each adapter already asserts
// the same thing in its own package, so a production edit that breaks the
// contract surfaces here even if that in-package assertion is ever removed.
package ports_test

import (
	"context"
	"testing"

	"gogo/internal/modules/inventory/application/ports"
	"gogo/internal/modules/inventory/infrastructure"
	inventorypostgres "gogo/internal/modules/inventory/infrastructure/postgres"
)

// The two adapters that carry inventory in production and development. Both
// values are nil; the assignment is a compile-time proof of the method set,
// evaluated before any test runs.
var (
	_ ports.StockService = (*infrastructure.StockRepository)(nil)
	_ ports.StockService = (*inventorypostgres.StockRepository)(nil)
)

// TestInMemoryAdapterIsUsableThroughThePort is the runtime half of the
// compile-time assertions above: the in-memory store answers an empty read
// through a ports.StockService variable, so the port is not merely satisfiable
// on paper — a caller holding only the interface can construct an adapter and
// call it. Behaviour beyond this smoke (tenant scoping, oversell, idempotency)
// is pinned in infrastructure/repository_test.go and is deliberately not
// repeated here.
func TestInMemoryAdapterIsUsableThroughThePort(t *testing.T) {
	t.Parallel()

	var service ports.StockService = infrastructure.NewStockRepository()

	stocks, err := service.ListStocks(context.Background(), "acme", nil)
	if err != nil {
		t.Fatalf("ListStocks through the port: %v", err)
	}
	if len(stocks) != 0 {
		t.Fatalf("a fresh store returned %d stocks, want 0", len(stocks))
	}
}
