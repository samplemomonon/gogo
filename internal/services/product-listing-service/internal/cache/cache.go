// Package cache provides an in-memory cache with TTL support and LRU eviction.
//
// The cache reduces database load for frequently read products. It is not a
// distributed cache: each replica holds its own view. That is acceptable only
// because the data is a projection that converges quickly, and it is bounded by
// two mechanisms rather than one:
//
//   - TTL bounds how stale a hit can be.
//   - The projection calls Delete for a product whose rows changed, which bounds
//     how stale the common case is. Without that second path the cache would
//     serve a pre-update product for the whole TTL after every write, so the
//     invalidation hook is load-bearing, not an optimisation.
//
// A TTL alone is not a correctness strategy for a read model that is written by
// events: the window it leaves open is exactly the window a user sees a product
// they just changed.
//
// Values are typed. Cache[V] stores V rather than any, so a caller reads back the
// type it stored. That removes the unchecked assertion this package used to
// force on its users: a key that held a Product could be read as a Variant, and
// the panic surfaced at the call site rather than here.
package cache

import (
	"container/list"
	"sync"
	"time"
)

// entry holds a cached value along with its expiration time.
type entry[V any] struct {
	key        string
	value      V
	expiration time.Time
}

// Cache is a thread-safe in-memory cache with TTL and LRU eviction.
//
// The mutex is a RWMutex because only Len is a genuine read: Get mutates the
// recency list on a hit, so it takes the write side. See Get.
type Cache[V any] struct {
	mu         sync.RWMutex
	items      map[string]*list.Element
	lru        *list.List
	maxSize    int
	defaultTTL time.Duration
}

// Options configures a Cache.
type Options struct {
	// MaxSize is the maximum number of entries. When the cache is full, the
	// least recently used entry is evicted. Zero means no limit.
	MaxSize int
	// DefaultTTL is the time after which an entry expires. Zero means no
	// expiration.
	DefaultTTL time.Duration
}

// New creates a cache of values of type V. The type argument cannot be inferred
// from Options, so a caller writes cache.New[*model.Product](cache.Options{...}).
func New[V any](opts Options) *Cache[V] {
	if opts.MaxSize <= 0 {
		opts.MaxSize = 1000
	}
	return &Cache[V]{
		items:      make(map[string]*list.Element),
		lru:        list.New(),
		maxSize:    opts.MaxSize,
		defaultTTL: opts.DefaultTTL,
	}
}

// Get retrieves a value from the cache. It reports false if the key is not found
// or has expired.
//
// The lock is exclusive, not shared: a hit moves its element to the front of the
// recency list, which is a mutation. Reading the map under RLock and then calling
// MoveToFront looked correct and was not — container/list mutates the element it
// is given, so every concurrent reader wrote through the same pointers while only
// holding a read lock. `go test -race` reproduces it on any shared instance, and
// production shares one Cache across every request goroutine.
//
// Len is the only read that can safely take a shared lock, because lru.Len reads
// only the length field and writes nothing.
func (c *Cache[V]) Get(key string) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	var zero V

	elem, ok := c.items[key]
	if !ok {
		return zero, false
	}

	e := elem.Value.(*entry[V])
	if !e.expiration.IsZero() && time.Now().After(e.expiration) {
		// Expired. Removing it here is safe now that the lock is already
		// exclusive, and it is better than leaving it to the next write: an entry
		// that has expired but is never overwritten would otherwise sit in the map
		// and consume a slot against maxSize.
		c.lru.Remove(elem)
		delete(c.items, key)
		return zero, false
	}

	c.lru.MoveToFront(elem)
	return e.value, true
}

// Set stores a value in the cache with the default TTL.
func (c *Cache[V]) Set(key string, value V) {
	c.SetWithTTL(key, value, c.defaultTTL)
}

// SetWithTTL stores a value in the cache with a specific TTL.
func (c *Cache[V]) SetWithTTL(key string, value V, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	expiration := time.Time{}
	if ttl > 0 {
		expiration = time.Now().Add(ttl)
	}

	if elem, ok := c.items[key]; ok {
		// Update existing entry.
		c.lru.MoveToFront(elem)
		stored := elem.Value.(*entry[V])
		stored.value = value
		stored.expiration = expiration
		return
	}

	// Add new entry.
	elem := c.lru.PushFront(&entry[V]{key: key, value: value, expiration: expiration})
	c.items[key] = elem

	// Evict oldest entries if we're over capacity.
	for c.lru.Len() > c.maxSize {
		oldest := c.lru.Back()
		if oldest == nil {
			break
		}
		c.lru.Remove(oldest)
		delete(c.items, oldest.Value.(*entry[V]).key)
	}
}

// Delete removes a key from the cache.
func (c *Cache[V]) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if elem, ok := c.items[key]; ok {
		c.lru.Remove(elem)
		delete(c.items, key)
	}
}

// Clear removes all entries from the cache.
func (c *Cache[V]) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.items = make(map[string]*list.Element)
	c.lru.Init()
}

// Len returns the number of entries in the cache.
func (c *Cache[V]) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lru.Len()
}

// Cleanup removes all expired entries and returns the number removed.
func (c *Cache[V]) Cleanup() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	removed := 0
	for key, elem := range c.items {
		e := elem.Value.(*entry[V])
		if !e.expiration.IsZero() && now.After(e.expiration) {
			c.lru.Remove(elem)
			delete(c.items, key)
			removed++
		}
	}
	return removed
}
