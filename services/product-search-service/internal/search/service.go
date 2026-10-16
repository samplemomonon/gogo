// Package search holds the search use case and the interface its storage
// engine must satisfy.
//
// The split from product-listing-service is the reason this package exists.
// Browsing answers "what is in category 3, newest first", which is a bounded
// indexed scan over structured columns. Searching answers "what matches this
// text", which needs a trigram index, a relevance score, and an engine that can
// be replaced. One service holding both would mean the browse table carries
// indexes it never uses and the search code cannot change on its own schedule.
//
// Engine is the seam that makes the replacement possible. Everything above this
// interface - validation, the response envelope, the HTTP and gRPC adapters -
// is engine-agnostic on purpose, so moving to Elasticsearch or OpenSearch is a
// new implementation here and a wiring change in main, not a rewrite.
package search

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"product-search-service/internal/apperror"
	"product-search-service/internal/model"
)

const (
	// maxLimit caps a single page so one request cannot ask for the whole index.
	maxLimit = 100
	// defaultLimit applies when the caller does not specify one.
	defaultLimit = 20
	// maxTermLength bounds the search term. A trigram index extracts roughly one
	// trigram per character, so an unbounded term is unbounded work in the index
	// and a cheap way to make the service busy.
	maxTermLength = 256
)

// Engine is the search backend.
//
// The two halves are deliberately asymmetric. Search is on the request path and
// has to be fast. Index and Remove are on the consumer path, where correctness
// matters more than latency and which have to be idempotent, because
// at-least-once delivery means they will be called twice for one event.
//
// This interface deliberately carries no write methods. Indexing has to happen
// inside the projection's transaction, alongside the dedupe marker, so those
// methods take a *sql.Tx and are declared on repository.Indexer instead. Keeping
// them off here is what stops a caller writing a document outside a transaction,
// which is how the marker and the document come to disagree.
//
// An Elasticsearch implementation of the read half is a direct mapping: Search
// is a query with a bool filter and a sort, and Count is a count. The response
// shape below is chosen to survive that move: it carries an explicit score, an
// unpaged total, and a cursor-shaped page, rather than leaking a backend's
// pagination model.
type Engine interface {
	// Search returns a ranked page of documents matching q, plus the total
	// number of matches. Implementations must not mutate q.
	Search(ctx context.Context, q *model.SearchQuery) (*model.SearchResult, error)

	// Count returns how many documents the index holds, so an empty index can
	// be told apart from a broken one.
	Count(ctx context.Context) (int64, error)
}

// Service answers search queries.
type Service struct {
	engine Engine
}

// NewService returns a search service backed by engine.
func NewService(engine Engine) *Service {
	return &Service{engine: engine}
}

// Search returns a ranked page of products matching the term.
//
// Validation lives here rather than in the handler so the same rules apply
// however the query arrives, including over gRPC.
//
// The term is trimmed and lowercased before it reaches the engine. The
// lowercasing is not optional: the indexed column is already lowercased, and
// LIKE is case-sensitive, so an uppercase term would compare against lowercased
// text and match nothing at all. A search for "AURORA" returning zero while
// "aurora" returns results is the exact failure this line exists to prevent.
func (s *Service) Search(ctx context.Context, q *model.SearchQuery) (*model.SearchResult, error) {
	if q == nil {
		return nil, fmt.Errorf("%w: a search term is required", apperror.ErrInvalidInput)
	}

	term := strings.TrimSpace(q.Query)
	if term == "" {
		// Refused rather than treated as "match everything". An empty term is
		// the shape of a browse request, and browse is a different question
		// answered by a different service with a plan this index cannot serve.
		return nil, fmt.Errorf(
			"%w: a search term is required; use product-listing-service to browse the catalogue",
			apperror.ErrInvalidInput,
		)
	}
	if len([]rune(term)) > maxTermLength {
		return nil, fmt.Errorf(
			"%w: the search term must be at most %d characters", apperror.ErrInvalidInput, maxTermLength,
		)
	}
	if err := checkTrigramFloor(term); err != nil {
		return nil, err
	}

	if q.Limit <= 0 {
		q.Limit = defaultLimit
	}
	if q.Limit > maxLimit {
		return nil, fmt.Errorf(
			"%w: limit must be at most %d, got %d", apperror.ErrInvalidInput, maxLimit, q.Limit,
		)
	}
	if q.Offset < 0 {
		return nil, fmt.Errorf("%w: offset must not be negative, got %d", apperror.ErrInvalidInput, q.Offset)
	}
	if q.CategoryID < 0 {
		return nil, fmt.Errorf(
			"%w: category_id must not be negative, got %d", apperror.ErrInvalidInput, q.CategoryID,
		)
	}
	// Presence, not value, is what distinguishes a filter from its absence: a bound
	// of 0 is a legitimate price, so rejecting it would reject a real query.
	if q.MinPriceCents != nil && *q.MinPriceCents < 0 {
		return nil, fmt.Errorf(
			"%w: min_price_cents must not be negative, got %d",
			apperror.ErrInvalidInput, *q.MinPriceCents,
		)
	}
	if q.MaxPriceCents != nil && *q.MaxPriceCents < 0 {
		return nil, fmt.Errorf(
			"%w: max_price_cents must not be negative, got %d",
			apperror.ErrInvalidInput, *q.MaxPriceCents,
		)
	}
	// Only a range that is actually bounded can be inverted. With one side absent
	// there is nothing to contradict.
	if q.MinPriceCents != nil && q.MaxPriceCents != nil && *q.MinPriceCents > *q.MaxPriceCents {
		return nil, fmt.Errorf(
			"%w: min_price_cents (%d) must not exceed max_price_cents (%d)",
			apperror.ErrInvalidInput, *q.MinPriceCents, *q.MaxPriceCents,
		)
	}

	q.Query = strings.ToLower(term)
	return s.engine.Search(ctx, q)
}

// checkTrigramFloor rejects a term that a trigram index cannot help with.
//
// A trigram is three characters, and pg_trgm only extracts trigrams from runs of
// alphanumeric characters. A shorter run yields no trigram at all, and a pattern
// with none is handed to the index as "consider everything", which degrades into
// a full index scan. Verified against PostgreSQL 17: a two character needle
// produced a Seq Scan while a three character needle produced a Bitmap Index
// Scan on the same table and index.
//
// The floor is per word, not per term, which is the part that is easy to get
// wrong: "a b c" is five characters long and still has no trigram in it. So the
// check looks for a run of three, not for a length of three.
//
// This is the cost of the current engine, not a rule of search. A future
// Elasticsearch implementation could accept a shorter term, and the check belongs
// with the engine that needs it rather than here, in the engine-agnostic half.
// Until then it is enforced in the application because the alternative is a
// request path that scans the whole index on every keystroke.
func checkTrigramFloor(term string) error {
	run := 0
	for _, r := range term {
		if isTrigramChar(r) {
			run++
			if run >= 3 {
				return nil
			}
			continue
		}
		run = 0
	}
	return fmt.Errorf(
		"%w: the search term must contain at least 3 consecutive letters or digits, "+
			"got %q; a shorter pattern cannot use a trigram index and would scan every product",
		apperror.ErrInvalidInput, term,
	)
}

// isTrigramChar reports whether r contributes trigrams, matching the alnum-only
// extraction pg_trgm performs. Anything else - spaces, punctuation, non-ASCII
// symbols - is a word boundary.
func isTrigramChar(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}
