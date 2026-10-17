package infrastructure_test

import (
	"context"
	"errors"
	"testing"

	"gogo/internal/modules/inventory/domain"
	"gogo/internal/modules/inventory/infrastructure"
)

func TestStockIsTenantScoped(t *testing.T) {
	ctx := context.Background()
	repo := infrastructure.NewStockRepository()

	if _, _, err := repo.AdjustStock(ctx, "tenant-a", "prod-1", 10, "adjustment", "", "tester", "", "ref-a"); err != nil {
		t.Fatalf("adjust as tenant-a: %v", err)
	}

	if _, err := repo.GetStock(ctx, "tenant-b", "prod-1"); !errors.Is(err, domain.ErrStockNotFound) {
		t.Fatalf("GetStock as tenant-b = %v, want ErrStockNotFound", err)
	}

	stocks, err := repo.ListStocks(ctx, "tenant-b", nil)
	if err != nil {
		t.Fatalf("ListStocks as tenant-b: %v", err)
	}
	if len(stocks) != 0 {
		t.Fatalf("tenant-b sees %d stocks, want 0", len(stocks))
	}

	movements, err := repo.ListMovements(ctx, "tenant-b", "", 0)
	if err != nil {
		t.Fatalf("ListMovements as tenant-b: %v", err)
	}
	if len(movements) != 0 {
		t.Fatalf("tenant-b sees %d movements, want 0", len(movements))
	}

	stock, err := repo.GetStock(ctx, "tenant-a", "prod-1")
	if err != nil {
		t.Fatalf("GetStock as tenant-a: %v", err)
	}
	if stock.Quantity != 10 {
		t.Fatalf("tenant-a stock = %d, want 10", stock.Quantity)
	}
}

func TestAdjustStockIsIdempotentPerTenant(t *testing.T) {
	ctx := context.Background()
	repo := infrastructure.NewStockRepository()

	if _, _, err := repo.AdjustStock(ctx, "tenant-a", "prod-1", 5, "sale", "", "tester", "", "order-1"); err != nil {
		t.Fatalf("first adjust: %v", err)
	}
	if _, _, err := repo.AdjustStock(ctx, "tenant-a", "prod-1", 5, "sale", "", "tester", "", "order-1"); err != nil {
		t.Fatalf("replayed adjust: %v", err)
	}

	stock, err := repo.GetStock(ctx, "tenant-a", "prod-1")
	if err != nil {
		t.Fatalf("GetStock: %v", err)
	}
	if stock.Quantity != 5 {
		t.Fatalf("stock after a replayed reference = %d, want 5", stock.Quantity)
	}

	if _, _, err := repo.AdjustStock(ctx, "tenant-b", "prod-1", 5, "sale", "", "tester", "", "order-1"); err != nil {
		t.Fatalf("same reference for another tenant: %v", err)
	}
	stock, err = repo.GetStock(ctx, "tenant-b", "prod-1")
	if err != nil {
		t.Fatalf("GetStock as tenant-b: %v", err)
	}
	if stock.Quantity != 5 {
		t.Fatalf("tenant-b stock = %d, want 5", stock.Quantity)
	}
}

func TestAdjustStockRejectsOversell(t *testing.T) {
	ctx := context.Background()
	repo := infrastructure.NewStockRepository()

	if _, _, err := repo.AdjustStock(ctx, "tenant-a", "prod-1", 3, "adjustment", "", "tester", "", ""); err != nil {
		t.Fatalf("seed adjust: %v", err)
	}
	if _, _, err := repo.AdjustStock(ctx, "tenant-a", "prod-1", -4, "adjustment", "", "tester", "", ""); !errors.Is(err, domain.ErrInsufficientStock) {
		t.Fatalf("oversell = %v, want ErrInsufficientStock", err)
	}

	stock, err := repo.GetStock(ctx, "tenant-a", "prod-1")
	if err != nil {
		t.Fatalf("GetStock: %v", err)
	}
	if stock.Quantity != 3 {
		t.Fatalf("stock after a rejected change = %d, want 3", stock.Quantity)
	}
}
