// Package product contains the use cases for product operations.
package product

import (
	"context"
	"fmt"
	"strings"

	"product-service/internal/domain/product"
	"product-service/internal/ports"
)

// CreateProductUseCase handles product creation.
type CreateProductUseCase struct {
	deps UseCaseDependencies
	refs *referenceValidator
}

// NewCreateProductUseCase creates a new create product use case.
func NewCreateProductUseCase(deps UseCaseDependencies) *CreateProductUseCase {
	return &CreateProductUseCase{deps: deps, refs: newReferenceValidator(deps.CategoryLookup, deps.MediaLookup, deps.DefaultCurrency)}
}

// Execute creates a new product with validation and event emission.
func (uc *CreateProductUseCase) Execute(ctx context.Context, req *product.CreateProductRequest) (*product.Product, error) {
	if req.ProductTypeID == "" {
		req.ProductTypeID = "general"
	}
	if req.SchemaVersion == 0 {
		req.SchemaVersion = 1
	}
	if req.Attributes == nil {
		req.Attributes = map[string]string{}
	}
	if err := product.ValidateAttributes(req.ProductTypeID, req.SchemaVersion, req.Attributes); err != nil {
		return nil, err
	}
	if err := product.ValidateMerchantID(req.MerchantID); err != nil {
		return nil, err
	}
	// Validate and prepare variants
	variants, err := uc.prepareVariants(ctx, req.Variants, req.Currency)
	if err != nil {
		return nil, err
	}
	// Validate category reference
	if err := uc.refs.requireCategory(ctx, req.CategoryID); err != nil {
		return nil, err
	}

	// Validate media reference
	if err := uc.refs.requireMedia(ctx, req.PrimaryMediaID); err != nil {
		return nil, err
	}

	// Resolve currency
	currency, err := uc.refs.resolveCurrency(req.Currency)
	if err != nil {
		return nil, err
	}

	// Parse status
	status := req.Status
	if status == "" {
		status = product.StatusDraft
	}

	// Generate slug if not provided
	slug := strings.TrimSpace(req.Slug)
	if slug == "" {
		slug = product.Slugify(req.Name)
	}
	if err := product.ValidateSlug(slug); err != nil {
		return nil, err
	}

	price, err := product.NewMoney(req.PriceCents, currency)
	if err != nil {
		return nil, err
	}

	// Create product entity
	p, err := product.NewProduct(
		req.CategoryID,
		strings.TrimSpace(req.Name),
		slug,
		req.Description,
		req.Brand,
		price,
		req.Stock,
		status,
		req.PrimaryMediaID,
	)
	if err != nil {
		return nil, err
	}
	p.ProductTypeID = req.ProductTypeID
	p.SchemaVersion = req.SchemaVersion
	p.Attributes = cloneAttributeValues(req.Attributes)
	p.MerchantID = strings.TrimSpace(req.MerchantID)

	// Persist in transaction with events
	created, err := uc.persistProduct(ctx, p, variants)
	if err != nil {
		return nil, err
	}

	return created, nil
}

func cloneAttributeValues(values map[string]string) map[string]string {
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

// prepareVariants validates and prepares variant entities.
func (uc *CreateProductUseCase) prepareVariants(ctx context.Context, specs []product.CreateVariantRequest, fallbackCurrency string) ([]*product.Variant, error) {
	if len(specs) > product.MaxVariantsPerProduct {
		return nil, product.NewValidationError("variants", product.ErrMaxVariantsExceeded)
	}
	if len(specs) == 0 {
		return nil, nil
	}

	seen := make(map[string]bool, len(specs))
	variants := make([]*product.Variant, 0, len(specs))
	mediaIDs := make([]int64, 0, len(specs))

	for i := range specs {
		variant, err := uc.prepareVariant(&specs[i], product.Currency(fallbackCurrency))
		if err != nil {
			return nil, err
		}
		if seen[variant.SKU] {
			return nil, product.NewValidationError("sku", product.ErrDuplicateSKU)
		}
		seen[variant.SKU] = true

		if variant.MediaID != nil {
			mediaIDs = append(mediaIDs, *variant.MediaID)
		}
		variants = append(variants, variant)
	}

	// Batch validate media references
	if err := uc.refs.requireMediaBatch(ctx, mediaIDs); err != nil {
		return nil, err
	}

	return variants, nil
}

// prepareVariant creates a variant entity from a spec.
func (uc *CreateProductUseCase) prepareVariant(spec *product.CreateVariantRequest, fallbackCurrency product.Currency) (*product.Variant, error) {
	currency := fallbackCurrency
	if strings.TrimSpace(spec.Currency) != "" {
		resolved, err := uc.refs.resolveCurrency(spec.Currency)
		if err != nil {
			return nil, err
		}
		currency = resolved
	}

	price, err := product.NewMoney(spec.PriceCents, currency)
	if err != nil {
		return nil, err
	}
	return product.NewVariant(
		0, // ProductID will be set after product creation
		spec.SKU,
		spec.Name,
		price,
		spec.Stock,
		spec.MediaID,
	)
}

// persistProduct saves the product and variants in a transaction with events.
func (uc *CreateProductUseCase) persistProduct(ctx context.Context, p *product.Product, variants []*product.Variant) (*product.Product, error) {
	// Start transaction
	tx, err := uc.deps.ProductRepo.(interface {
		BeginTx(context.Context) (ports.Transaction, error)
	}).BeginTx(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Create product
	created, err := uc.deps.ProductRepo.CreateProduct(ctx, p, variants)
	if err != nil {
		return nil, err
	}

	// Set product ID on variants
	for _, v := range variants {
		v.ProductID = created.ID
	}

	// Record events in the same transaction
	events, err := uc.deps.EventRecorder.ProductCreated(created, variants)
	if err != nil {
		return nil, fmt.Errorf("build product created events: %w", err)
	}

	if len(events) > 0 {
		if _, err := uc.deps.EventSink.Append(ctx, tx, events); err != nil {
			return nil, fmt.Errorf("append events: %w", err)
		}
	}

	// Commit transaction
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit transaction: %w", err)
	}

	return created, nil
}
