package cache

import (
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestCacheSetAndGet(t *testing.T) {
	t.Parallel()

	c := New[string](Options{MaxSize: 10, DefaultTTL: time.Minute})
	c.Set("key1", "value1")

	val, ok := c.Get("key1")
	if !ok {
		t.Fatal("expected to find key1")
	}
	if val != "value1" {
		t.Errorf("got %v, want value1", val)
	}
}

func TestCacheMiss(t *testing.T) {
	t.Parallel()

	c := New[string](Options{MaxSize: 10, DefaultTTL: time.Minute})
	_, ok := c.Get("nonexistent")
	if ok {
		t.Error("expected cache miss for nonexistent key")
	}
}

func TestCacheExpiration(t *testing.T) {
	t.Parallel()

	c := New[string](Options{MaxSize: 10, DefaultTTL: 10 * time.Millisecond})
	c.Set("key1", "value1")

	time.Sleep(20 * time.Millisecond)

	_, ok := c.Get("key1")
	if ok {
		t.Error("expected cache entry to expire")
	}
}

func TestCacheDelete(t *testing.T) {
	t.Parallel()

	c := New[string](Options{MaxSize: 10, DefaultTTL: time.Minute})
	c.Set("key1", "value1")
	c.Delete("key1")

	_, ok := c.Get("key1")
	if ok {
		t.Error("expected cache miss after delete")
	}
}

func TestCacheClear(t *testing.T) {
	t.Parallel()

	c := New[string](Options{MaxSize: 10, DefaultTTL: time.Minute})
	c.Set("key1", "value1")
	c.Set("key2", "value2")
	c.Clear()

	if c.Len() != 0 {
		t.Errorf("expected empty cache, got %d items", c.Len())
	}
}

func TestCacheLRUEviction(t *testing.T) {
	t.Parallel()

	c := New[string](Options{MaxSize: 2, DefaultTTL: time.Minute})
	c.Set("key1", "value1")
	c.Set("key2", "value2")
	c.Set("key3", "value3")

	if c.Len() != 2 {
		t.Errorf("expected 2 items after eviction, got %d", c.Len())
	}

	_, ok := c.Get("key1")
	if ok {
		t.Error("expected key1 to be evicted (LRU)")
	}
}

func TestCacheCleanup(t *testing.T) {
	t.Parallel()

	c := New[string](Options{MaxSize: 10, DefaultTTL: 10 * time.Millisecond})
	c.Set("key1", "value1")
	c.Set("key2", "value2")

	time.Sleep(20 * time.Millisecond)

	removed := c.Cleanup()
	if removed != 2 {
		t.Errorf("expected 2 expired entries removed, got %d", removed)
	}
	if c.Len() != 0 {
		t.Errorf("expected empty cache after cleanup, got %d items", c.Len())
	}
}

func TestCacheSetWithTTL(t *testing.T) {
	t.Parallel()

	c := New[string](Options{MaxSize: 10})
	c.SetWithTTL("key1", "value1", 10*time.Millisecond)

	time.Sleep(20 * time.Millisecond)

	_, ok := c.Get("key1")
	if ok {
		t.Error("expected cache entry to expire with custom TTL")
	}
}

// TestCacheSharedInstanceIsRaceFree exercises one Cache from many goroutines.
//
// Every other test in this file builds its own Cache and calls t.Parallel, so
// no two goroutines ever touch the same instance — which is precisely why a data
// race in Get survived here. Production does the opposite: main.go hands one
// Cache to the service and every request goroutine reads through it.
//
// This only fails under `go test -race`. The read-write pair is what makes it
// useful: concurrent Get alone would find the map read under a shared lock
// benign, and it is the MoveToFront that writes through the same list element.
func TestCacheSharedInstanceIsRaceFree(t *testing.T) {
	t.Parallel()

	// A small MaxSize keeps the eviction path running alongside the read path,
	// so the map and the list are both being restructured while readers walk them.
	c := New[int](Options{MaxSize: 8, DefaultTTL: time.Minute})

	const goroutines = 32
	const iterations = 300

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				// Deliberately overlapping keys: goroutines hit the same entries
				// concurrently, which is what a hot product looks like.
				key := strconv.Itoa((g + i) % 12)
				c.Set(key, i)
				c.Get(key)
				if i%50 == 0 {
					c.Len()
					c.Cleanup()
				}
			}
		}(g)
	}
	wg.Wait()
}

// TestCacheGetRemovesExpiredEntry covers the reclamation that Get now performs.
func TestCacheGetRemovesExpiredEntry(t *testing.T) {
	t.Parallel()

	c := New[string](Options{MaxSize: 10})
	c.SetWithTTL("gone", "value", 10*time.Millisecond)
	c.Set("kept", "value")

	time.Sleep(20 * time.Millisecond)

	if _, ok := c.Get("gone"); ok {
		t.Fatal("expected the expired entry to miss")
	}
	if got := c.Len(); got != 1 {
		t.Errorf("expected the expired entry to be dropped on read, cache holds %d entries", got)
	}
	if _, ok := c.Get("kept"); !ok {
		t.Error("expired read must not disturb unrelated entries")
	}
}
