// Package product contains the core domain models and business rules for products.
package product

import (
	"math"
	"strings"
	"time"
	"unicode/utf8"
)

// Product represents a sellable item in the catalog.
// This is the aggregate root for the product domain.
type Product struct {
	ID             int64
	CategoryID     int64
	Name           string
	Slug           string
	Description    *string
	Brand          *string
	Price          Money
	Stock          int64
	Status         Status
	PrimaryMediaID *int64
	Variants       []*Variant
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Product constants for validation.
const (
	MaxNameLength         = 255
	MaxSlugLength         = 255
	MaxBrandLength        = 255
	MaxDescriptionLength  = 10_000
	MaxVariantsPerProduct = 100
)

// NewProduct creates a new product with validation.
// This is a factory method that ensures the product is valid at creation.
func NewProduct(
	categoryID int64,
	name string,
	slug string,
	description *string,
	brand *string,
	price Money,
	stock int64,
	status Status,
	primaryMediaID *int64,
) (*Product, error) {
	if err := validateName(name); err != nil {
		return nil, err
	}
	if err := validateSlug(slug); err != nil {
		return nil, err
	}
	if err := validateDescription(description); err != nil {
		return nil, err
	}
	if err := validateBrand(brand); err != nil {
		return nil, err
	}
	if stock < 0 {
		return nil, NewValidationError("stock", ErrNegativeStock)
	}
	if err := price.validate(); err != nil {
		return nil, NewValidationError("price", err)
	}
	if categoryID <= 0 {
		return nil, NewValidationError("category_id", ErrInvalidInput)
	}
	if status == "" {
		status = StatusDraft
	}
	if err := validateStatus(status); err != nil {
		return nil, err
	}
	if err := validateMediaID(primaryMediaID); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	return &Product{
		CategoryID:     categoryID,
		Name:           strings.TrimSpace(name),
		Slug:           strings.ToLower(strings.TrimSpace(slug)),
		Description:    normalizeOptionalString(description),
		Brand:          normalizeOptionalString(brand),
		Price:          price,
		Stock:          stock,
		Status:         status,
		PrimaryMediaID: primaryMediaID,
		Variants:       make([]*Variant, 0),
		CreatedAt:      now,
		UpdatedAt:      now,
	}, nil
}

// validateName checks the product name constraints.
func validateName(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return NewValidationError("name", ErrEmptyName)
	}
	if utf8.RuneCountInString(trimmed) > MaxNameLength {
		return NewValidationError("name", ErrNameTooLong)
	}
	return nil
}

// validateSlug checks the slug format constraints.
func validateSlug(slug string) error {
	trimmed := strings.TrimSpace(slug)
	if trimmed == "" {
		return NewValidationError("slug", ErrInvalidSlug)
	}
	if utf8.RuneCountInString(trimmed) > MaxSlugLength {
		return NewValidationError("slug", ErrSlugTooLong)
	}
	for i := 0; i < len(trimmed); i++ {
		c := trimmed[i]
		isLower := c >= 'a' && c <= 'z'
		isDigit := c >= '0' && c <= '9'
		if isLower || isDigit || c == '-' {
			continue
		}
		return NewValidationError("slug", ErrInvalidSlug)
	}
	if strings.HasPrefix(trimmed, "-") || strings.HasSuffix(trimmed, "-") {
		return NewValidationError("slug", ErrInvalidSlug)
	}
	return nil
}

// validateDescription checks the description length constraint.
func validateDescription(description *string) error {
	if description != nil && utf8.RuneCountInString(*description) > MaxDescriptionLength {
		return NewValidationError("description", ErrDescriptionTooLong)
	}
	return nil
}

// validateBrand checks the brand length constraint.
func validateBrand(brand *string) error {
	if brand != nil && utf8.RuneCountInString(*brand) > MaxBrandLength {
		return NewValidationError("brand", ErrBrandTooLong)
	}
	return nil
}

// validateCurrency checks that the currency is a non-empty, well-formed code.
func validateCurrency(currency string) error {
	c := strings.ToUpper(strings.TrimSpace(currency))
	if c == "" {
		return ErrInvalidCurrency
	}
	if len(c) != 3 {
		return ErrInvalidCurrency
	}
	for i := 0; i < len(c); i++ {
		if c[i] < 'A' || c[i] > 'Z' {
			return ErrInvalidCurrency
		}
	}
	return nil
}

// validateStatus checks that the status is a known value.
func validateStatus(status Status) error {
	if !status.IsValid() {
		return NewValidationError("status", ErrInvalidStatus)
	}
	return nil
}

// validateMediaID checks that a referenced media ID is positive when set.
func validateMediaID(id *int64) error {
	if id != nil && *id <= 0 {
		return NewValidationError("primary_media_id", ErrInvalidInput)
	}
	return nil
}

// normalizeOptionalString trims an optional string value.
func normalizeOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	v := strings.TrimSpace(*value)
	return &v
}

// Update applies partial updates to the product.
// Only fields present in the request are updated.
// The update is atomic: if any validation fails, the product is left unchanged.
func (p *Product) Update(req *UpdateProductRequest) error {
	if p == nil {
		return NewValidationError("product", ErrInvalidInput)
	}
	if req == nil {
		return NewValidationError("request", ErrInvalidInput)
	}

	var (
		name           = p.Name
		slug           = p.Slug
		description    = p.Description
		brand          = p.Brand
		price          = p.Price
		stock          = p.Stock
		status         = p.Status
		categoryID     = p.CategoryID
		primaryMediaID = p.PrimaryMediaID
	)

	if req.HasName() {
		if err := validateName(*req.Name); err != nil {
			return err
		}
		name = strings.TrimSpace(*req.Name)
	}
	if req.HasSlug() {
		if err := validateSlug(*req.Slug); err != nil {
			return err
		}
		slug = strings.ToLower(strings.TrimSpace(*req.Slug))
	}
	if req.HasDescription() {
		if err := validateDescription(req.Description); err != nil {
			return err
		}
		description = normalizeOptionalString(req.Description)
	}
	if req.HasBrand() {
		if err := validateBrand(req.Brand); err != nil {
			return err
		}
		brand = normalizeOptionalString(req.Brand)
	}
	if req.HasPriceCents() {
		if *req.PriceCents < 0 {
			return NewValidationError("price_cents", ErrNegativePrice)
		}
		price.Amount = *req.PriceCents
	}
	if req.HasStock() {
		if *req.Stock < 0 {
			return NewValidationError("stock", ErrNegativeStock)
		}
		stock = *req.Stock
	}
	if req.HasCurrency() {
		c := Currency(strings.ToUpper(strings.TrimSpace(string(*req.Currency))))
		if err := validateCurrency(string(c)); err != nil {
			return NewValidationError("currency", err)
		}
		price.Currency = c
	}
	if req.HasStatus() {
		if err := validateStatus(*req.Status); err != nil {
			return err
		}
		status = *req.Status
	}
	if req.HasCategoryID() {
		if *req.CategoryID <= 0 {
			return NewValidationError("category_id", ErrInvalidInput)
		}
		categoryID = *req.CategoryID
	}
	if req.HasPrimaryMediaID() {
		if err := validateMediaID(req.PrimaryMediaID); err != nil {
			return err
		}
		primaryMediaID = req.PrimaryMediaID
	}

	p.Name = name
	p.Slug = slug
	p.Description = description
	p.Brand = brand
	p.Price = price
	p.Stock = stock
	p.Status = status
	p.CategoryID = categoryID
	p.PrimaryMediaID = primaryMediaID
	p.UpdatedAt = time.Now().UTC()
	return nil
}

// AdjustStock adds delta to the product's stock.
// Returns an error if the adjustment would make stock negative or overflow.
func (p *Product) AdjustStock(delta int64) error {
	if p == nil {
		return NewValidationError("product", ErrInvalidInput)
	}
	if delta > 0 && p.Stock > math.MaxInt64-delta {
		return ErrStockOverflow
	}
	if delta < 0 && p.Stock < -delta {
		return ErrNegativeStock
	}
	p.Stock += delta
	p.UpdatedAt = time.Now().UTC()
	return nil
}

// CanAddVariant reports whether another variant can be added.
func (p *Product) CanAddVariant() error {
	if p == nil {
		return NewValidationError("product", ErrInvalidInput)
	}
	if len(p.Variants) >= MaxVariantsPerProduct {
		return ErrMaxVariantsExceeded
	}
	return nil
}

// AddVariant adds a variant to the product.
// The aggregate enforces its own invariants: the variant must be non-nil,
// the variant cap must not be exceeded, and the SKU must not be duplicated.
func (p *Product) AddVariant(variant *Variant) error {
	if p == nil {
		return NewValidationError("product", ErrInvalidInput)
	}
	if variant == nil {
		return NewValidationError("variant", ErrInvalidInput)
	}
	if len(p.Variants) >= MaxVariantsPerProduct {
		return ErrMaxVariantsExceeded
	}
	for _, v := range p.Variants {
		if v != nil && v.SKU == variant.SKU {
			return NewValidationError("sku", ErrDuplicateSKU)
		}
	}
	p.Variants = append(p.Variants, variant)
	p.UpdatedAt = time.Now().UTC()
	return nil
}

// UpdateVariant applies a partial update to the variant with the given ID.
// The variant's own invariants are preserved by delegating to Variant.Update.
func (p *Product) UpdateVariant(variantID int64, req *UpdateVariantRequest) error {
	if p == nil {
		return NewValidationError("product", ErrInvalidInput)
	}
	if req == nil {
		return NewValidationError("request", ErrInvalidInput)
	}
	variant, err := p.GetVariant(variantID)
	if err != nil {
		return err
	}
	if err := variant.Update(req); err != nil {
		return err
	}
	p.UpdatedAt = time.Now().UTC()
	return nil
}

// RemoveVariant removes a variant from the product.
func (p *Product) RemoveVariant(variantID int64) error {
	if p == nil {
		return NewValidationError("product", ErrInvalidInput)
	}
	for i, v := range p.Variants {
		if v != nil && v.ID == variantID {
			p.Variants = append(p.Variants[:i], p.Variants[i+1:]...)
			p.UpdatedAt = time.Now().UTC()
			return nil
		}
	}
	return ErrVariantNotFound
}

// GetVariant returns a variant by ID.
func (p *Product) GetVariant(variantID int64) (*Variant, error) {
	if p == nil {
		return nil, NewValidationError("product", ErrInvalidInput)
	}
	for _, v := range p.Variants {
		if v != nil && v.ID == variantID {
			return v, nil
		}
	}
	return nil, ErrVariantNotFound
}

// Snapshot returns a deep copy of the product for event publishing.
func (p *Product) Snapshot() *Product {
	if p == nil {
		return nil
	}
	snap := *p
	if p.Variants != nil {
		snap.Variants = make([]*Variant, len(p.Variants))
		for i, variant := range p.Variants {
			if variant != nil {
				snap.Variants[i] = variant.Snapshot()
			}
		}
	}
	if p.Description != nil {
		d := *p.Description
		snap.Description = &d
	}
	if p.Brand != nil {
		b := *p.Brand
		snap.Brand = &b
	}
	if p.PrimaryMediaID != nil {
		m := *p.PrimaryMediaID
		snap.PrimaryMediaID = &m
	}
	return &snap
}
