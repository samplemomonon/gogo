package infrastructure

import (
	"context"
	"sort"
	"sync"

	"gogo/internal/features/sales/application/ports"
	"gogo/internal/features/sales/domain"
)

// Store is the in-memory transaction store used when no database is configured.
type Store struct {
	mu           sync.RWMutex
	transactions map[string]domain.Transaction
}

var _ ports.Store = (*Store)(nil)

func NewStore() *Store {
	return &Store{transactions: make(map[string]domain.Transaction)}
}

func (r *Store) Create(ctx context.Context, tenant domain.Tenant, transaction *domain.Transaction) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.transactions[transaction.ID]; exists {
		return domain.ErrInvalidTransaction
	}
	r.transactions[transaction.ID] = *transaction
	return nil
}

func (r *Store) Get(ctx context.Context, tenant domain.Tenant, id string) (*domain.Transaction, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	transaction, ok := r.transactions[id]
	if !ok || transaction.TenantID != tenant {
		return nil, domain.ErrTransactionNotFound
	}
	snapshot := transaction
	return &snapshot, nil
}

func (r *Store) List(ctx context.Context, tenant domain.Tenant, limit int) ([]*domain.Transaction, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if limit <= 0 {
		limit = 100
	}
	out := make([]*domain.Transaction, 0, len(r.transactions))
	for id := range r.transactions {
		transaction := r.transactions[id]
		if transaction.TenantID != tenant {
			continue
		}
		snapshot := transaction
		out = append(out, &snapshot)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (r *Store) SaveVoid(ctx context.Context, tenant domain.Tenant, transaction *domain.Transaction) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	existing, ok := r.transactions[transaction.ID]
	if !ok || existing.TenantID != tenant {
		return domain.ErrTransactionNotFound
	}
	r.transactions[transaction.ID] = *transaction
	return nil
}
