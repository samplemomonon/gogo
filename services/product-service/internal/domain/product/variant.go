// Package product contains the core domain models and business rules for products.
package product

import (
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"
)

// Variant represents a purchasable variation of a product (size, color, bundle).
type Variant struct {
	ID        int64
	ProductID int64
	SKU       string
	Name      string
	Price     Money
	Stock     int64
	MediaID   *int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Variant constants for validation.
const (
	MaxSKULength = 64
)

// MarshalJSON renders the variant as the wire snapshot shared by the HTTP API
// and every variant event, mirroring Product.MarshalJSON: flat snake_case
// fields and the price flattened into price_cents plus currency.
func (v *Variant) MarshalJSON() ([]byte, error) {
	type snapshot struct {
		ID         int64     `json:"id"`
		ProductID  int64     `json:"product_id"`
		SKU        string    `json:"sku"`
		Name       string    `json:"name"`
		PriceCents int64     `json:"price_cents"`
		Currency   Currency  `json:"currency"`
		Stock      int64     `json:"stock"`
		MediaID    *int64    `json:"media_id"`
		CreatedAt  time.Time `json:"created_at"`
		UpdatedAt  time.Time `json:"updated_at"`
	}
	return json.Marshal(snapshot{
		ID:         v.ID,
		ProductID:  v.ProductID,
		SKU:        v.SKU,
		Name:       v.Name,
		PriceCents: v.Price.Amount,
		Currency:   v.Price.Currency,
		Stock:      v.Stock,
		MediaID:    v.MediaID,
		CreatedAt:  v.CreatedAt,
		UpdatedAt:  v.UpdatedAt,
	})
}

// NewVariant creates a new variant with validation.
func NewVariant(
	productID int64,
	sku string,
	name string,
	price Money,
	stock int64,
	mediaID *int64,
) (*Variant, error) {
	if err := validateSKU(sku); err != nil {
		return nil, err
	}
	if name == "" {
		name = sku
	} else if err := validateVariantName(name); err != nil {
		return nil, err
	}
	if stock < 0 {
		return nil, NewValidationError("stock", ErrNegativeStock)
	}
	if err := price.validate(); err != nil {
		return nil, NewValidationError("price", err)
	}
	if mediaID != nil && *mediaID <= 0 {
		return nil, NewValidationError("media_id", ErrInvalidInput)
	}

	now := time.Now().UTC()
	return &Variant{
		ProductID: productID,
		SKU:       strings.ToUpper(strings.TrimSpace(sku)),
		Name:      strings.TrimSpace(name),
		Price:     price,
		Stock:     stock,
		MediaID:   mediaID,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// validateSKU checks the SKU format constraints.
func validateSKU(sku string) error {
	trimmed := strings.TrimSpace(sku)
	if trimmed == "" {
		return NewValidationError("sku", ErrEmptySKU)
	}
	if utf8.RuneCountInString(trimmed) > MaxSKULength {
		return NewValidationError("sku", ErrSKUTooLong)
	}
	for i := 0; i < len(trimmed); i++ {
		c := trimmed[i]
		isUpper := c >= 'A' && c <= 'Z'
		isDigit := c >= '0' && c <= '9'
		if isUpper || isDigit || c == '-' || c == '_' || c == '.' {
			continue
		}
		return NewValidationError("sku", ErrInvalidSKU)
	}
	return nil
}

// validateVariantName checks the variant name constraints.
func validateVariantName(name string) error {
	trimmed := strings.TrimSpace(name)
	if utf8.RuneCountInString(trimmed) > MaxNameLength {
		return NewValidationError("name", ErrNameTooLong)
	}
	return nil
}

// Update applies partial updates to the variant.
// The update is atomic: if any validation fails, the variant is left unchanged.
func (v *Variant) Update(req *UpdateVariantRequest) error {
	if v == nil {
		return NewValidationError("variant", ErrInvalidInput)
	}
	if req == nil {
		return NewValidationError("request", ErrInvalidInput)
	}

	var (
		sku     = v.SKU
		name    = v.Name
		price   = v.Price
		stock   = v.Stock
		mediaID = v.MediaID
	)

	if req.HasSKU() {
		if err := validateSKU(*req.SKU); err != nil {
			return err
		}
		sku = strings.ToUpper(strings.TrimSpace(*req.SKU))
	}
	if req.HasName() {
		if err := validateVariantName(*req.Name); err != nil {
			return err
		}
		name = strings.TrimSpace(*req.Name)
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
	if req.HasMediaID() {
		if req.MediaID != nil && *req.MediaID <= 0 {
			return NewValidationError("media_id", ErrInvalidInput)
		}
		mediaID = req.MediaID
	}

	v.SKU = sku
	v.Name = name
	v.Price = price
	v.Stock = stock
	v.MediaID = mediaID
	v.UpdatedAt = time.Now().UTC()
	return nil
}

// Snapshot returns a copy of the variant for event publishing.
func (v *Variant) Snapshot() *Variant {
	if v == nil {
		return nil
	}
	snap := *v
	if v.MediaID != nil {
		m := *v.MediaID
		snap.MediaID = &m
	}
	return &snap
}
