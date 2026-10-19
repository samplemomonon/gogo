// Package application holds the sales facade: it composes the transaction store with
// the inventory stock effect so a checkout and its stock change commit together, or
// neither does.
package application

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"gogo/internal/features/sales/application/ports"
	"gogo/internal/features/sales/domain"
)

var _ ports.SalesService = (*Service)(nil)

// Service implements ports.SalesService.
type Service struct {
	store ports.Store
	stock ports.StockAdjuster
}

func NewService(store ports.Store, stock ports.StockAdjuster) *Service {
	return &Service{store: store, stock: stock}
}

// Checkout computes the sale, reserves stock for every line through inventory
// (atomic and idempotent on the transaction number), then records the sale. A
// failure after some stock was reserved compensates the lines already applied.
func (s *Service) Checkout(ctx context.Context, tenant domain.Tenant, items []domain.CheckoutItem, discount, paid int64, method, cashier, employeeID string) (*domain.Transaction, error) {
	number := newNumber()
	transaction, err := domain.NewTransaction(tenant, uuid.NewString(), number, items, discount, paid, method, cashier, employeeID)
	if err != nil {
		return nil, err
	}

	applied := 0
	for _, item := range transaction.Items {
		if err := s.stock.Adjust(ctx, tenant, item.ProductID, -item.Quantity, "sale", reference(number, "sale", item.ProductID)); err != nil {
			s.compensate(ctx, tenant, number, transaction.Items[:applied])
			return nil, err
		}
		applied++
	}

	if err := s.store.Create(ctx, tenant, transaction); err != nil {
		s.compensate(ctx, tenant, number, transaction.Items)
		return nil, err
	}
	return transaction, nil
}

// Get returns one transaction.
func (s *Service) Get(ctx context.Context, tenant domain.Tenant, id string) (*domain.Transaction, error) {
	return s.store.Get(ctx, tenant, id)
}

// List returns recent transactions, newest first.
func (s *Service) List(ctx context.Context, tenant domain.Tenant, limit int) ([]*domain.Transaction, error) {
	return s.store.List(ctx, tenant, limit)
}

// Void restores stock for every line (idempotent on the transaction number) and
// then marks the sale voided. A second void is rejected.
func (s *Service) Void(ctx context.Context, tenant domain.Tenant, id, by, reason string) (*domain.Transaction, error) {
	transaction, err := s.store.Get(ctx, tenant, id)
	if err != nil {
		return nil, err
	}
	if transaction.VoidedAt != nil {
		return nil, domain.ErrAlreadyVoided
	}

	for _, item := range transaction.Items {
		if err := s.stock.Adjust(ctx, tenant, item.ProductID, item.Quantity, "void", reference(transaction.Number, "void", item.ProductID)); err != nil {
			return nil, err
		}
	}

	if err := transaction.Void(by, reason); err != nil {
		return nil, err
	}
	if err := s.store.SaveVoid(ctx, tenant, transaction); err != nil {
		return nil, err
	}
	return transaction, nil
}

// compensate returns the stock a failed checkout had already reserved, keyed so a
// retry cannot double-apply.
func (s *Service) compensate(ctx context.Context, tenant domain.Tenant, number string, items []domain.Item) {
	for _, item := range items {
		_ = s.stock.Adjust(ctx, tenant, item.ProductID, item.Quantity, "void", reference(number, "comp", item.ProductID))
	}
}

func reference(number, kind, productID string) string {
	return number + ":" + kind + ":" + productID
}

func newNumber() string {
	return "TRX-" + time.Now().Format("20060102-150405") + "-" + strings.ToUpper(uuid.NewString()[:6])
}
