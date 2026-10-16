// Package product contains the use cases for product operations.
package product

import (
	"context"
	"fmt"
	"strings"

	"product-service/internal/domain/product"
	"product-service/internal/ports"
)

// CreateVariantUseCase handles variant creation.
type CreateVariantUseCase struct {
	deps UseCaseDependencies
	refs *referenceValidator
}

// NewCreateVariantUseCase creates a new create variant use case.
func NewCreateVariantUseCase(deps UseCaseDependencies) *CreateVariantUseCase {
	return &CreateVariantUseCase{deps: deps, refs: newReferenceValidator(deps.CategoryLookup, deps.MediaLookup, deps.DefaultCurrency)}
}

// Execute creates a new variant for a product.
func (uc *CreateVariantUseCase) Execute(ctx context.Context, productID int64, req *product.CreateVariantRequest) (*product.Variant, error) {
	// Get product (with variants) to validate it exists and check variant limit
	p, err := uc.deps.ProductRepo.GetByIDWithVariants(ctx, productID)
	if err != nil {
		return nil, err
	}

	// Check variant limit using the aggregate's own state
	if err := p.CanAddVariant(); err != nil {
		return nil, err
	}

	// Prepare variant
	variant, err := uc.prepareVariant(req, p.Price.Currency, productID)
	if err != nil {
		return nil, err
	}

	// Validate media reference
	if err := uc.refs.requireMedia(ctx, variant.MediaID); err != nil {
		return nil, err
	}

	tx, err := uc.deps.ProductRepo.(interface {
		BeginTx(context.Context) (ports.Transaction, error)
	}).BeginTx(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	created, err := uc.deps.VariantRepo.CreateVariant(ctx, variant)
	if err != nil {
		return nil, err
	}

	// Record variant event
	events, err := uc.deps.EventRecorder.VariantCreated(created)
	if err != nil {
		return nil, fmt.Errorf("build variant created events: %w", err)
	}

	if len(events) > 0 {
		if _, err := uc.deps.EventSink.Append(ctx, tx, events); err != nil {
			return nil, fmt.Errorf("append events: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit transaction: %w", err)
	}

	// Publish product republish event after commit (derived event)
	go uc.publishProductAfterVariant(ctx, productID)

	return created, nil
}

// prepareVariant creates a variant entity from a spec.
func (uc *CreateVariantUseCase) prepareVariant(req *product.CreateVariantRequest, fallbackCurrency product.Currency, productID int64) (*product.Variant, error) {
	currency := fallbackCurrency
	if strings.TrimSpace(req.Currency) != "" {
		resolved, err := uc.refs.resolveCurrency(req.Currency)
		if err != nil {
			return nil, err
		}
		currency = resolved
	}

	price, err := product.NewMoney(req.PriceCents, currency)
	if err != nil {
		return nil, err
	}
	return product.NewVariant(
		productID,
		req.SKU,
		req.Name,
		price,
		req.Stock,
		req.MediaID,
	)
}

// publishProductAfterVariant republishes the product after a variant change.
func (uc *CreateVariantUseCase) publishProductAfterVariant(ctx context.Context, productID int64) {
	p, err := uc.deps.ProductRepo.GetByID(ctx, productID)
	if err != nil {
		// Log error but don't fail the request
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
