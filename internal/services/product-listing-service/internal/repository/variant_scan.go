package repository

import (
	"database/sql"
	"time"

	"github.com/ruangbercerita182-tech/product-listing-service/internal/model"
)

const variantColumns = `variant_id, product_id, sku, name, price_cents, currency,
	stock, media_id, created_at, updated_at, projected_at`

// scannedVariant holds a variant row using the sql null types for the nullable
// media_id.
type scannedVariant struct {
	variantID   int64
	productID   int64
	sku         string
	name        string
	priceCents  int64
	currency    string
	stock       int
	mediaID     sql.NullInt64
	createdAt   time.Time
	updatedAt   time.Time
	projectedAt time.Time
}

func (s scannedVariant) variant() *model.Variant {
	v := &model.Variant{
		ID:          s.variantID,
		ProductID:   s.productID,
		SKU:         s.sku,
		Name:        s.name,
		PriceCents:  s.priceCents,
		Currency:    s.currency,
		Stock:       s.stock,
		CreatedAt:   s.createdAt,
		UpdatedAt:   s.updatedAt,
		ProjectedAt: s.projectedAt,
	}
	if s.mediaID.Valid {
		mediaID := s.mediaID.Int64
		v.MediaID = &mediaID
	}
	return v
}

// scanDest returns the scan targets for a row. The receiver must be a pointer,
// or the driver would write into this method's copy of the row. See the note on
// scannedListing.scanDest.
func (s *scannedVariant) scanDest() []any {
	return []any{
		&s.variantID,
		&s.productID,
		&s.sku,
		&s.name,
		&s.priceCents,
		&s.currency,
		&s.stock,
		&s.mediaID,
		&s.createdAt,
		&s.updatedAt,
		&s.projectedAt,
	}
}
