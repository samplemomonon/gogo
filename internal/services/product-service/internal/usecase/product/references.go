package product

import (
	"context"
	"strings"

	"product-service/internal/domain/product"
	"product-service/internal/ports"
)

// referenceValidator centralizes cross-aggregate reference and currency
// validation shared by the product use cases.
type referenceValidator struct {
	categories      ports.CategoryLookup
	media           ports.MediaLookup
	defaultCurrency string
}

func newReferenceValidator(categories ports.CategoryLookup, media ports.MediaLookup, defaultCurrency string) *referenceValidator {
	return &referenceValidator{categories: categories, media: media, defaultCurrency: defaultCurrency}
}

// requireCategory validates that a category exists.
func (v *referenceValidator) requireCategory(ctx context.Context, categoryID int64) error {
	if categoryID <= 0 {
		return product.NewValidationError("category_id", product.ErrInvalidInput)
	}
	exists, err := v.categories.Exists(ctx, []int64{categoryID})
	if err != nil {
		return err
	}
	if !exists[categoryID] {
		return product.NewValidationError("category_id", product.ErrCategoryNotFound)
	}
	return nil
}

// requireMedia validates that a media ID exists (if provided).
func (v *referenceValidator) requireMedia(ctx context.Context, mediaID *int64) error {
	if mediaID == nil || *mediaID <= 0 {
		return nil
	}
	return v.requireMediaBatch(ctx, []int64{*mediaID})
}

// requireMediaBatch validates multiple media IDs in one call.
func (v *referenceValidator) requireMediaBatch(ctx context.Context, mediaIDs []int64) error {
	if len(mediaIDs) == 0 {
		return nil
	}
	unique := make([]int64, 0, len(mediaIDs))
	seen := make(map[int64]bool, len(mediaIDs))
	for _, id := range mediaIDs {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		unique = append(unique, id)
	}
	if len(unique) == 0 {
		return nil
	}

	exists, err := v.media.Exists(ctx, unique)
	if err != nil {
		return err
	}
	for _, id := range unique {
		if !exists[id] {
			return product.NewValidationError("media_id", product.ErrMediaNotFound)
		}
	}
	return nil
}

// resolveCurrency uppercases and validates a currency code, falling back to default.
func (v *referenceValidator) resolveCurrency(raw string) (product.Currency, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return product.Currency(v.defaultCurrency), nil
	}
	currency, err := product.NewCurrency(raw)
	if err != nil {
		return "", product.NewValidationError("currency", err)
	}
	return currency, nil
}
