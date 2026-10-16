// Package product contains the core domain models and business rules for products.
package product

import (
	"encoding/json"
	"fmt"
)

// CreateProductRequest is the input for creating a product.
type CreateProductRequest struct {
	ProductTypeID  string                 `json:"product_type_id"`
	SchemaVersion  int32                  `json:"schema_version"`
	Attributes     map[string]string      `json:"attributes"`
	CategoryID     int64                  `json:"category_id"`
	Name           string                 `json:"name"`
	Slug           string                 `json:"slug"`
	Description    *string                `json:"description"`
	Brand          *string                `json:"brand"`
	PriceCents     int64                  `json:"price_cents"`
	Currency       string                 `json:"currency"`
	Stock          int64                  `json:"stock"`
	Status         Status                 `json:"status"`
	PrimaryMediaID *int64                 `json:"primary_media_id"`
	Variants       []CreateVariantRequest `json:"variants"`
}

// CreateVariantRequest is the input for creating a variant.
type CreateVariantRequest struct {
	SKU        string `json:"sku"`
	Name       string `json:"name"`
	PriceCents int64  `json:"price_cents"`
	Currency   string `json:"currency"`
	Stock      int64  `json:"stock"`
	MediaID    *int64 `json:"media_id"`
}

// UpdateProductRequest is the input for updating a product.
// Uses pointers to distinguish between "not provided" and "set to null".
type UpdateProductRequest struct {
	ProductTypeID  *string
	SchemaVersion  *int32
	Attributes     map[string]string
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

// updateProductFields is the set of keys an update product body may contain.
var updateProductFields = map[string]bool{
	"product_type_id":  true,
	"schema_version":   true,
	"attributes":       true,
	"category_id":      true,
	"name":             true,
	"slug":             true,
	"description":      true,
	"brand":            true,
	"price_cents":      true,
	"currency":         true,
	"stock":            true,
	"status":           true,
	"primary_media_id": true,
}

// UnmarshalJSON decodes the body, rejecting unknown keys and remembering which
// ones were present even when their value is null. A plain pointer cannot
// express the difference between "field omitted" and "field set to null": the
// first leaves the column alone, the second clears it for the nullable fields.
//
// The strict per-field decoding is deliberate. It runs because a custom
// unmarshaller takes over decoding, so the HTTP handler's
// DisallowUnknownFields no longer applies, and it keeps null out of the
// non-nullable scalar fields whose Has* accessors the domain dereferences.
func (r *UpdateProductRequest) UnmarshalJSON(data []byte) error {
	raw, err := decodeUpdateObject(data)
	if err != nil {
		return err
	}
	for key := range raw {
		if !updateProductFields[key] {
			return fmt.Errorf("unknown field %q", key)
		}
	}

	r.present = make(map[string]bool, len(raw))
	for key, value := range raw {
		r.present[key] = true

		switch key {
		case "category_id":
			if r.CategoryID, err = decodeUpdateInt(value, key); err != nil {
				return err
			}
		case "name":
			if r.Name, err = decodeUpdateString(value, key); err != nil {
				return err
			}
		case "slug":
			if r.Slug, err = decodeUpdateString(value, key); err != nil {
				return err
			}
		case "description":
			if r.Description, err = decodeNullableString(value, key); err != nil {
				return err
			}
		case "brand":
			if r.Brand, err = decodeNullableString(value, key); err != nil {
				return err
			}
		case "price_cents":
			if r.PriceCents, err = decodeUpdateInt(value, key); err != nil {
				return err
			}
		case "currency":
			if r.Currency, err = decodeUpdateCurrency(value); err != nil {
				return err
			}
		case "stock":
			if r.Stock, err = decodeUpdateInt(value, key); err != nil {
				return err
			}
		case "status":
			if r.Status, err = decodeUpdateStatus(value); err != nil {
				return err
			}
		case "primary_media_id":
			if r.PrimaryMediaID, err = decodeNullableInt(value, key); err != nil {
				return err
			}
		case "product_type_id":
			if r.ProductTypeID, err = decodeUpdateString(value, key); err != nil {
				return err
			}
		case "schema_version":
			number, decodeErr := decodeUpdateInt(value, key)
			if decodeErr != nil {
				err = decodeErr
				return err
			}
			if *number <= 0 || *number > 2147483647 {
				return fmt.Errorf("schema_version must be a positive 32-bit integer")
			}
			v := int32(*number)
			r.SchemaVersion = &v
		case "attributes":
			if err = json.Unmarshal(value, &r.Attributes); err != nil || r.Attributes == nil {
				return fmt.Errorf("attributes must be an object of string values")
			}
		}
	}
	return nil
}

// UpdateFields carries an update as one pointer per updatable field.
// Used by gRPC adapter to build UpdateProductRequest.
type UpdateFields struct {
	ProductTypeID  *string
	SchemaVersion  *int32
	Attributes     map[string]string
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
	if f.ProductTypeID != nil {
		r.ProductTypeID, r.present["product_type_id"] = f.ProductTypeID, true
	}
	if f.SchemaVersion != nil {
		r.SchemaVersion, r.present["schema_version"] = f.SchemaVersion, true
	}
	if f.Attributes != nil {
		r.Attributes, r.present["attributes"] = f.Attributes, true
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
func (r *UpdateProductRequest) HasProductTypeID() bool  { return r.present["product_type_id"] }
func (r *UpdateProductRequest) HasSchemaVersion() bool  { return r.present["schema_version"] }
func (r *UpdateProductRequest) HasAttributes() bool     { return r.present["attributes"] }

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

// updateVariantFields is the set of keys an update variant body may contain.
var updateVariantFields = map[string]bool{
	"sku":         true,
	"name":        true,
	"price_cents": true,
	"currency":    true,
	"stock":       true,
	"media_id":    true,
}

// UnmarshalJSON decodes the body, rejecting unknown keys and remembering which
// ones were present even when their value is null, mirroring
// UpdateProductRequest.UnmarshalJSON.
func (r *UpdateVariantRequest) UnmarshalJSON(data []byte) error {
	raw, err := decodeUpdateObject(data)
	if err != nil {
		return err
	}
	for key := range raw {
		if !updateVariantFields[key] {
			return fmt.Errorf("unknown field %q", key)
		}
	}

	r.present = make(map[string]bool, len(raw))
	for key, value := range raw {
		r.present[key] = true

		switch key {
		case "sku":
			if r.SKU, err = decodeUpdateString(value, key); err != nil {
				return err
			}
		case "name":
			if r.Name, err = decodeUpdateString(value, key); err != nil {
				return err
			}
		case "price_cents":
			if r.PriceCents, err = decodeUpdateInt(value, key); err != nil {
				return err
			}
		case "currency":
			if r.Currency, err = decodeUpdateCurrency(value); err != nil {
				return err
			}
		case "stock":
			if r.Stock, err = decodeUpdateInt(value, key); err != nil {
				return err
			}
		case "media_id":
			if r.MediaID, err = decodeNullableInt(value, key); err != nil {
				return err
			}
		}
	}
	return nil
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

// decodeUpdateObject decodes an update body into its raw key/value pairs.
func decodeUpdateObject(data []byte) (map[string]json.RawMessage, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func isJSONNull(raw json.RawMessage) bool {
	return string(raw) == "null"
}

// decodeUpdateString decodes a non-nullable string field. Explicit null is
// rejected because the field cannot be cleared and the domain dereferences it.
func decodeUpdateString(raw json.RawMessage, field string) (*string, error) {
	if isJSONNull(raw) {
		return nil, fmt.Errorf("%s must be a string", field)
	}
	return decodeNullableString(raw, field)
}

// decodeNullableString decodes a string field where null means "clear".
func decodeNullableString(raw json.RawMessage, field string) (*string, error) {
	if isJSONNull(raw) {
		return nil, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("%s must be a string: %w", field, err)
	}
	return &value, nil
}

// decodeUpdateInt decodes a non-nullable integer field, rejecting null.
func decodeUpdateInt(raw json.RawMessage, field string) (*int64, error) {
	if isJSONNull(raw) {
		return nil, fmt.Errorf("%s must be an integer", field)
	}
	return decodeNullableInt(raw, field)
}

// decodeNullableInt decodes an integer field where null means "clear".
func decodeNullableInt(raw json.RawMessage, field string) (*int64, error) {
	if isJSONNull(raw) {
		return nil, nil
	}
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("%s must be an integer: %w", field, err)
	}
	return &value, nil
}

// decodeUpdateCurrency decodes the currency field into its domain type.
func decodeUpdateCurrency(raw json.RawMessage) (*Currency, error) {
	value, err := decodeUpdateString(raw, "currency")
	if err != nil {
		return nil, err
	}
	currency := Currency(*value)
	return &currency, nil
}

// decodeUpdateStatus decodes the status field into its domain type.
func decodeUpdateStatus(raw json.RawMessage) (*Status, error) {
	value, err := decodeUpdateString(raw, "status")
	if err != nil {
		return nil, err
	}
	status := Status(*value)
	return &status, nil
}
