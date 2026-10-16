package render

import (
	"container/list"
	"expvar"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// cacheHits and cacheMisses aggregate every TTLCache in the process, so a
// single hit-ratio metric describes the whole rendering layer (home, seller and
// catalog islands together). The counters are published as the expvar Func
// "gogo_render_cache" and rendered into /metrics by the gateway.
var (
	cacheHits   atomic.Uint64
	cacheMisses atomic.Uint64
)

func init() {
	expvar.Publish("gogo_render_cache", expvar.Func(func() any {
		hits, misses := CacheCounters()
		return map[string]uint64{"hits": hits, "misses": misses, "total": hits + misses}
	}))
}

// CacheCounters returns the process-wide render-cache hit and miss totals.
// Operators use hits/(hits+misses) to judge whether an island's ISR TTL or
// invalidation policy is effective on a heavy-read storefront.
func CacheCounters() (hits, misses uint64) {
	hits = cacheHits.Load()
	misses = cacheMisses.Load()
	return hits, misses
}

// Cache stores pre-rendered island HTML so revalidated (ISR) and static
// (SSG) islands can skip re-rendering. Implementations must be safe for
// concurrent use.
type Cache interface {
	Get(key string) ([]byte, bool)
	Set(key string, value []byte, ttl time.Duration)
	Invalidate(keys ...string)
	// InvalidatePrefix drops every entry whose key begins with prefix. It is
	// how a family of related islands (e.g. one cache entry per paginated
	// page of a listing) is cleared in a single call.
	InvalidatePrefix(prefix string)
}

// defaultCacheLimit bounds the number of distinct island HTML documents the
// TTLCache retains. Island keys are bounded by the product set, but a cache
// that can hold a million keys protects nothing.
const defaultCacheLimit = 1024

// TTLCache is a bounded, concurrency-safe in-memory cache with per-entry
// expiry and least-recently-used eviction. A TTL of zero means the entry
// never expires on its own (SSG semantics) and is only removed by an explicit
// Invalidate call, e.g. after a mutation invalidates the affected islands.
//
// The cache holds at most maxLen entries. LRU eviction (rather than FIFO)
// keeps the working set: a popular island accessed every request is never
// evicted in favour of a cold one that merely arrived later. Get and Set
// trade copies of the payload so callers can never mutate the stored bytes
// concurrently.
type TTLCache struct {
	mu     sync.Mutex
	items  map[string]*list.Element // key -> element holding *entry
	order  *list.List               // front = most recently used
	maxLen int
}

type entry struct {
	key     string
	data    []byte
	expires time.Time
}

// NewTTLCache returns a cache that retains at most defaultCacheLimit
// entries.
func NewTTLCache() *TTLCache {
	return NewTTLCacheWithLimit(defaultCacheLimit)
}

// NewTTLCacheWithLimit returns a cache that retains at most maxLen entries.
// A non-positive limit falls back to the default.
func NewTTLCacheWithLimit(maxLen int) *TTLCache {
	if maxLen <= 0 {
		maxLen = defaultCacheLimit
	}
	return &TTLCache{
		items:  make(map[string]*list.Element, maxLen),
		order:  list.New(),
		maxLen: maxLen,
	}
}

func (c *TTLCache) Get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	element, ok := c.items[key]
	if !ok {
		cacheMisses.Add(1)
		return nil, false
	}
	item := element.Value.(*entry)
	if !item.expires.IsZero() && time.Now().After(item.expires) {
		c.removeLocked(element)
		cacheMisses.Add(1)
		return nil, false
	}
	// Mark as most recently used.
	c.order.MoveToFront(element)
	// Return a copy: the caller may hold the slice beyond the call and must
	// not be able to mutate the entry under another reader.
	cacheHits.Add(1)
	return append([]byte(nil), item.data...), true
}

func (c *TTLCache) Set(key string, value []byte, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	var expires time.Time
	if ttl > 0 {
		expires = time.Now().Add(ttl)
	}
	data := append([]byte(nil), value...)

	if element, ok := c.items[key]; ok {
		item := element.Value.(*entry)
		item.data = data
		item.expires = expires
		c.order.MoveToFront(element)
		return
	}

	c.items[key] = c.order.PushFront(&entry{key: key, data: data, expires: expires})
	c.evictLocked()
}

func (c *TTLCache) Invalidate(keys ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, key := range keys {
		if element, ok := c.items[key]; ok {
			c.removeLocked(element)
		}
	}
}

// InvalidatePrefix drops every entry whose key begins with prefix. Used to
// clear a whole family of related islands (one per paginated page) at once.
func (c *TTLCache) InvalidatePrefix(prefix string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for key, element := range c.items {
		if strings.HasPrefix(key, prefix) {
			c.removeLocked(element)
		}
	}
}

// Len reports the number of cached entries. Used by observability.
func (c *TTLCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}

// evictLocked keeps the cache within its bound, dropping at most one entry.
// An expired entry is dropped first (it is useless whether or not the cache
// is full); otherwise the least-recently-used entry goes. Callers hold the
// lock.
func (c *TTLCache) evictLocked() {
	if len(c.items) <= c.maxLen {
		return
	}
	for element := c.order.Back(); element != nil; element = element.Prev() {
		if !element.Value.(*entry).expires.IsZero() {
			c.removeLocked(element)
			return
		}
	}
	if back := c.order.Back(); back != nil {
		c.removeLocked(back)
	}
}

// removeLocked removes an element from both the index and the order list.
// Callers hold the lock.
func (c *TTLCache) removeLocked(element *list.Element) {
	c.order.Remove(element)
	delete(c.items, element.Value.(*entry).key)
}
