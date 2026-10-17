// Package application implements inventory use cases between the domain,
// persistence adapters, and delivery adapters.
package application

import (
	"context"

	"gogo/internal/modules/inventory/application/ports"
	"gogo/internal/modules/inventory/domain"
)

// Service implements inventory use cases. Persistence adapters own atomic
// storage mechanics; this service owns validation and the application boundary.
type Service struct {
	repository ports.StockStore
}

var _ ports.StockService = (*Service)(nil)

func NewService(repository ports.StockStore) *Service {
	return &Service{repository: repository}
}

func (s *Service) GetStock(ctx context.Context, tenant domain.Tenant, productID string) (*domain.Stock, error) {
	return s.repository.GetStock(ctx, tenant, productID)
}

func (s *Service) ListStocks(ctx context.Context, tenant domain.Tenant, productIDs []string) ([]*domain.Stock, error) {
	return s.repository.ListStocks(ctx, tenant, productIDs)
}

func (s *Service) AdjustStock(ctx context.Context, tenant domain.Tenant, productID string, delta int, kind, note, actor, serviceActor, reference string) (*domain.Stock, *domain.Movement, error) {
	if err := domain.NewAdjustment(tenant, productID, kind, actor, reference); err != nil {
		return nil, nil, err
	}
	return s.repository.AdjustStock(ctx, tenant, productID, delta, kind, note, actor, serviceActor, reference)
}

func (s *Service) ListMovements(ctx context.Context, tenant domain.Tenant, productID string, limit int) ([]*domain.Movement, error) {
	return s.repository.ListMovements(ctx, tenant, productID, limit)
}
