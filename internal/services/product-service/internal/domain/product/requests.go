// Package product contains the core domain models and business rules for products.
package product

// CreateProductRequest is the input for creating a product.
type CreateProductRequest struct {
	CategoryID     int64
	Name           string
	Slug           string
	Description    *string
	Brand          *string
	PriceCents     int64
	Currency       string
	Stock          int64
	Status         Status
	PrimaryMediaID *int64
	Variants       []CreateVariantRequest
}

// CreateVariantRequest is the input for creating a variant.
type CreateVariantRequest struct {
	SKU        string
	Name       string
	PriceCents int64
	Currency   string
	Stock      int64
	MediaID    *int64
}

// UpdateProductRequest is the input for updating a product.
// Uses pointers to distinguish between "not provided" and "set to null".
type UpdateProductRequest struct {
	CategoryID     *int64
	Name           *string
	Slug           *string
	Description    *string
	Brand          *string
	PriceCents     *int64
	Currency       *Currency
	Stock          *int64
	Status         *Status
	PrimaryMediaID *int64

	present map[string]bool
}

// UpdateFields carries an update as one pointer per updatable field.
// Used by gRPC adapter to build UpdateProductRequest.
type UpdateFields struct {
	CategoryID     *int64
	Name           *string
	Slug           *string
	Description    *string
	Brand          *string
	PriceCents     *int64
	Currency       *Currency
	Stock          *int64
	Status         *Status
	PrimaryMediaID *int64
}

// NewUpdateProductRequest creates an UpdateProductRequest from UpdateFields.
func NewUpdateProductRequest(f UpdateFields) *UpdateProductRequest {
	r := &UpdateProductRequest{
		present: make(map[string]bool, 10),
	}
	if f.CategoryID != nil {
		r.CategoryID, r.present["category_id"] = f.CategoryID, true
	}
	if f.Name != nil {
		r.Name, r.present["name"] = f.Name, true
	}
	if f.Slug != nil {
		r.Slug, r.present["slug"] = f.Slug, true
	}
	if f.Description != nil {
		r.Description, r.present["description"] = f.Description, true
	}
	if f.Brand != nil {
		r.Brand, r.present["brand"] = f.Brand, true
	}
	if f.PriceCents != nil {
		r.PriceCents, r.present["price_cents"] = f.PriceCents, true
	}
	if f.Currency != nil {
		r.Currency, r.present["currency"] = f.Currency, true
	}
	if f.Stock != nil {
		r.Stock, r.present["stock"] = f.Stock, true
	}
	if f.Status != nil {
		r.Status, r.present["status"] = f.Status, true
	}
	if f.PrimaryMediaID != nil {
		r.PrimaryMediaID, r.present["primary_media_id"] = f.PrimaryMediaID, true
	}
	return r
}

// HasCategoryID reports whether category_id was provided.
func (r *UpdateProductRequest) HasCategoryID() bool { return r.present["category_id"] }

// HasName reports whether name was provided.
func (r *UpdateProductRequest) HasName() bool { return r.present["name"] }

// HasSlug reports whether slug was provided.
func (r *UpdateProductRequest) HasSlug() bool { return r.present["slug"] }

// HasDescription reports whether description was provided.
func (r *UpdateProductRequest) HasDescription() bool { return r.present["description"] }

// HasBrand reports whether brand was provided.
func (r *UpdateProductRequest) HasBrand() bool { return r.present["brand"] }

// HasPriceCents reports whether price_cents was provided.
func (r *UpdateProductRequest) HasPriceCents() bool { return r.present["price_cents"] }

// HasCurrency reports whether currency was provided.
func (r *UpdateProductRequest) HasCurrency() bool { return r.present["currency"] }

// HasStock reports whether stock was provided.
func (r *UpdateProductRequest) HasStock() bool { return r.present["stock"] }

// HasStatus reports whether status was provided.
func (r *UpdateProductRequest) HasStatus() bool { return r.present["status"] }

// HasPrimaryMediaID reports whether primary_media_id was provided.
func (r *UpdateProductRequest) HasPrimaryMediaID() bool { return r.present["primary_media_id"] }

// IsEmpty reports whether the request would change nothing.
func (r *UpdateProductRequest) IsEmpty() bool { return len(r.present) == 0 }

// UpdateVariantRequest is the input for updating a variant.
type UpdateVariantRequest struct {
	SKU        *string
	Name       *string
	PriceCents *int64
	Currency   *Currency
	Stock      *int64
	MediaID    *int64

	present map[string]bool
}

// NewUpdateVariantRequest creates an UpdateVariantRequest from fields.
func NewUpdateVariantRequest(sku *string, name *string, priceCents *int64, currency *Currency, stock *int64, mediaID *int64) *UpdateVariantRequest {
	r := &UpdateVariantRequest{
		present: make(map[string]bool, 6),
	}
	if sku != nil {
		r.SKU, r.present["sku"] = sku, true
	}
	if name != nil {
		r.Name, r.present["name"] = name, true
	}
	if priceCents != nil {
		r.PriceCents, r.present["price_cents"] = priceCents, true
	}
	if currency != nil {
		r.Currency, r.present["currency"] = currency, true
	}
	if stock != nil {
		r.Stock, r.present["stock"] = stock, true
	}
	if mediaID != nil {
		r.MediaID, r.present["media_id"] = mediaID, true
	}
	return r
}

// HasSKU reports whether sku was provided.
func (r *UpdateVariantRequest) HasSKU() bool { return r.present["sku"] }

// HasName reports whether name was provided.
func (r *UpdateVariantRequest) HasName() bool { return r.present["name"] }

// HasPriceCents reports whether price_cents was provided.
func (r *UpdateVariantRequest) HasPriceCents() bool { return r.present["price_cents"] }

// HasCurrency reports whether currency was provided.
func (r *UpdateVariantRequest) HasCurrency() bool { return r.present["currency"] }

// HasStock reports whether stock was provided.
func (r *UpdateVariantRequest) HasStock() bool { return r.present["stock"] }

// HasMediaID reports whether media_id was provided.
func (r *UpdateVariantRequest) HasMediaID() bool { return r.present["media_id"] }

// IsEmpty reports whether the request would change nothing.
func (r *UpdateVariantRequest) IsEmpty() bool { return len(r.present) == 0 }

// AdjustStockRequest is the input for adjusting stock.
type AdjustStockRequest struct {
	Delta int64
}
