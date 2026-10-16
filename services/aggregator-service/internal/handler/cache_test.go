package handler

import (
	"context"
	"fmt"
	"testing"

	"github.com/vektah/gqlparser/v2/ast"
)

// The caches are keyed on caller-supplied strings, so the bound is a security
// property rather than a tuning knob: without it, varying whitespace in an
// otherwise identical query fills memory until the process dies.

func TestDocumentCacheStoresAndReturns(t *testing.T) {
	cache := newDocumentCache(10)
	doc := &ast.QueryDocument{}
	ctx := context.Background()

	cache.Add(ctx, "query-a", doc)

	got, ok := cache.Get(ctx, "query-a")
	if !ok {
		t.Fatal("expected a hit")
	}
	if got != doc {
		t.Fatal("returned a different document")
	}
}

func TestDocumentCacheMissesForAnUnknownKey(t *testing.T) {
	cache := newDocumentCache(10)
	if _, ok := cache.Get(context.Background(), "never-stored"); ok {
		t.Fatal("expected a miss")
	}
}

func TestDocumentCacheStaysBounded(t *testing.T) {
	// The property that matters: an attacker sending distinct keys cannot grow the
	// cache without limit.
	const max = 8
	cache := newDocumentCache(max)
	ctx := context.Background()

	for i := range 500 {
		cache.Add(ctx, fmt.Sprintf("query-%d", i), &ast.QueryDocument{})
		if got := cache.size(); got > max {
			t.Fatalf("after %d inserts the cache holds %d entries; the bound is %d", i+1, got, max)
		}
	}
}

func TestDocumentCacheEvictsTheLeastRecentlyUsed(t *testing.T) {
	// Exact LRU order, not an approximation: the entry used longest ago goes first.
	const max = 2
	cache := newDocumentCache(max)
	ctx := context.Background()

	cache.Add(ctx, "a", &ast.QueryDocument{})
	cache.Add(ctx, "b", &ast.QueryDocument{})

	// Touch a, so b is now the least recently used.
	if _, ok := cache.Get(ctx, "a"); !ok {
		t.Fatal("expected a to be present")
	}

	cache.Add(ctx, "c", &ast.QueryDocument{})

	if _, ok := cache.Get(ctx, "b"); ok {
		t.Fatal("b was the least recently used and should have been evicted")
	}
	for _, key := range []string{"a", "c"} {
		if _, ok := cache.Get(ctx, key); !ok {
			t.Fatalf("%q should have survived", key)
		}
	}
}

func TestDocumentCacheOverwritesRatherThanDuplicating(t *testing.T) {
	cache := newDocumentCache(4)
	ctx := context.Background()

	cache.Add(ctx, "a", &ast.QueryDocument{})
	replacement := &ast.QueryDocument{}
	cache.Add(ctx, "a", replacement)

	if got := cache.size(); got != 1 {
		t.Fatalf("cache holds %d entries for one key", got)
	}
	got, _ := cache.Get(ctx, "a")
	if got != replacement {
		t.Fatal("the second insert did not replace the value")
	}
}

func TestDocumentCacheKeepsAWorkingSet(t *testing.T) {
	// A real client fleet sends the same handful of documents. Those should stay
	// resident while one-off queries are evicted, so the hot path keeps hitting.
	const max = 4
	cache := newDocumentCache(max)
	ctx := context.Background()

	hot := []string{"hot-a", "hot-b", "hot-c"}
	for _, key := range hot {
		cache.Add(ctx, key, &ast.QueryDocument{})
	}

	// Churn through one-off keys, touching the hot ones between rounds so they stay
	// referenced.
	for i := range 100 {
		cache.Add(ctx, fmt.Sprintf("cold-%d", i), &ast.QueryDocument{})
		for _, key := range hot {
			if _, ok := cache.Get(ctx, key); !ok {
				t.Fatalf("the hot key %q was evicted after %d cold inserts", key, i+1)
			}
		}
	}
}

func TestDocumentCacheEvictsWhenEveryEntryIsReferenced(t *testing.T) {
	// The pathological case: max smaller than the working set, so every entry is
	// referenced on every pass and the clock finds no unreferenced candidate. The
	// fallback clears the cache rather than growing it, because unbounded growth is
	// the failure this bound exists to prevent.
	cache := newDocumentCache(2)
	ctx := context.Background()

	for i := range 6 {
		cache.Add(ctx, fmt.Sprintf("query-%d", i), &ast.QueryDocument{})
		if got := cache.size(); got > 2 {
			t.Fatalf("cache grew to %d entries with a bound of 2", got)
		}
	}
}

func TestDocumentCacheZeroSizeUsesTheDefault(t *testing.T) {
	cache := newDocumentCache(0)
	if cache.documents.capacity != lruDefaultSize {
		t.Fatalf("capacity %d, want the default %d", cache.documents.capacity, lruDefaultSize)
	}
}

func TestPersistedQueryCacheStoresText(t *testing.T) {
	// The APQ store holds query text, not parsed documents. That is what the protocol
	// exchanges, and caching parsed documents here would not skip the parse, which is
	// the cost the protocol exists to remove.
	cache := newPersistedQueryCache(10)
	ctx := context.Background()

	const query = `query { products { id } }`
	cache.Add(ctx, "hash-1", query)

	got, ok := cache.Get(ctx, "hash-1")
	if !ok {
		t.Fatal("expected a hit")
	}
	if got != query {
		t.Fatalf("got %q", got)
	}
}

func TestPersistedQueryCacheMissesOnUnknownHash(t *testing.T) {
	cache := newPersistedQueryCache(10)
	if _, ok := cache.Get(context.Background(), "not-a-known-hash"); ok {
		t.Fatal("expected a miss")
	}
}

func TestPersistedQueryCacheStaysBounded(t *testing.T) {
	const max = 8
	cache := newPersistedQueryCache(max)
	ctx := context.Background()

	for i := range 500 {
		cache.Add(ctx, fmt.Sprintf("hash-%d", i), fmt.Sprintf("query-%d", i))
		if got := cache.size(); got > max {
			t.Fatalf("after %d inserts the cache holds %d entries; the bound is %d", i+1, got, max)
		}
	}
}

func TestPersistedQueryCacheOverwritesAnExistingKey(t *testing.T) {
	// Re-registering a hash replaces the text rather than adding a second entry, which
	// is what a client retrying after a deployment would do.
	cache := newPersistedQueryCache(4)
	ctx := context.Background()

	cache.Add(ctx, "hash-1", "first")
	cache.Add(ctx, "hash-1", "second")

	if cache.size() != 1 {
		t.Fatalf("cache holds %d entries for one key", cache.size())
	}
	got, _ := cache.Get(ctx, "hash-1")
	if got != "second" {
		t.Fatalf("got %q", got)
	}
}

func TestPersistedQueryCacheSatisfiesTheAPQInterface(t *testing.T) {
	// graphql.Cache[string] is what extension.AutomaticPersistedQuery requires, so a
	// mismatch here would only surface at server construction.
	var cache interface {
		Get(ctx context.Context, key string) (string, bool)
		Add(ctx context.Context, key string, value string)
	} = newPersistedQueryCache(4)
	_ = cache
}
