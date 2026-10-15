// Package product contains the use cases for product operations.
package product

import (
	"context"
	"fmt"

	"product-service/internal/domain/product"
	"product-service/internal/ports"
)

// DeleteVariantUseCase handles variant deletion.
type DeleteVariantUseCase struct {
	deps UseCaseDependencies
}

// NewDeleteVariantUseCase creates a new delete variant use case.
func NewDeleteVariantUseCase(deps UseCaseDependencies) *DeleteVariantUseCase {
	return &DeleteVariantUseCase{deps: deps}
}

// Execute deletes a variant.
func (uc *DeleteVariantUseCase) Execute(ctx context.Context, productID, variantID int64) error {
	// Get variant to validate it exists and belongs to the product
	variant, err := uc.deps.VariantRepo.GetVariantByID(ctx, variantID)
	if err != nil {
		return err
	}
	if variant.ProductID != productID {
		return product.NewValidationError("variant_id", product.ErrVariantNotOwnedByProduct)
	}

	tx, err := uc.deps.ProductRepo.(interface {
		BeginTx(context.Context) (ports.Transaction, error)
	}).BeginTx(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := uc.deps.VariantRepo.DeleteVariant(ctx, productID, variantID); err != nil {
		return err
	}

	// Record events
	events, err := uc.deps.EventRecorder.VariantDeleted(productID, variantID)
	if err != nil {
		return fmt.Errorf("build variant deleted events: %w", err)
	}

	if len(events) > 0 {
		if _, err := uc.deps.EventSink.Append(ctx, tx, events); err != nil {
			return fmt.Errorf("append events: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	// Publish product republish event after commit (derived event)
	go uc.publishProductAfterVariant(ctx, productID)

	return nil
}

// publishProductAfterVariant republishes the product after a variant change.
func (uc *DeleteVariantUseCase) publishProductAfterVariant(ctx context.Context, productID int64) {
	p, err := uc.deps.ProductRepo.GetByID(ctx, productID)
	if err != nil {
		return
	}

	event, err := uc.deps.EventRecorder.ProductUpdated(p)
	if err != nil {
		return
	}

	if len(event) > 0 {
		_ = uc.deps.OutboxRecorder.Append(ctx, event[0])
	}
}
