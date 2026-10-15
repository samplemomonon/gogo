// Package product contains the use cases for product operations.
package product

import (
	"context"
	"fmt"

	"product-service/internal/domain/product"
	"product-service/internal/ports"
)

// UpdateVariantUseCase handles variant updates.
type UpdateVariantUseCase struct {
	deps UseCaseDependencies
	refs *referenceValidator
}

// NewUpdateVariantUseCase creates a new update variant use case.
func NewUpdateVariantUseCase(deps UseCaseDependencies) *UpdateVariantUseCase {
	return &UpdateVariantUseCase{deps: deps, refs: newReferenceValidator(deps.CategoryLookup, deps.MediaLookup, deps.DefaultCurrency)}
}

// Execute updates an existing variant.
func (uc *UpdateVariantUseCase) Execute(ctx context.Context, productID, variantID int64, req *product.UpdateVariantRequest) (*product.Variant, error) {
	if req.IsEmpty() {
		return nil, product.NewValidationError("body", product.ErrInvalidInput)
	}

	// Get variant to validate it exists and belongs to the product
	variant, err := uc.deps.VariantRepo.GetVariantByID(ctx, variantID)
	if err != nil {
		return nil, err
	}
	if variant.ProductID != productID {
		return nil, product.NewValidationError("variant_id", product.ErrVariantNotOwnedByProduct)
	}

	// Validate currency if changed
	if req.HasCurrency() {
		if _, err := uc.refs.resolveCurrency(string(*req.Currency)); err != nil {
			return nil, err
		}
	}

	// Validate media if changed
	if req.HasMediaID() && req.MediaID != nil {
		if err := uc.refs.requireMedia(ctx, req.MediaID); err != nil {
			return nil, err
		}
	}

	// Apply updates
	if err := variant.Update(req); err != nil {
		return nil, err
	}

	tx, err := uc.deps.ProductRepo.(interface {
		BeginTx(context.Context) (ports.Transaction, error)
	}).BeginTx(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	updated, err := uc.deps.VariantRepo.UpdateVariant(ctx, variant)
	if err != nil {
		return nil, err
	}

	// Record events
	events, err := uc.deps.EventRecorder.VariantUpdated(updated)
	if err != nil {
		return nil, fmt.Errorf("build variant updated events: %w", err)
	}

	if len(events) > 0 {
		if _, err := uc.deps.EventSink.Append(ctx, tx, events); err != nil {
			return nil, fmt.Errorf("append events: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit transaction: %w", err)
	}

	return updated, nil
}
