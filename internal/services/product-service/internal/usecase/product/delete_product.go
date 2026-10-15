// Package product contains the use cases for product operations.
package product

import (
	"context"
	"fmt"

	"product-service/internal/ports"
)

// DeleteProductUseCase handles product deletion.
type DeleteProductUseCase struct {
	deps UseCaseDependencies
}

// NewDeleteProductUseCase creates a new delete product use case.
func NewDeleteProductUseCase(deps UseCaseDependencies) *DeleteProductUseCase {
	return &DeleteProductUseCase{deps: deps}
}

// Execute deletes a product and its variants.
func (uc *DeleteProductUseCase) Execute(ctx context.Context, id int64) error {
	// Check if product exists
	if _, err := uc.deps.ProductRepo.GetByID(ctx, id); err != nil {
		return err
	}

	tx, err := uc.deps.ProductRepo.(interface {
		BeginTx(context.Context) (ports.Transaction, error)
	}).BeginTx(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := uc.deps.ProductRepo.DeleteProduct(ctx, id); err != nil {
		return err
	}

	// Record events
	events, err := uc.deps.EventRecorder.ProductDeleted(id)
	if err != nil {
		return fmt.Errorf("build product deleted events: %w", err)
	}

	if len(events) > 0 {
		if _, err := uc.deps.EventSink.Append(ctx, tx, events); err != nil {
			return fmt.Errorf("append events: %w", err)
		}
	}

	return tx.Commit()
}
