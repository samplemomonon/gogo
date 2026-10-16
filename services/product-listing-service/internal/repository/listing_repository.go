// Package repository stores the product projection in PostgreSQL.
//
// Every method here reads a table this service owns outright. Nothing in this
// package knows how the rows got there, which is what keeps the read path
// independent of the write service: it could equally well be filled by replaying
// the topic, by a backfill job, or by hand during an incident.
package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ruangbercerita182-tech/product-listing-service/internal/apperror"
	"github.com/ruangbercerita182-tech/product-listing-service/internal/model"
)

// ListingRepository reads the product read model.
type ListingRepository struct {
	db *sql.DB
}

// NewListingRepository returns a repository backed by db.
func NewListingRepository(db *sql.DB) *ListingRepository {
	return &ListingRepository{db: db}
}

const listingColumns = `product_id, category_id, name, slug, description, brand,
	price_cents, currency, stock, status, primary_media_id, product_type_id, schema_version, attributes, created_at, updated_at,
	projected_at`

// scannedListing holds a row using the sql null types, which the driver needs for
// scanning but which the model does not expose.
type scannedListing struct {
	productID      int64
	categoryID     int64
	name           string
	slug           string
	description    sql.NullString
	brand          sql.NullString
	priceCents     int64
	currency       string
	stock          int
	status         string
	primaryMediaID sql.NullInt64
	productTypeID  string
	schemaVersion  int32
	attributes     []byte
	createdAt      time.Time
	updatedAt      time.Time
	projectedAt    time.Time
}

func (s scannedListing) product() *model.Product {
	p := &model.Product{
		ID:            s.productID,
		ProductTypeID: s.productTypeID,
		SchemaVersion: s.schemaVersion,
		Attributes:    map[string]string{},
		CategoryID:    s.categoryID,
		Name:          s.name,
		Slug:          s.slug,
		PriceCents:    s.priceCents,
		Currency:      strings.TrimSpace(s.currency),
		Stock:         s.stock,
		Status:        model.Status(s.status),
		CreatedAt:     s.createdAt,
		UpdatedAt:     s.updatedAt,
		ProjectedAt:   s.projectedAt,
	}
	_ = json.Unmarshal(s.attributes, &p.Attributes)
	if s.description.Valid {
		description := s.description.String
		p.Description = &description
	}
	if s.brand.Valid {
		brand := s.brand.String
		p.Brand = &brand
	}
	if s.primaryMediaID.Valid {
		mediaID := s.primaryMediaID.Int64
		p.PrimaryMediaID = &mediaID
	}
	return p
}

// scanDest returns the scan targets for a row.
//
// The receiver must be a pointer: the returned slice holds addresses of the
// struct's fields, and with a value receiver those addresses would point into
// this method's copy of the row rather than the caller's, so every column would
// silently stay at its zero value.
func (s *scannedListing) scanDest() []any {
	return []any{
		&s.productID,
		&s.categoryID,
		&s.name,
		&s.slug,
		&s.description,
		&s.brand,
		&s.priceCents,
		&s.currency,
		&s.stock,
		&s.status,
		&s.primaryMediaID,
		&s.productTypeID,
		&s.schemaVersion,
		&s.attributes,
		&s.createdAt,
		&s.updatedAt,
		&s.projectedAt,
	}
}

// GetByID returns a single product from the read model without its variants.
func (r *ListingRepository) GetByID(ctx context.Context, id int64) (*model.Product, error) {
	const query = `SELECT ` + listingColumns + ` FROM product_listings WHERE product_id = $1`

	row := scannedListing{}
	if err := r.db.QueryRowContext(ctx, query, id).Scan(row.scanDest()...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// A miss here does not necessarily mean the product does not exist: the
			// event that would have created it may simply not have been consumed
			// yet. The distinction matters to a caller, so it is logged by the
			// caller rather than hidden here.
			return nil, fmt.Errorf("%w: product %d", apperror.ErrNotFound, id)
		}
		return nil, fmt.Errorf("get product %d: %w", id, err)
	}

	product := row.product()
	// Always a non-nil slice so the JSON is [] rather than null.
	product.Variants = []*model.Variant{}
	return product, nil
}

// List returns a page of products plus the total number of matches. Variants are
// not included unless asked for, so a listing does not grow with the number of
// variants per product.
func (r *ListingRepository) List(
	ctx context.Context,
	q *model.ListProductsQuery,
) (*model.ListProductsResult, error) {
	conditions, args := buildFilters(q)
	clause := ""
	if len(conditions) > 0 {
		clause = " WHERE " + strings.Join(conditions, " AND ")
	}

	// Counted separately because a window function would report zero for a page
	// that is past the end of the result set.
	var total int64
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM product_listings`+clause, args...,
	).Scan(&total); err != nil {
		return nil, fmt.Errorf("count products: %w", err)
	}

	args = append(args, q.Limit, q.Offset)
	listQuery := fmt.Sprintf(
		`SELECT %s FROM product_listings%s ORDER BY created_at DESC, product_id DESC LIMIT $%d OFFSET $%d`,
		listingColumns, clause, len(args)-1, len(args),
	)

	rows, err := r.db.QueryContext(ctx, listQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("list products: %w", err)
	}
	defer func() { _ = rows.Close() }()

	// An empty slice, not nil, so the API renders [] rather than null.
	products := make([]*model.Product, 0, q.Limit)
	for rows.Next() {
		row := scannedListing{}
		if err := rows.Scan(row.scanDest()...); err != nil {
			return nil, fmt.Errorf("list products: scan row: %w", err)
		}
		product := row.product()
		product.Variants = []*model.Variant{}
		products = append(products, product)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list products: %w", err)
	}

	result := &model.ListProductsResult{Products: products, Total: total}
	if q.IncludeVariants && len(products) > 0 {
		if err := r.attachVariants(ctx, products); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// buildFilters turns the query into WHERE conditions with bound placeholders, so
// every value travels as a parameter and the SQL shape never varies with input.
func buildFilters(q *model.ListProductsQuery) ([]string, []any) {
	var (
		conditions []string
		args       []any
	)
	add := func(condition string, value any) {
		args = append(args, value)
		conditions = append(conditions, fmt.Sprintf(condition, len(args)))
	}

	if q.CategoryID > 0 {
		add("category_id = $%d", q.CategoryID)
	}
	if q.Status != nil {
		add("status = $%d", string(*q.Status))
	}
	// The price bounds are tested for presence, not for being non-zero. A caller
	// asking for products up to IDR 0 means exactly that, and comparing against 0
	// dropped the filter and returned the whole catalogue instead.
	if q.MinPriceCents != nil {
		add("price_cents >= $%d", *q.MinPriceCents)
	}
	if q.MaxPriceCents != nil {
		add("price_cents <= $%d", *q.MaxPriceCents)
	}
	return conditions, args
}

// attachVariants loads the variants of every product in one query, so a listing
// does not fan out into a query per product.
func (r *ListingRepository) attachVariants(ctx context.Context, products []*model.Product) error {
	ids := make([]any, 0, len(products))
	byID := make(map[int64]*model.Product, len(products))
	for _, p := range products {
		ids = append(ids, p.ID)
		byID[p.ID] = p
	}

	placeholders := make([]string, len(ids))
	for i := range ids {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
	}
	query := fmt.Sprintf(
		`SELECT %s FROM product_listing_variants WHERE product_id IN (%s) ORDER BY variant_id ASC`,
		variantColumns, strings.Join(placeholders, ", "),
	)

	rows, err := r.db.QueryContext(ctx, query, ids...)
	if err != nil {
		return fmt.Errorf("list variants: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		row := scannedVariant{}
		if err := rows.Scan(row.scanDest()...); err != nil {
			return fmt.Errorf("list variants: scan row: %w", err)
		}
		if product, ok := byID[row.productID]; ok {
			product.Variants = append(product.Variants, row.variant())
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("list variants: %w", err)
	}
	return nil
}

// ListVariants returns the variants of a product ordered by id.
func (r *ListingRepository) ListVariants(ctx context.Context, productID int64) ([]*model.Variant, error) {
	const query = `
		SELECT ` + variantColumns + `
		FROM product_listing_variants WHERE product_id = $1 ORDER BY variant_id ASC`

	rows, err := r.db.QueryContext(ctx, query, productID)
	if err != nil {
		return nil, fmt.Errorf("list variants of product %d: %w", productID, err)
	}
	defer func() { _ = rows.Close() }()

	variants := make([]*model.Variant, 0)
	for rows.Next() {
		row := scannedVariant{}
		if err := rows.Scan(row.scanDest()...); err != nil {
			return nil, fmt.Errorf("list variants of product %d: scan row: %w", productID, err)
		}
		variants = append(variants, row.variant())
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list variants of product %d: %w", productID, err)
	}
	return variants, nil
}

// Count returns how many products the read model holds, reported by the status
// endpoint so an empty projection is distinguishable from a broken one.
func (r *ListingRepository) Count(ctx context.Context) (int64, error) {
	var count int64
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM product_listings`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count products: %w", err)
	}
	return count, nil
}
