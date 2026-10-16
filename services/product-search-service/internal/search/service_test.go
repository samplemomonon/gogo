package search

import (
	"context"
	"errors"
	"strings"
	"testing"

	"product-search-service/internal/apperror"
	"product-search-service/internal/model"
)

// stubEngine serves a fixed result and records the query it was handed.
type stubEngine struct {
	gotQuery *model.SearchQuery
	result   *model.SearchResult
	err      error

	indexed    []*model.SearchProduct
	indexedIDs []string
	removed    []int64
}

func (s *stubEngine) Search(_ context.Context, q *model.SearchQuery) (*model.SearchResult, error) {
	s.gotQuery = q
	if s.err != nil {
		return nil, s.err
	}
	if s.result != nil {
		return s.result, nil
	}
	return &model.SearchResult{Products: []*model.SearchProduct{}, Limit: q.Limit, Offset: q.Offset}, nil
}

func (s *stubEngine) Index(_ context.Context, doc *model.SearchProduct, eventID string) error {
	s.indexed = append(s.indexed, doc)
	s.indexedIDs = append(s.indexedIDs, eventID)
	return nil
}

func (s *stubEngine) Remove(_ context.Context, productID int64) error {
	s.removed = append(s.removed, productID)
	return nil
}

func (s *stubEngine) Count(context.Context) (int64, error) { return 0, nil }

func TestSearchValidatesTheTerm(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		query string
		want  string
	}{
		"empty":           {"", "a search term is required"},
		"whitespace only": {"   ", "a search term is required"},
		"one character":   {"a", "at least 3 consecutive"},
		"two characters":  {"ab", "at least 3 consecutive"},
		// The floor is per word, not per term. This is the case that is easy to
		// miss: five characters long, and still no trigram anywhere in it.
		"only short words": {"a b c", "at least 3 consecutive"},
		"punctuation only": {"---", "at least 3 consecutive"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := NewService(&stubEngine{}).Search(context.Background(),
				&model.SearchQuery{Query: tc.query})
			if !errors.Is(err, apperror.ErrInvalidInput) {
				t.Fatalf("err = %v, want ErrInvalidInput", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// A term that contains any run of three is searchable, however short the rest of
// it is. Refusing those would be a rule that looks reasonable and is wrong.
func TestSearchAcceptsATermWithOneSearchableRun(t *testing.T) {
	t.Parallel()

	// "a bc" is deliberately absent: its runs are one and two characters, so it
	// has no trigram either and the service is right to refuse it. The floor is
	// per run, not per term.
	for _, term := range []string{"abc", "usb c", "t-shirt", "x y z 12345", "50% off"} {
		engine := &stubEngine{}
		if _, err := NewService(engine).Search(context.Background(),
			&model.SearchQuery{Query: term}); err != nil {
			t.Errorf("%q: err = %v, want it accepted", term, err)
		}
	}
}

// An empty term is a browse request, and this index cannot serve one. Answering
// it with the whole catalogue would be wrong: it has no relevance order, and the
// caller asked a question about relevance.
func TestSearchRefusesAnEmptyTermAndNamesBrowse(t *testing.T) {
	t.Parallel()

	engine := &stubEngine{}
	_, err := NewService(engine).Search(context.Background(), &model.SearchQuery{Query: "  "})
	if !errors.Is(err, apperror.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
	if !strings.Contains(err.Error(), "product-listing-service") {
		t.Errorf("err = %v, want it to name the service that does serve browsing", err)
	}
	if engine.gotQuery != nil {
		t.Error("the engine was called for a term that was never valid")
	}
}

func TestSearchLowercasesTheTerm(t *testing.T) {
	t.Parallel()

	engine := &stubEngine{}
	if _, err := NewService(engine).Search(context.Background(),
		&model.SearchQuery{Query: "  AuRoRa Phone  "},
	); err != nil {
		t.Fatalf("Search: %v", err)
	}

	// Not cosmetic. The indexed column is lowercased and LIKE is case-sensitive,
	// so an unlowercased term matches nothing at all. Verified against
	// PostgreSQL 17: '%AURORA%' returned 0 rows where '%aurora%' returned 2.
	if engine.gotQuery.Query != "aurora phone" {
		t.Errorf("term = %q, want it trimmed and lowercased", engine.gotQuery.Query)
	}
}

func TestSearchRejectsAnOverlongTerm(t *testing.T) {
	t.Parallel()

	term := strings.Repeat("a", maxTermLength+1)
	if _, err := NewService(&stubEngine{}).Search(context.Background(),
		&model.SearchQuery{Query: term}); !errors.Is(err, apperror.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}

func TestSearchPaging(t *testing.T) {
	t.Parallel()

	t.Run("applies the default page size", func(t *testing.T) {
		t.Parallel()

		engine := &stubEngine{}
		if _, err := NewService(engine).Search(context.Background(),
			&model.SearchQuery{Query: "aurora"}); err != nil {
			t.Fatalf("Search: %v", err)
		}
		if engine.gotQuery.Limit != defaultLimit {
			t.Errorf("limit = %d, want %d", engine.gotQuery.Limit, defaultLimit)
		}
	})

	t.Run("rejects invalid paging and filters", func(t *testing.T) {
		t.Parallel()

		cases := map[string]*model.SearchQuery{
			"limit too large":     {Query: "aurora", Limit: maxLimit + 1},
			"negative offset":     {Query: "aurora", Offset: -1},
			"negative category":   {Query: "aurora", CategoryID: -1},
			"negative min price":  {Query: "aurora", MinPriceCents: pricePointer(-1)},
			"negative max price":  {Query: "aurora", MaxPriceCents: pricePointer(-1)},
			"inverted price band": {Query: "aurora", MinPriceCents: pricePointer(1000), MaxPriceCents: pricePointer(100)},
		}
		for name, q := range cases {
			if _, err := NewService(&stubEngine{}).Search(context.Background(), q); !errors.Is(err, apperror.ErrInvalidInput) {
				t.Errorf("%s: err = %v, want ErrInvalidInput", name, err)
			}
		}
	})
}

// The filters that reach the index have to express an absent value distinctly
// from a real one, or a search for "no status" silently becomes a search for
// drafts and the shopper sees an empty catalogue.
func TestSearchKeepsAnAbsentStatusFilterAsNil(t *testing.T) {
	t.Parallel()

	engine := &stubEngine{}
	if _, err := NewService(engine).Search(context.Background(),
		&model.SearchQuery{Query: "aurora"}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if engine.gotQuery.Status != nil {
		t.Errorf("status = %v, want nil for an absent filter", *engine.gotQuery.Status)
	}
}

// pricePointer builds a present price bound. The query field is a pointer so that
// "no filter" and "a bound of zero" stay distinguishable all the way to SQL.
func pricePointer(v int64) *int64 { return &v }
