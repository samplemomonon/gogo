// Package product contains the use cases for product operations.
package product

import (
	"context"
	"fmt"

	"product-service/internal/domain/product"
	"product-service/internal/ports"
)

// AdjustStockUseCase handles stock adjustments.
type AdjustStockUseCase struct {
	deps UseCaseDependencies
}

// NewAdjustStockUseCase creates a new adjust stock use case.
func NewAdjustStockUseCase(deps UseCaseDependencies) *AdjustStockUseCase {
	return &AdjustStockUseCase{deps: deps}
}

// Execute adjusts the stock of a product.
func (uc *AdjustStockUseCase) Execute(ctx context.Context, id int64, delta int64) (*product.Product, error) {
	if delta == 0 {
		return nil, product.NewValidationError("delta", product.ErrInvalidInput)
	}

	tx, err := uc.deps.ProductRepo.(interface {
		BeginTx(context.Context) (ports.Transaction, error)
	}).BeginTx(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	adjusted, err := uc.deps.ProductRepo.AdjustStock(ctx, id, delta)
	if err != nil {
		return nil, err
	}

	// Record events
	events, err := uc.deps.EventRecorder.ProductStockAdjusted(adjusted, delta)
	if err != nil {
		return nil, fmt.Errorf("build stock adjusted events: %w", err)
	}

	if len(events) > 0 {
		if _, err := uc.deps.EventSink.Append(ctx, tx, events); err != nil {
			return nil, fmt.Errorf("append events: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit transaction: %w", err)
	}

	return adjusted, nil
}
