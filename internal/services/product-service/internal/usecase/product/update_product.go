// Package product contains the use cases for product operations.
package product

import (
	"context"
	"fmt"

	"product-service/internal/domain/product"
	"product-service/internal/ports"
)

// UpdateProductUseCase handles product updates.
type UpdateProductUseCase struct {
	deps UseCaseDependencies
	refs *referenceValidator
}

// NewUpdateProductUseCase creates a new update product use case.
func NewUpdateProductUseCase(deps UseCaseDependencies) *UpdateProductUseCase {
	return &UpdateProductUseCase{deps: deps, refs: newReferenceValidator(deps.CategoryLookup, deps.MediaLookup, deps.DefaultCurrency)}
}

// Execute updates an existing product.
func (uc *UpdateProductUseCase) Execute(ctx context.Context, id int64, req *product.UpdateProductRequest) (*product.Product, error) {
	if req.IsEmpty() {
		return nil, product.NewValidationError("body", product.ErrInvalidInput)
	}

	// Get existing product to validate it exists
	existing, err := uc.deps.ProductRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	// Validate category reference if changed
	if req.HasCategoryID() {
		if err := uc.refs.requireCategory(ctx, *req.CategoryID); err != nil {
			return nil, err
		}
	}

	// Validate media reference if changed
	if req.HasPrimaryMediaID() && req.PrimaryMediaID != nil {
		if err := uc.refs.requireMedia(ctx, req.PrimaryMediaID); err != nil {
			return nil, err
		}
	}

	// Validate currency if changed
	if req.HasCurrency() {
		if _, err := uc.refs.resolveCurrency(string(*req.Currency)); err != nil {
			return nil, err
		}
	}

	// Apply updates to product entity
	if err := existing.Update(req); err != nil {
		return nil, err
	}

	// Persist in transaction with events
	updated, err := uc.persistProduct(ctx, existing)
	if err != nil {
		return nil, err
	}

	// Clear variants from response (they're not included in update response)
	updated.Variants = nil

	return updated, nil
}

// persistProduct saves the product in a transaction with events.
func (uc *UpdateProductUseCase) persistProduct(ctx context.Context, p *product.Product) (*product.Product, error) {
	tx, err := uc.deps.ProductRepo.(interface {
		BeginTx(context.Context) (ports.Transaction, error)
	}).BeginTx(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	updated, err := uc.deps.ProductRepo.UpdateProduct(ctx, p)
	if err != nil {
		return nil, err
	}

	// Record events
	events, err := uc.deps.EventRecorder.ProductUpdated(updated)
	if err != nil {
		return nil, fmt.Errorf("build product updated events: %w", err)
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
