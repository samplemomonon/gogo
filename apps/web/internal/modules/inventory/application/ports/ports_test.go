// Package ports_test pins the inventory boundaries from the outside: application
// service to delivery port, and persistence adapters to repository port.
package ports_test

import (
	"context"
	"testing"

	"gogo/internal/modules/inventory/application"
	"gogo/internal/modules/inventory/application/ports"
	"gogo/internal/modules/inventory/infrastructure"
	inventorypostgres "gogo/internal/modules/inventory/infrastructure/postgres"
)

// These assignments are compile-time proofs of the two distinct boundaries.
var (
	_ ports.StockStore   = (*infrastructure.StockRepository)(nil)
	_ ports.StockStore   = (*inventorypostgres.StockRepository)(nil)
	_ ports.StockService = (*application.Service)(nil)
)

// TestInMemoryAdapterIsUsableThroughTheRepositoryPort checks that the in-memory
// adapter can be consumed through the persistence boundary.
func TestInMemoryAdapterIsUsableThroughTheRepositoryPort(t *testing.T) {
	t.Parallel()

	var repository ports.StockStore = infrastructure.NewStockRepository()

	stocks, err := repository.ListStocks(context.Background(), "acme", nil)
	if err != nil {
		t.Fatalf("ListStocks through the port: %v", err)
	}
	if len(stocks) != 0 {
		t.Fatalf("a fresh store returned %d stocks, want 0", len(stocks))
	}
}
