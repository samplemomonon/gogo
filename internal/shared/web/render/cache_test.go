package render

import (
	"testing"
	"time"
)

func TestTTLCacheCapacityEvictsLeastRecentlyUsed(t *testing.T) {
	t.Parallel()

	cache := NewTTLCacheWithLimit(3)
	for i := 0; i < 3; i++ {
		cache.Set("k"+string(rune('a'+i)), []byte("v"), 0)
	}

	// Touch "ka" so it becomes the most recently used entry; the next insert
	// must then evict "kb" (the LRU), not "ka".
	if _, ok := cache.Get("ka"); !ok {
		t.Fatal("expected ka to be cached")
	}
	cache.Set("new", []byte("ok"), 0)

	if _, ok := cache.Get("kb"); ok {
		t.Error("least-recently-used entry should have been evicted at capacity")
	}
	if got := cache.Len(); got != 3 {
		t.Errorf("Len() = %d after eviction, want 3", got)
	}
	for _, key := range []string{"ka", "kc", "new"} {
		if _, ok := cache.Get(key); !ok {
			t.Errorf("entry %q should still be cached", key)
		}
	}
}

func TestTTLCacheGetReturnsDefensiveCopy(t *testing.T) {
	t.Parallel()

	cache := NewTTLCache()
	cache.Set("k", []byte("original"), 0)

	data, ok := cache.Get("k")
	if !ok {
		t.Fatal("expected a cache hit")
	}
	data[0] = 'X'

	fresh, ok := cache.Get("k")
	if !ok {
		t.Fatal("second read should still hit")
	}
	if string(fresh) != "original" {
		t.Errorf("stored entry = %q after mutating a returned copy, want original", fresh)
	}
}

func TestTTLCacheSetStoresCopyOfInput(t *testing.T) {
	t.Parallel()

	cache := NewTTLCache()
	input := []byte("value")
	cache.Set("k", input, 0)
	input[0] = 'X'

	data, ok := cache.Get("k")
	if !ok {
		t.Fatal("expected a cache hit")
	}
	if string(data) != "value" {
		t.Errorf("stored entry = %q after mutating the input, want value", data)
	}
}

func TestTTLCacheLen(t *testing.T) {
	t.Parallel()

	cache := NewTTLCache()
	cache.Set("a", []byte("1"), 0)
	cache.Set("b", []byte("2"), time.Second)
	if got := cache.Len(); got != 2 {
		t.Errorf("Len() = %d, want 2", got)
	}

	cache.Invalidate("a")
	if got := cache.Len(); got != 1 {
		t.Errorf("Len() after invalidate = %d, want 1", got)
	}
}

func TestTTLCacheInvalidateClearsRecencyOrder(t *testing.T) {
	t.Parallel()

	cache := NewTTLCacheWithLimit(2)
	cache.Set("a", []byte("1"), 0)
	cache.Set("b", []byte("2"), 0)
	cache.Invalidate("a")

	// After invalidating "a", the recency order must drop it cleanly so the
	// next insert past capacity evicts the true LRU ("b"), not a dangling
	// reference.
	cache.Set("c", []byte("3"), 0)
	cache.Set("d", []byte("4"), 0)

	if _, ok := cache.Get("b"); ok {
		t.Error("b should have been evicted once the order caught up")
	}
	if _, ok := cache.Get("d"); !ok {
		t.Error("newest entry should be cached")
	}
}

func TestNewTTLCacheWithLimitFallsBackForZero(t *testing.T) {
	t.Parallel()

	if got := NewTTLCacheWithLimit(0); got.maxLen != defaultCacheLimit {
		t.Errorf("maxLen = %d, want %d", got.maxLen, defaultCacheLimit)
	}
	if got := NewTTLCacheWithLimit(-1); got.maxLen != defaultCacheLimit {
		t.Errorf("negative maxLen = %d, want %d", got.maxLen, defaultCacheLimit)
	}
}

// TestCacheCountersTracksHitsAndMisses pins the process-wide hit/miss counters
// that back the /metrics and /debug/vars cache observability. It compares
// deltas because the counters are shared across every cache and test in the
// package.
func TestCacheCountersTracksHitsAndMisses(t *testing.T) {
	startHits, startMisses := CacheCounters()

	cache := NewTTLCache()
	cache.Set("present", []byte("v"), 0)

	if _, ok := cache.Get("present"); !ok {
		t.Fatal("expected a hit")
	}
	if _, ok := cache.Get("missing"); ok {
		t.Fatal("expected a miss")
	}
	if _, ok := cache.Get("missing"); ok {
		t.Fatal("expected a second miss")
	}

	endHits, endMisses := CacheCounters()
	if want := startHits + 1; endHits != want {
		t.Errorf("hits = %d, want %d", endHits, want)
	}
	if want := startMisses + 2; endMisses != want {
		t.Errorf("misses = %d, want %d", endMisses, want)
	}

	// A read of an expired entry is also a miss.
	expiring := NewTTLCache()
	expiring.Set("expired", []byte("v"), time.Nanosecond)
	time.Sleep(time.Millisecond)
	if _, ok := expiring.Get("expired"); ok {
		t.Fatal("expected an expired entry to miss")
	}
	_, endMisses = CacheCounters()
	if want := startMisses + 3; endMisses != want {
		t.Errorf("misses after expiry = %d, want %d", endMisses, want)
	}
}
