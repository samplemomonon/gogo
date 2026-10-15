package handler

import (
	"container/list"
	"context"
	"sync"

	"github.com/vektah/gqlparser/v2/ast"
)

// The caches in this file exist because a GraphQL endpoint re-parses a
// caller-supplied document on every request, while a production client sends the
// same handful of documents over and over. Re-parsing and re-validating them per
// request is overhead paid on the hot path for no new information.
//
// The bound is the part that matters. Both caches are keyed on caller-supplied
// strings, so an unbounded map is a memory leak anyone who can send a query can
// reach: vary whitespace in an otherwise identical document and every variant
// occupies an entry forever. lruDefaultSize is the countermeasure.

// lruDefaultSize bounds both caches.
const lruDefaultSize = 1000

// lru is a fixed-capacity least-recently-used cache.
//
// It is a map plus an intrusive list: a map lookup for the value and a list move for
// the recency, both O(1). An earlier clock-based version here was O(1) too but its
// eviction order was an approximation that could drop a key the client sends on
// every request while keeping one it sent once, which is the wrong trade for a cache
// whose entire purpose is to serve a small hot set.
//
// A mutex is enough rather than a lock-free ring: gqlgen consults the parse cache
// once per request, so contention is not the concern, and correctness under the race
// detector matters more here than shaving nanoseconds.
type lru[K comparable, V any] struct {
	mu       sync.Mutex
	entries  map[K]*list.Element
	order    *list.List // front is most recently used
	capacity int
}

type lruEntry[K comparable, V any] struct {
	key   K
	value V
}

func newLRU[K comparable, V any](capacity int) *lru[K, V] {
	if capacity <= 0 {
		capacity = lruDefaultSize
	}
	return &lru[K, V]{
		entries:  make(map[K]*list.Element, capacity),
		order:    list.New(),
		capacity: capacity,
	}
}

// Get returns a value and marks it most recently used.
func (c *lru[K, V]) get(key K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	element, ok := c.entries[key]
	if !ok {
		var zero V
		return zero, false
	}
	c.order.MoveToFront(element)
	return element.Value.(*lruEntry[K, V]).value, true
}

// Add stores a value, evicting the least recently used entry when full.
func (c *lru[K, V]) add(key K, value V) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if element, ok := c.entries[key]; ok {
		element.Value.(*lruEntry[K, V]).value = value
		c.order.MoveToFront(element)
		return
	}

	c.entries[key] = c.order.PushFront(&lruEntry[K, V]{key: key, value: value})

	if c.order.Len() > c.capacity {
		// The back is the least recently used. Removing it here rather than lazily
		// keeps the map's size equal to the bound at all times, so a reader cannot
		// observe a cache that has already outgrown its limit.
		if oldest := c.order.Back(); oldest != nil {
			c.order.Remove(oldest)
			delete(c.entries, oldest.Value.(*lruEntry[K, V]).key)
		}
	}
}

// len reports the entry count. Present so tests can assert the bound directly rather
// than inferring it.
func (c *lru[K, V]) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}

// documentCache stores parsed query documents.
type documentCache struct {
	documents *lru[string, *ast.QueryDocument]
}

func newDocumentCache(size int) *documentCache {
	return &documentCache{documents: newLRU[string, *ast.QueryDocument](size)}
}

func (c *documentCache) Get(ctx context.Context, key string) (*ast.QueryDocument, bool) {
	return c.documents.get(key)
}

func (c *documentCache) Add(ctx context.Context, key string, document *ast.QueryDocument) {
	c.documents.add(key, document)
}

func (c *documentCache) size() int { return c.documents.size() }

// persistedQueryCache implements the Automatic Persisted Queries store.
//
// It caches query *text*, not parsed documents, because that is what the protocol
// exchanges: the client sends a SHA-256 of its query, and the server either finds
// matching text here or replies PERSISTED_QUERY_NOT_FOUND and asks for the text once.
// Storing parsed documents would not skip the parse, which is the cost the protocol
// exists to remove.
//
// It is separate from the parse cache because they answer different questions. This
// one maps a client-supplied hash to query text; the parse cache maps a document to
// its parsed form.
type persistedQueryCache struct {
	queries *lru[string, string]
}

func newPersistedQueryCache(size int) *persistedQueryCache {
	return &persistedQueryCache{queries: newLRU[string, string](size)}
}

func (c *persistedQueryCache) Get(ctx context.Context, key string) (string, bool) {
	return c.queries.get(key)
}

// Add stores query text under its hash.
//
// Soundness comes from the caller: the APQ extension verifies that the supplied hash
// matches the supplied query before calling Add, so a client cannot poison the cache
// with a document that does not match its own key.
func (c *persistedQueryCache) Add(ctx context.Context, key string, value string) {
	c.queries.add(key, value)
}

func (c *persistedQueryCache) size() int { return c.queries.size() }
