// Package repository provides PostgreSQL implementations of the repository ports.
package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"

	"product-service/internal/domain/product"
	"product-service/internal/ports"
)

const productColumns = `id, category_id, name, slug, description, brand,
	price_cents, currency, stock, status, primary_media_id, product_type_id,
	schema_version, attributes, created_at, updated_at, merchant_id`

const variantColumns = `id, product_id, sku, name, price_cents, currency, stock, media_id, created_at, updated_at`

// ProductPostgres implements the ProductRepository and VariantRepository interfaces.
type ProductPostgres struct {
	db *sql.DB
}

// NewProductPostgres creates a new PostgreSQL product repository.
func NewProductPostgres(db *sql.DB) *ProductPostgres {
	return &ProductPostgres{db: db}
}

// Compile-time interface checks
var _ ports.ProductRepository = (*ProductPostgres)(nil)
var _ ports.VariantRepository = (*ProductPostgres)(nil)
var _ ports.EventSink = (*ProductPostgres)(nil)

// BeginTx starts a new transaction.
func (r *ProductPostgres) BeginTx(ctx context.Context) (ports.Transaction, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	return &sqlTx{tx: tx}, nil
}

// sqlTx wraps sql.Tx to implement ports.Transaction.
type sqlTx struct {
	tx *sql.Tx
}

func (t *sqlTx) Commit() error   { return t.tx.Commit() }
func (t *sqlTx) Rollback() error { return t.tx.Rollback() }

// GetByID returns a product by ID without variants.
func (r *ProductPostgres) GetByID(ctx context.Context, id int64) (*product.Product, error) {
	const query = `SELECT ` + productColumns + ` FROM products WHERE id = $1`

	row := scannedProduct{}
	if err := r.db.QueryRowContext(ctx, query, id).Scan(row.scanDest()...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, product.NewValidationError("id", product.ErrProductNotFound)
		}
		return nil, fmt.Errorf("get product %d: %w", id, err)
	}
	return row.product(), nil
}

// GetByIDWithVariants returns a product by ID with its variants.
func (r *ProductPostgres) GetByIDWithVariants(ctx context.Context, id int64) (*product.Product, error) {
	p, err := r.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	variants, err := r.getVariantsByProductID(ctx, r.db, id)
	if err != nil {
		return nil, err
	}
	p.Variants = variants
	return p, nil
}

// ExistsByID checks if a product exists.
func (r *ProductPostgres) ExistsByID(ctx context.Context, id int64) (bool, error) {
	const query = `SELECT 1 FROM products WHERE id = $1`
	var exists int
	err := r.db.QueryRowContext(ctx, query, id).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check product existence %d: %w", id, err)
	}
	return true, nil
}

// CreateProduct inserts a new product with its variants.
func (r *ProductPostgres) CreateProduct(ctx context.Context, p *product.Product, variants []*product.Variant) (*product.Product, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin create product: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	const insertProduct = `
		INSERT INTO products (
			category_id, name, slug, description, brand,
			price_cents, currency, stock, status, primary_media_id,
			product_type_id, schema_version, attributes, merchant_id
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		RETURNING ` + productColumns

	row := scannedProduct{}
	err = tx.QueryRowContext(ctx, insertProduct,
		p.CategoryID,
		p.Name,
		p.Slug,
		p.Description,
		p.Brand,
		p.Price.Amount,
		string(p.Price.Currency),
		p.Stock,
		string(p.Status),
		p.PrimaryMediaID,
		p.ProductTypeID,
		p.SchemaVersion,
		marshalAttributes(p.Attributes),
		p.MerchantID,
	).Scan(row.scanDest()...)
	if err != nil {
		return nil, translateWriteError("create product", err, p.Slug)
	}

	created := row.product()
	if len(variants) > 0 {
		stored, err := insertVariants(ctx, tx, created.ID, variants)
		if err != nil {
			return nil, err
		}
		created.Variants = stored
	}

	if err := tx.Commit(); err != nil {
		return nil, translateWriteError("create product", err, p.Slug)
	}
	return created, nil
}

// UpdateProduct modifies an existing product.
func (r *ProductPostgres) UpdateProduct(ctx context.Context, p *product.Product) (*product.Product, error) {
	var (
		clauses []string
		args    []any
	)
	add := func(clause string, value any) {
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf("%s = $%d", clause, len(args)))
	}

	// Only update fields that are non-zero/non-empty
	if p.CategoryID != 0 {
		add("category_id", p.CategoryID)
	}
	if p.Name != "" {
		add("name", p.Name)
	}
	if p.Slug != "" {
		add("slug", p.Slug)
	}
	if p.Description != nil {
		add("description", p.Description)
	}
	if p.Brand != nil {
		add("brand", p.Brand)
	}
	if p.Price.Amount != 0 {
		add("price_cents", p.Price.Amount)
	}
	if p.Price.Currency != "" {
		add("currency", string(p.Price.Currency))
	}
	if p.Stock != 0 {
		add("stock", p.Stock)
	}
	if p.Status != "" {
		add("status", string(p.Status))
	}
	if p.PrimaryMediaID != nil {
		add("primary_media_id", p.PrimaryMediaID)
	}
	if p.ProductTypeID != "" {
		add("product_type_id", p.ProductTypeID)
	}
	if p.SchemaVersion != 0 {
		add("schema_version", p.SchemaVersion)
	}
	if p.Attributes != nil {
		add("attributes", marshalAttributes(p.Attributes))
	}

	if len(clauses) == 0 {
		return r.GetByID(ctx, p.ID)
	}

	args = append(args, p.ID)
	query := fmt.Sprintf(
		`UPDATE products SET %s WHERE id = $%d RETURNING %s`,
		strings.Join(clauses, ", "), len(args), productColumns,
	)

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin update product %d: %w", p.ID, err)
	}
	defer func() { _ = tx.Rollback() }()

	row := scannedProduct{}
	err = tx.QueryRowContext(ctx, query, args...).Scan(row.scanDest()...)
	if err != nil {
		return nil, translateWriteError(fmt.Sprintf("update product %d", p.ID), err, "")
	}

	updated := row.product()

	if err := tx.Commit(); err != nil {
		return nil, translateWriteError(fmt.Sprintf("update product %d", p.ID), err, "")
	}
	return updated, nil
}

// DeleteProduct removes a product and its variants (via cascade).
func (r *ProductPostgres) DeleteProduct(ctx context.Context, id int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin delete product %d: %w", id, err)
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(ctx, `DELETE FROM products WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete product %d: %w", id, err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete product %d: rows affected: %w", id, err)
	}
	if rowsAffected == 0 {
		return product.NewValidationError("id", product.ErrProductNotFound)
	}

	return tx.Commit()
}

// AdjustStock atomically adjusts product stock.
func (r *ProductPostgres) AdjustStock(ctx context.Context, id int64, delta int64) (*product.Product, error) {
	const query = `
		UPDATE products SET stock = stock + $2
		WHERE id = $1 AND stock + $2 >= 0
		RETURNING ` + productColumns

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin adjust stock of product %d: %w", id, err)
	}
	defer func() { _ = tx.Rollback() }()

	row := scannedProduct{}
	err = tx.QueryRowContext(ctx, query, id, delta).Scan(row.scanDest()...)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// Check if product exists
		if _, getErr := getByIDTx(ctx, tx, id); getErr != nil {
			return nil, getErr
		}
		return nil, product.NewValidationError("stock", product.ErrNegativeStock)
	case err != nil:
		return nil, fmt.Errorf("adjust stock of product %d: %w", id, err)
	}

	adjusted := row.product()

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("adjust stock of product %d: %w", id, err)
	}
	return adjusted, nil
}

// GetVariantByID returns a variant by ID.
func (r *ProductPostgres) GetVariantByID(ctx context.Context, id int64) (*product.Variant, error) {
	const query = `SELECT ` + variantColumns + ` FROM product_variants WHERE id = $1`

	row := scannedVariant{}
	if err := r.db.QueryRowContext(ctx, query, id).Scan(row.scanDest()...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, product.NewValidationError("id", product.ErrVariantNotFound)
		}
		return nil, fmt.Errorf("get variant %d: %w", id, err)
	}
	return row.variant(), nil
}

// CountByProductID returns the number of variants for a product.
func (r *ProductPostgres) CountByProductID(ctx context.Context, productID int64) (int, error) {
	const query = `SELECT COUNT(*) FROM product_variants WHERE product_id = $1`

	var count int
	if err := r.db.QueryRowContext(ctx, query, productID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count variants of product %d: %w", productID, err)
	}
	return count, nil
}

// CreateVariant inserts a new variant for a product.
func (r *ProductPostgres) CreateVariant(ctx context.Context, v *product.Variant) (*product.Variant, error) {
	const insertVariant = `
		INSERT INTO product_variants (product_id, sku, name, price_cents, currency, stock, media_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING ` + variantColumns

	row := scannedVariant{}
	err := r.db.QueryRowContext(ctx, insertVariant,
		v.ProductID,
		v.SKU,
		v.Name,
		v.Price.Amount,
		string(v.Price.Currency),
		v.Stock,
		v.MediaID,
	).Scan(row.scanDest()...)
	if err != nil {
		return nil, translateWriteError("create variant", err, v.SKU)
	}
	return row.variant(), nil
}

// UpdateVariant modifies an existing variant.
func (r *ProductPostgres) UpdateVariant(ctx context.Context, v *product.Variant) (*product.Variant, error) {
	const query = `
		UPDATE product_variants
		SET sku = $2, name = $3, price_cents = $4, currency = $5, stock = $6, media_id = $7
		WHERE id = $1
		RETURNING ` + variantColumns

	row := scannedVariant{}
	err := r.db.QueryRowContext(ctx, query,
		v.ID,
		v.SKU,
		v.Name,
		v.Price.Amount,
		string(v.Price.Currency),
		v.Stock,
		v.MediaID,
	).Scan(row.scanDest()...)
	if err != nil {
		return nil, translateWriteError("update variant", err, v.SKU)
	}
	return row.variant(), nil
}

// DeleteVariant removes a variant.
func (r *ProductPostgres) DeleteVariant(ctx context.Context, productID, variantID int64) error {
	result, err := r.db.ExecContext(ctx, `DELETE FROM product_variants WHERE id = $1 AND product_id = $2`, variantID, productID)
	if err != nil {
		return fmt.Errorf("delete variant %d: %w", variantID, err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete variant %d: rows affected: %w", variantID, err)
	}
	if rowsAffected == 0 {
		return product.NewValidationError("id", product.ErrVariantNotFound)
	}
	return nil
}

// Append writes events to the outbox inside a transaction.
func (r *ProductPostgres) Append(ctx context.Context, tx ports.Transaction, events []*product.DomainEvent) ([]*product.DomainEvent, error) {
	if len(events) == 0 {
		return nil, nil
	}

	sqlTx, ok := tx.(*sqlTx)
	if !ok {
		return nil, fmt.Errorf("invalid transaction type")
	}

	const insert = `
		INSERT INTO outbox_events (
			event_id, topic, event_type, aggregate_type, aggregate_id,
			partition_key, payload
		)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6)
		RETURNING event_id::text, occurred_at`

	stmt, err := sqlTx.tx.PrepareContext(ctx, insert)
	if err != nil {
		return nil, fmt.Errorf("prepare outbox insert: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	appended := make([]*product.DomainEvent, 0, len(events))
	for _, event := range events {
		var eventID string
		var occurredAt time.Time
		err := stmt.QueryRowContext(ctx,
			event.Topic, event.EventType, event.AggregateType,
			event.AggregateID, event.PartitionKey, event.Payload,
		).Scan(&eventID, &occurredAt)
		if err != nil {
			return nil, fmt.Errorf("insert outbox event %s: %w", event.EventType, err)
		}
		event.EventID = eventID
		event.OccurredAt = occurredAt
		appended = append(appended, event)
	}
	return appended, nil
}

// scannedProduct holds a row using sql null types for scanning.
type scannedProduct struct {
	id             int64
	categoryID     int64
	name           string
	slug           string
	description    sql.NullString
	brand          sql.NullString
	priceCents     int64
	currency       string
	stock          int64
	status         string
	primaryMediaID sql.NullInt64
	productTypeID  string
	schemaVersion  int32
	attributes     []byte
	createdAt      time.Time
	updatedAt      time.Time
	merchantID     string
}

func (s scannedProduct) product() *product.Product {
	p := &product.Product{
		ID:            s.id,
		ProductTypeID: s.productTypeID,
		SchemaVersion: s.schemaVersion,
		Attributes:    map[string]string{},
		CategoryID:    s.categoryID,
		Name:          s.name,
		Slug:          s.slug,
		Price: product.Money{
			Amount:   s.priceCents,
			Currency: product.Currency(strings.TrimSpace(s.currency)),
		},
		Stock:      s.stock,
		Status:     product.Status(s.status),
		CreatedAt:  s.createdAt,
		UpdatedAt:  s.updatedAt,
		MerchantID: s.merchantID,
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

func (s *scannedProduct) scanDest() []any {
	return []any{
		&s.id,
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
		&s.merchantID,
	}
}

func marshalAttributes(values map[string]string) string {
	data, err := json.Marshal(values)
	if err != nil {
		return "{}"
	}
	return string(data)
}

// scannedVariant holds a variant row for scanning.
type scannedVariant struct {
	id         int64
	productID  int64
	sku        string
	name       string
	priceCents int64
	currency   string
	stock      int64
	mediaID    sql.NullInt64
	createdAt  time.Time
	updatedAt  time.Time
}

func (s scannedVariant) variant() *product.Variant {
	v := &product.Variant{
		ID:        s.id,
		ProductID: s.productID,
		SKU:       s.sku,
		Name:      s.name,
		Price: product.Money{
			Amount:   s.priceCents,
			Currency: product.Currency(strings.TrimSpace(s.currency)),
		},
		Stock:     s.stock,
		CreatedAt: s.createdAt,
		UpdatedAt: s.updatedAt,
	}
	if s.mediaID.Valid {
		mediaID := s.mediaID.Int64
		v.MediaID = &mediaID
	}
	return v
}

func (s *scannedVariant) scanDest() []any {
	return []any{
		&s.id,
		&s.productID,
		&s.sku,
		&s.name,
		&s.priceCents,
		&s.currency,
		&s.stock,
		&s.mediaID,
		&s.createdAt,
		&s.updatedAt,
	}
}

// insertVariants adds variants in a transaction.
func insertVariants(ctx context.Context, tx *sql.Tx, productID int64, variants []*product.Variant) ([]*product.Variant, error) {
	const insertVariant = `
		INSERT INTO product_variants (product_id, sku, name, price_cents, currency, stock, media_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING ` + variantColumns

	stored := make([]*product.Variant, 0, len(variants))
	for _, v := range variants {
		row := scannedVariant{}
		err := tx.QueryRowContext(ctx, insertVariant,
			productID,
			v.SKU,
			v.Name,
			v.Price.Amount,
			string(v.Price.Currency),
			v.Stock,
			v.MediaID,
		).Scan(row.scanDest()...)
		if err != nil {
			return nil, translateWriteError("create variant", err, v.SKU)
		}
		stored = append(stored, row.variant())
	}
	return stored, nil
}

// getVariantsByProductID returns all variants for a product.
func (r *ProductPostgres) getVariantsByProductID(ctx context.Context, querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}, productID int64) ([]*product.Variant, error) {
	const query = `SELECT ` + variantColumns + ` FROM product_variants WHERE product_id = $1`

	rows, err := querier.QueryContext(ctx, query, productID)
	if err != nil {
		return nil, fmt.Errorf("get variants for product %d: %w", productID, err)
	}
	defer func() { _ = rows.Close() }()

	var variants []*product.Variant
	for rows.Next() {
		row := scannedVariant{}
		if err := rows.Scan(row.scanDest()...); err != nil {
			return nil, fmt.Errorf("scan variant: %w", err)
		}
		variants = append(variants, row.variant())
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate variants: %w", err)
	}
	return variants, nil
}

// getByIDTx reads a product inside a transaction.
func getByIDTx(ctx context.Context, tx *sql.Tx, id int64) (*product.Product, error) {
	const query = `SELECT ` + productColumns + ` FROM products WHERE id = $1`

	row := scannedProduct{}
	if err := tx.QueryRowContext(ctx, query, id).Scan(row.scanDest()...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, product.NewValidationError("id", product.ErrProductNotFound)
		}
		return nil, fmt.Errorf("get product %d: %w", id, err)
	}
	return row.product(), nil
}

// translateWriteError maps unique constraint violations to domain errors.
func translateWriteError(op string, err error, conflicting string) error {
	var pqErr *pq.Error
	switch {
	case errors.As(err, &pqErr) && pqErr.Code == "23505":
		return product.NewValidationError(conflicting, product.ErrConflict)
	case errors.Is(err, sql.ErrNoRows):
		return product.NewValidationError("id", product.ErrNotFound)
	case err != nil:
		return fmt.Errorf("%s: %w", op, err)
	default:
		return nil
	}
}
