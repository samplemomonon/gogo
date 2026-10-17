package infrastructure

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"gogo/internal/modules/inventory/application/ports"
	"gogo/internal/modules/inventory/domain"
)

// StockRepository is the in-memory inventory store used when no database is
// configured. Like the Postgres adapter it scopes every read and write by
// tenant: two tenants may not see or adjust each other's stock, and idempotency
// is per tenant.
type StockRepository struct {
	mu        sync.RWMutex
	stocks    map[stockKey]domain.Stock
	movements []movementEntry
}

type stockKey struct {
	tenant    domain.Tenant
	productID string
}

// movementEntry carries the tenant of a ledger row, which domain.Movement
// deliberately does not hold (the Postgres adapter filters by tenant in SQL).
type movementEntry struct {
	tenant domain.Tenant
	value  domain.Movement
}

var _ ports.StockStore = (*StockRepository)(nil)

func NewStockRepository() *StockRepository {
	return &StockRepository{stocks: make(map[stockKey]domain.Stock)}
}

func (r *StockRepository) GetStock(ctx context.Context, tenant domain.Tenant, productID string) (*domain.Stock, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	stock, ok := r.stocks[stockKey{tenant: tenant, productID: productID}]
	if !ok {
		return nil, domain.ErrStockNotFound
	}
	return &stock, nil
}

func (r *StockRepository) ListStocks(ctx context.Context, tenant domain.Tenant, productIDs []string) ([]*domain.Stock, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	want := make(map[string]bool, len(productIDs))
	for _, id := range productIDs {
		want[id] = true
	}

	out := make([]*domain.Stock, 0, len(r.stocks))
	for key, stock := range r.stocks {
		if key.tenant != tenant {
			continue
		}
		if len(want) > 0 && !want[key.productID] {
			continue
		}
		snapshot := stock
		out = append(out, &snapshot)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ProductID < out[j].ProductID })
	return out, nil
}

func (r *StockRepository) AdjustStock(ctx context.Context, tenant domain.Tenant, productID string, delta int, kind, note, actor, serviceActor, reference string) (*domain.Stock, *domain.Movement, error) {
	if err := domain.NewAdjustment(tenant, productID, kind, actor, reference); err != nil {
		return nil, nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if reference != "" {
		for i := range r.movements {
			entry := &r.movements[i]
			if entry.tenant != tenant || entry.value.Reference != reference {
				continue
			}
			stock := domain.Stock{ProductID: entry.value.ProductID, Quantity: entry.value.After, UpdatedAt: entry.value.CreatedAt}
			movement := entry.value
			return &stock, &movement, nil
		}
	}

	key := stockKey{tenant: tenant, productID: productID}
	before := r.stocks[key].Quantity
	after, err := domain.AdjustedQuantity(before, delta)
	if err != nil {
		return nil, nil, err
	}

	now := time.Now()
	stock := domain.Stock{ProductID: productID, Quantity: after, UpdatedAt: now}
	r.stocks[key] = stock

	movement := domain.Movement{
		ID:           uuid.NewString(),
		ProductID:    productID,
		Kind:         kind,
		Delta:        delta,
		Before:       before,
		After:        after,
		Note:         note,
		Actor:        actor,
		ServiceActor: serviceActor,
		Reference:    reference,
		CreatedAt:    now,
	}
	r.movements = append(r.movements, movementEntry{tenant: tenant, value: movement})

	return &stock, &movement, nil
}

func (r *StockRepository) ListMovements(ctx context.Context, tenant domain.Tenant, productID string, limit int) ([]*domain.Movement, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if limit <= 0 {
		limit = 100
	}

	out := make([]*domain.Movement, 0, len(r.movements))
	for i := len(r.movements) - 1; i >= 0 && len(out) < limit; i-- {
		entry := &r.movements[i]
		if entry.tenant != tenant {
			continue
		}
		if productID != "" && entry.value.ProductID != productID {
			continue
		}
		snapshot := entry.value
		out = append(out, &snapshot)
	}
	return out, nil
}
