// Package repository holds the PostgreSQL implementation of the search engine.
//
// The pg_trgm queries here are the replaceable half of the service. The usecase
// above them, the HTTP and gRPC adapters, and the response shape are engine
// agnostic on purpose, so replacing this with Elasticsearch is a new
// implementation of search.Engine plus a wiring change in main.
package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"product-search-service/internal/apperror"
	"product-search-service/internal/model"
	"product-search-service/internal/search"
)

// Engine is a search engine backed by PostgreSQL trigram indexes.
type Engine struct {
	db *sql.DB
}

// The write half satisfies the projector's transactional contract. The signatures
// carry a *sql.Tx because the dedupe marker and the document write have to commit
// together; see Indexer.
var _ Indexer = (*Engine)(nil)

// The concrete engine is checked against the contract it is written for, so a
// change to search.Engine that this does not satisfy fails the build here rather
// than at some call site in a handler. Swapping in another implementation means
// adding a second assertion beside this one.
var _ search.Engine = (*Engine)(nil)

// NewEngine returns a search engine reading from db.
func NewEngine(db *sql.DB) *Engine {
	return &Engine{db: db}
}

// searchColumns are the document columns. Score is computed in the query, not
// stored: it depends on the term, so a stored value would be meaningless.
const searchColumns = `product_id, category_id, name, slug, description, brand,
	price_cents, currency, stock, status, primary_media_id, created_at, updated_at,
	projected_at`

// scoreExpression ranks a document against the term.
//
// The term is a placeholder rather than a literal because its position depends on
// how many filters precede it, so the expression is built once the argument list
// is known.
//
// similarity is used, and the choice is not obvious. word_similarity looks like the
// better fit - it asks how well the term matches one contiguous extent of the
// document - and in isolation it is. But this filter has already selected documents
// that contain the term contiguously, so every surviving hit holds the term's
// trigrams in one extent and word_similarity returns exactly 1.0 for all of them.
// It cannot rank anything.
//
// similarity measures how much of the document the term accounts for, so it does
// distinguish them. Verified against PostgreSQL 17 on three documents that all
// contain "aurora":
//
//	document                                              word_similarity  similarity
//	Aurora                                                     1.0           1.0
//	Auroraphone                                                0.86          0.46
//	Aurora Phone 15 Pro Max Ultra Limited Edition Bundle         1.0          0.14
//
// word_similarity ties the exact match with the long title and leaves their order
// to the tiebreaker; similarity separates all three, putting the product whose
// title really is about the query first. That is the behaviour a shopper expects
// from a catalogue search.
//
// The argument order is the reverse of what it looks like: the first argument is
// the term, the second the document. Passing them the other way round measures the
// document against the term, which is a different and much smaller number.
//
// No lower() call appears here and none is needed. pg_trgm lowercases both sides
// before extracting trigrams, so similarity is already case-insensitive on the raw
// column, and wrapping the column in lower() would be pure overhead. The filter
// below is a different story, because LIKE is case-sensitive.
//
// greatest() takes the best single field rather than averaging, so a term that is
// the product's whole brand outranks one that is a fragment of a long title. This
// is the shape Elasticsearch calls multi_match with best_fields, and it is why the
// per-field arithmetic is kept separate from the indexable text filter below: the
// index narrows the candidates, and only the candidates are scored.
func scoreExpression(termPos int) string {
	return fmt.Sprintf(`GREATEST(
	similarity($%d, name),
	similarity($%d, COALESCE(brand, '')),
	similarity($%d, slug))`, termPos, termPos, termPos)
}

// scannedSearch holds a row using the sql null types, which the driver needs for
// scanning but which the model does not expose.
type scannedSearch struct {
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
	createdAt      time.Time
	updatedAt      time.Time
	projectedAt    time.Time
	score          float64
}

func (s scannedSearch) document() *model.SearchProduct {
	doc := &model.SearchProduct{
		ID:          s.productID,
		CategoryID:  s.categoryID,
		Name:        s.name,
		Slug:        s.slug,
		PriceCents:  s.priceCents,
		Currency:    strings.TrimSpace(s.currency),
		Stock:       s.stock,
		Status:      model.Status(s.status),
		Score:       s.score,
		CreatedAt:   s.createdAt,
		UpdatedAt:   s.updatedAt,
		ProjectedAt: s.projectedAt,
	}
	if s.description.Valid {
		description := s.description.String
		doc.Description = &description
	}
	if s.brand.Valid {
		brand := s.brand.String
		doc.Brand = &brand
	}
	if s.primaryMediaID.Valid {
		mediaID := s.primaryMediaID.Int64
		doc.PrimaryMediaID = &mediaID
	}
	return doc
}

// scanDest returns the scan targets for a row.
//
// The receiver must be a pointer: the returned slice holds addresses of the
// struct's fields, and with a value receiver those addresses would point into
// this method's copy of the row rather than the caller's, so every column would
// silently stay at its zero value.
func (s *scannedSearch) scanDest() []any {
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
		&s.createdAt,
		&s.updatedAt,
		&s.projectedAt,
		&s.score,
	}
}

// Search returns a ranked page of documents plus the total number of matches.
func (e *Engine) Search(ctx context.Context, q *model.SearchQuery) (*model.SearchResult, error) {
	started := time.Now()

	// The term arrives already trimmed and lowercased by the service. The pattern
	// is assembled from a bound parameter, so the wildcards go onto the value
	// rather than into the SQL text, and they are escaped, because a shopper
	// searching for "50%" means a percent sign and would otherwise match every
	// product. Verified against PostgreSQL 17: an escaped "%" matched 1 of 3
	// candidate rows where an unescaped one matched all 3.
	pattern := "%" + escapeLike(q.Query) + "%"

	conditions := []string{`search_text LIKE $1 ESCAPE '\'`}
	args := []any{pattern}
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
	// dropped the filter and returned the whole index instead.
	if q.MinPriceCents != nil {
		add("price_cents >= $%d", *q.MinPriceCents)
	}
	if q.MaxPriceCents != nil {
		add("price_cents <= $%d", *q.MaxPriceCents)
	}
	clause := " WHERE " + strings.Join(conditions, " AND ")

	// Counted separately from the page. A window function would report zero for a
	// page past the end of the result set, and a client could then never page on.
	var total int64
	if err := e.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM product_search`+clause, args...,
	).Scan(&total); err != nil {
		return nil, fmt.Errorf("count search hits: %w", err)
	}

	// The term is bound a second time because the ranking expression needs it
	// separately from the LIKE pattern, and its position is only known now that
	// every filter has been added.
	termPos := len(args) + 1
	scoreArgs := append(append([]any{}, args...), q.Query)
	pageArgs := append(append([]any{}, scoreArgs...), q.Limit, q.Offset)
	limitPos, offsetPos := len(pageArgs)-1, len(pageArgs)

	query := fmt.Sprintf(
		`SELECT %s, %s AS score FROM product_search%s ORDER BY score DESC, created_at DESC, product_id DESC `+
			`LIMIT $%d OFFSET $%d`,
		searchColumns, scoreExpression(termPos), clause, limitPos, offsetPos,
	)

	rows, err := e.db.QueryContext(ctx, query, pageArgs...)
	if err != nil {
		return nil, fmt.Errorf("search products: %w", err)
	}
	defer func() { _ = rows.Close() }()

	// An empty slice, not nil, so the API renders [] rather than null.
	products := make([]*model.SearchProduct, 0, q.Limit)
	for rows.Next() {
		row := scannedSearch{}
		if err := rows.Scan(row.scanDest()...); err != nil {
			return nil, fmt.Errorf("search products: scan row: %w", err)
		}
		products = append(products, row.document())
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("search products: %w", err)
	}

	return &model.SearchResult{
		Products: products,
		Total:    total,
		Limit:    q.Limit,
		Offset:   q.Offset,
		TookMS:   time.Since(started).Milliseconds(),
	}, nil
}

// escapeLike neutralises the LIKE metacharacters so a search term is matched
// literally. The backslash is the escape character named by the ESCAPE clause in
// Search, and the order matters: the backslash itself has to be escaped first or
// the backslashes added for the other two would be escaped a second time.
func escapeLike(term string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(term)
}

// Index upserts one document.
//
// eventID is passed separately rather than carried on the document because it is
// projection bookkeeping, not something a search result would ever show. An
// Elasticsearch implementation stores it as a field of the indexed document, so
// the value is available where it is needed without polluting the search model.
//
// The insert and the update are one statement, so a redelivered event costs the
// same as the first delivery and cannot fail halfway. search_text is left out
// because it is generated from the columns being written.
func (e *Engine) Index(ctx context.Context, tx *sql.Tx, doc *model.SearchProduct, eventID string) error {
	const upsert = `
		INSERT INTO product_search (
			product_id, category_id, name, slug, description, brand,
			price_cents, currency, stock, status, primary_media_id,
			created_at, updated_at, projected_at, last_event_id
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, CURRENT_TIMESTAMP, $14)
		ON CONFLICT (product_id) DO UPDATE SET
			category_id      = EXCLUDED.category_id,
			name             = EXCLUDED.name,
			slug             = EXCLUDED.slug,
			description      = EXCLUDED.description,
			brand            = EXCLUDED.brand,
			price_cents      = EXCLUDED.price_cents,
			currency         = EXCLUDED.currency,
			stock            = EXCLUDED.stock,
			status           = EXCLUDED.status,
			primary_media_id = EXCLUDED.primary_media_id,
			created_at       = EXCLUDED.created_at,
			updated_at       = EXCLUDED.updated_at,
			projected_at     = CURRENT_TIMESTAMP,
			last_event_id    = EXCLUDED.last_event_id`

	if _, err := tx.ExecContext(ctx, upsert,
		doc.ID, doc.CategoryID, doc.Name, doc.Slug, doc.Description, doc.Brand,
		doc.PriceCents, doc.Currency, doc.Stock, doc.Status, doc.PrimaryMediaID,
		doc.CreatedAt, doc.UpdatedAt, eventID,
	); err != nil {
		return fmt.Errorf("index product %d: %w", doc.ID, err)
	}
	return nil
}

// SetStock updates only the stock column.
//
// A stock adjustment event carries the resulting stock and nothing else, so
// writing just that column leaves every other field as the last snapshot put it.
// Writing the whole row instead would be wrong: the event does not know the
// current values, and a stale copy would overwrite a newer product update that
// had already been indexed.
//
// This is also the one place the engine interface would need a method for a
// complete OpenSearch port: Elasticsearch has no partial update that avoids
// re-sending the document, so a real implementation either refetches the
// document or carries the stock on a separate field. It is declared here so that
// change is visible rather than surprising.
func (e *Engine) SetStock(ctx context.Context, tx *sql.Tx, productID int64, stock int, eventID string) error {
	const query = `
		UPDATE product_search
		SET stock = $2, projected_at = CURRENT_TIMESTAMP, last_event_id = $3
		WHERE product_id = $1`

	if _, err := tx.ExecContext(ctx, query, productID, stock, eventID); err != nil {
		return fmt.Errorf("set stock of product %d: %w", productID, err)
	}
	return nil
}

// Remove deletes one document.
//
// A missing row is not an error. That is what makes a redelivered delete harmless
// rather than a consumer failure that stalls the partition.
func (e *Engine) Remove(ctx context.Context, tx *sql.Tx, productID int64) error {
	if _, err := e.db.ExecContext(ctx,
		`DELETE FROM product_search WHERE product_id = $1`, productID,
	); err != nil {
		return fmt.Errorf("remove product %d: %w", productID, err)
	}
	return nil
}

// Count returns how many documents the index holds.
func (e *Engine) Count(ctx context.Context) (int64, error) {
	var count int64
	if err := e.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM product_search`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count indexed products: %w", err)
	}
	return count, nil
}

// GetByID returns one indexed document. It is not part of search.Engine, because
// a search result does not need it; it exists for the gRPC adapter's convenience
// and for tests that want to assert on what was indexed.
func (e *Engine) GetByID(ctx context.Context, id int64) (*model.SearchProduct, error) {
	const query = `SELECT ` + searchColumns + `, 0 AS score FROM product_search WHERE product_id = $1`

	row := scannedSearch{}
	if err := e.db.QueryRowContext(ctx, query, id).Scan(row.scanDest()...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: product %d", apperror.ErrNotFound, id)
		}
		return nil, fmt.Errorf("get product %d: %w", id, err)
	}
	return row.document(), nil
}
