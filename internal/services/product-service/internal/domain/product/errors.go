// Package product contains the core domain models and business rules for products.
package product

import "errors"

// Domain errors that represent business rule violations.
var (
	// ErrNotFound indicates the requested resource does not exist.
	ErrNotFound = errors.New("not found")

	// ErrProductNotFound indicates the requested product does not exist.
	ErrProductNotFound = errors.New("product not found")

	// ErrVariantNotFound indicates the requested variant does not exist.
	ErrVariantNotFound = errors.New("variant not found")

	// ErrInvalidInput indicates the request was malformed or semantically invalid.
	ErrInvalidInput = errors.New("invalid input")

	// ErrConflict indicates the request collided with the current state.
	ErrConflict = errors.New("conflict")

	// ErrCategoryNotFound indicates the referenced category does not exist.
	ErrCategoryNotFound = errors.New("category not found")

	// ErrMediaNotFound indicates the referenced media does not exist.
	ErrMediaNotFound = errors.New("media not found")

	// ErrMaxVariantsExceeded indicates the product has reached the maximum number of variants.
	ErrMaxVariantsExceeded = errors.New("maximum variants per product exceeded")

	// ErrNegativeStock indicates an operation would result in negative stock.
	ErrNegativeStock = errors.New("stock cannot be negative")

	// ErrNegativePrice indicates a price cannot be negative.
	ErrNegativePrice = errors.New("price cannot be negative")

	// ErrEmptyName indicates a name is required.
	ErrEmptyName = errors.New("name is required")

	// ErrEmptySKU indicates a SKU is required.
	ErrEmptySKU = errors.New("sku is required")

	// ErrInvalidSlug indicates the slug format is invalid.
	ErrInvalidSlug = errors.New("invalid slug format")

	// ErrInvalidSKU indicates the SKU format is invalid.
	ErrInvalidSKU = errors.New("invalid sku format")

	// ErrInvalidCurrency indicates the currency code is invalid.
	ErrInvalidCurrency = errors.New("invalid currency code")

	// ErrDescriptionTooLong indicates the description exceeds maximum length.
	ErrDescriptionTooLong = errors.New("description too long")

	// ErrNameTooLong indicates the name exceeds maximum length.
	ErrNameTooLong = errors.New("name too long")

	// ErrSKUTooLong indicates the SKU exceeds maximum length.
	ErrSKUTooLong = errors.New("sku too long")

	// ErrSlugTooLong indicates the slug exceeds maximum length.
	ErrSlugTooLong = errors.New("slug too long")

	// ErrBrandTooLong indicates the brand exceeds maximum length.
	ErrBrandTooLong = errors.New("brand too long")

	// ErrInvalidStatus indicates an unknown product status.
	ErrInvalidStatus = errors.New("invalid status")

	// ErrStockOverflow indicates a stock adjustment would overflow.
	ErrStockOverflow = errors.New("stock overflow")

	// ErrDuplicateSKU indicates a duplicate SKU in the same request.
	ErrDuplicateSKU = errors.New("duplicate sku in request")

	// ErrVariantNotOwnedByProduct indicates the variant does not belong to the product.
	ErrVariantNotOwnedByProduct = errors.New("variant does not belong to product")

	// ErrOutboxBlocked indicates a change was rejected because its event could not be recorded.
	ErrOutboxBlocked = errors.New("event could not be recorded")

	// ErrUnavailable indicates a dependency is unavailable.
	ErrUnavailable = errors.New("dependency unavailable")
)

// ValidationError wraps a domain error with a field name for detailed reporting.
type ValidationError struct {
	Field string
	Err   error
}

func (e *ValidationError) Error() string {
	return e.Field + ": " + e.Err.Error()
}

func (e *ValidationError) Unwrap() error {
	return e.Err
}

// NewValidationError creates a new validation error.
func NewValidationError(field string, err error) *ValidationError {
	return &ValidationError{Field: field, Err: err}
}
