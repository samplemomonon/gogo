// Package product contains the core domain models and business rules for products.
package product

import (
	"encoding/json"
	"math"
	"strings"
	"time"
	"unicode/utf8"
)

// Product represents a sellable item in the catalog.
// This is the aggregate root for the product domain.
type Product struct {
	ID             int64
	ProductTypeID  string
	SchemaVersion  int32
	Attributes     map[string]string
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
	// MerchantID names the merchant that owns this product, inside gogo's
	// identity module. Empty means unowned. Ownership is fixed at creation —
	// moving a product between merchants is not a supported operation — and
	// every event carries it so the read models can enforce ownership too.
	MerchantID string
}

// Product constants for validation.
const (
	MaxNameLength         = 255
	MaxSlugLength         = 255
	MaxBrandLength        = 255
	MaxDescriptionLength  = 10_000
	MaxVariantsPerProduct = 100
	// MaxMerchantIDLength bounds the opaque merchant reference. gogo's
	// merchant IDs are UUIDs; the cap only stops a caller from storing
	// unbounded junk in a reference column.
	MaxMerchantIDLength = 128
)

// ValidateMerchantID checks the optional merchant reference. An empty ID is
// valid — it means unowned — but a non-empty one must fit the reference
// column's bound.
func ValidateMerchantID(id string) error {
	if utf8.RuneCountInString(strings.TrimSpace(id)) > MaxMerchantIDLength {
		return NewValidationError("merchant_id", ErrInvalidInput)
	}
	return nil
}

// MarshalJSON renders the product as the wire snapshot shared by the HTTP API
// and every product event.
//
// The shape is the contract product-listing-service and product-search-service
// project from the Kafka topic: flat snake_case fields and the price flattened
// into price_cents plus currency. Marshalling the entity's default shape would
// produce PascalCase keys and a nested Price object, which is not what the
// consumers decode, so the representation is pinned here in one place rather
// than assembled separately by each caller.
func (p *Product) MarshalJSON() ([]byte, error) {
	type snapshot struct {
		ID             int64             `json:"id"`
		ProductTypeID  string            `json:"product_type_id"`
		SchemaVersion  int32             `json:"schema_version"`
		Attributes     map[string]string `json:"attributes"`
		CategoryID     int64             `json:"category_id"`
		Name           string            `json:"name"`
		Slug           string            `json:"slug"`
		Description    *string           `json:"description"`
		Brand          *string           `json:"brand"`
		PriceCents     int64             `json:"price_cents"`
		Currency       Currency          `json:"currency"`
		Stock          int64             `json:"stock"`
		Status         Status            `json:"status"`
		PrimaryMediaID *int64            `json:"primary_media_id"`
		Variants       []*Variant        `json:"variants,omitempty"`
		CreatedAt      time.Time         `json:"created_at"`
		UpdatedAt      time.Time         `json:"updated_at"`
		// Omitted when empty so an unowned product's snapshot stays
		// byte-identical to the shape consumers decoded before the field
		// existed. Present, it is the ownership the read models enforce.
		MerchantID string `json:"merchant_id,omitempty"`
	}
	return json.Marshal(snapshot{
		ID:             p.ID,
		ProductTypeID:  p.ProductTypeID,
		SchemaVersion:  p.SchemaVersion,
		Attributes:     p.Attributes,
		CategoryID:     p.CategoryID,
		Name:           p.Name,
		Slug:           p.Slug,
		Description:    p.Description,
		Brand:          p.Brand,
		PriceCents:     p.Price.Amount,
		Currency:       p.Price.Currency,
		Stock:          p.Stock,
		Status:         p.Status,
		PrimaryMediaID: p.PrimaryMediaID,
		Variants:       p.Variants,
		CreatedAt:      p.CreatedAt,
		UpdatedAt:      p.UpdatedAt,
		MerchantID:     p.MerchantID,
	})
}

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
	// Zero represents a product that has not been assigned to a category.
	if categoryID < 0 {
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
		ProductTypeID:  "general",
		SchemaVersion:  1,
		Attributes:     map[string]string{},
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
		productTypeID  = p.ProductTypeID
		schemaVersion  = p.SchemaVersion
		attributes     = p.Attributes
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
		if *req.CategoryID < 0 {
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
	if req.HasProductTypeID() {
		productTypeID = strings.TrimSpace(*req.ProductTypeID)
	}
	if req.HasSchemaVersion() {
		schemaVersion = *req.SchemaVersion
	}
	if req.HasAttributes() {
		attributes = cloneAttributes(req.Attributes)
	}
	if req.HasProductTypeID() || req.HasSchemaVersion() || req.HasAttributes() {
		if err := ValidateAttributes(productTypeID, schemaVersion, attributes); err != nil {
			return err
		}
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
	p.ProductTypeID = productTypeID
	p.SchemaVersion = schemaVersion
	p.Attributes = attributes
	p.UpdatedAt = time.Now().UTC()
	return nil
}

func cloneAttributes(values map[string]string) map[string]string {
	copy := make(map[string]string, len(values))
	for key, value := range values {
		copy[key] = value
	}
	return copy
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
