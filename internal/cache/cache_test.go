// file: internal/cache/cache_test.go
// version: 1.4.0
// guid: b2c3d4e5-f6a7-8b9c-0d1e-2f3a4b5c6d7e

package cache

import (
	"strconv"
	"testing"
	"time"
)

func TestGetSet(t *testing.T) {
	c := New[string]("test_getset", time.Minute)
	c.Set("k", "v")
	v, ok := c.Get("k")
	if !ok || v != "v" {
		t.Fatalf("expected v, got %q ok=%v", v, ok)
	}
}

func TestExpiry(t *testing.T) {
	c := New[int]("test_expiry", time.Millisecond)
	c.Set("k", 42)
	time.Sleep(5 * time.Millisecond)
	_, ok := c.Get("k")
	if ok {
		t.Fatal("expected expired entry")
	}
}

func TestInvalidate(t *testing.T) {
	c := New[string]("test_invalidate", time.Minute)
	c.Set("a", "1")
	c.Set("b", "2")
	c.Invalidate("a")
	_, ok := c.Get("a")
	if ok {
		t.Fatal("expected a to be invalidated")
	}
	v, ok := c.Get("b")
	if !ok || v != "2" {
		t.Fatal("expected b to remain")
	}
}

func TestInvalidateAll(t *testing.T) {
	c := New[int]("test_invalidate_all", time.Minute)
	c.Set("a", 1)
	c.Set("b", 2)
	c.InvalidateAll()
	_, ok := c.Get("a")
	if ok {
		t.Fatal("expected all invalidated")
	}
}

func TestName(t *testing.T) {
	c := New[int]("named", time.Minute)
	if got := c.Name(); got != "named" {
		t.Fatalf("expected name=named, got %q", got)
	}
}

func TestKeys(t *testing.T) {
	c := New[string]("test_keys", time.Minute)
	c.Set("zebra", "z")
	c.Set("alpha", "a")
	c.Set("beta", "b")

	keys := c.Keys()
	if len(keys) != 3 {
		t.Fatalf("expected 3 keys, got %d", len(keys))
	}

	// Check sorted order
	if keys[0] != "alpha" || keys[1] != "beta" || keys[2] != "zebra" {
		t.Fatalf("expected sorted [alpha beta zebra], got %v", keys)
	}

	// Invalidate one and check again
	c.Invalidate("beta")
	keys = c.Keys()
	if len(keys) != 2 || keys[0] != "alpha" || keys[1] != "zebra" {
		t.Fatalf("expected [alpha zebra] after delete, got %v", keys)
	}
}

func TestLRUCapacityEviction(t *testing.T) {
	c := NewWithLimit[string]("test_lru", time.Minute, 3)
	c.Set("a", "1")
	c.Set("b", "2")
	c.Set("c", "3")
	if got := c.Len(); got != 3 {
		t.Fatalf("expected size 3, got %d", got)
	}
	// Touch "a" so it becomes most-recent; "b" is now least-recent.
	if _, ok := c.Get("a"); !ok {
		t.Fatal("expected a to be present")
	}
	c.Set("d", "4") // pushes "b" out
	if _, ok := c.Get("b"); ok {
		t.Fatal("expected b to be evicted")
	}
	for _, k := range []string{"a", "c", "d"} {
		if _, ok := c.Get(k); !ok {
			t.Fatalf("expected %s to remain after capacity eviction", k)
		}
	}
}

// TestLRUCapacityBoundedAtN inserts N+10 entries with no recency touches and
// verifies the cap is enforced: only N entries remain, the oldest 10 are
// evicted, and the newest N are retained. This is the MAYDEPLOY-I4 contract
// that prevents unbounded growth of the list/facets/dedup/book caches.
func TestLRUCapacityBoundedAtN(t *testing.T) {
	const N = 50
	c := NewWithLimit[int]("test_lru_n_plus_10", time.Minute, N)
	for i := range N + 10 {
		c.Set(strconv.Itoa(i), i)
	}
	if got := c.Len(); got != N {
		t.Fatalf("expected exactly %d entries after N+10 inserts, got %d", N, got)
	}
	// Oldest 10 (keys 0..9) must be evicted.
	for i := range 10 {
		if _, ok := c.Get(strconv.Itoa(i)); ok {
			t.Fatalf("expected key %d to be evicted (oldest)", i)
		}
	}
	// Newest N (keys 10..N+9) must be retained.
	for i := 10; i < N+10; i++ {
		if _, ok := c.Get(strconv.Itoa(i)); !ok {
			t.Fatalf("expected key %d to be retained", i)
		}
	}
}

func TestLRUUpdateInPlaceDoesNotEvict(t *testing.T) {
	c := NewWithLimit[int]("test_lru_update", time.Minute, 2)
	c.Set("a", 1)
	c.Set("b", 2)
	c.Set("a", 99) // overwrite, not new entry — no eviction expected
	if c.Len() != 2 {
		t.Fatalf("size grew unexpectedly: %d", c.Len())
	}
	v, _ := c.Get("a")
	if v != 99 {
		t.Fatalf("expected updated value 99, got %d", v)
	}
}

func TestLazyExpiredOnGet(t *testing.T) {
	c := New[int]("test_lazy", time.Millisecond)
	c.Set("k", 7)
	if c.Len() != 1 {
		t.Fatalf("expected 1 entry, got %d", c.Len())
	}
	time.Sleep(5 * time.Millisecond)
	if _, ok := c.Get("k"); ok {
		t.Fatal("expected miss")
	}
	// Lazy reap means the entry should have been evicted by Get.
	if c.Len() != 0 {
		t.Fatalf("expected 0 entries after lazy reap, got %d", c.Len())
	}
}

func TestUnboundedAcceptsMany(t *testing.T) {
	c := New[int]("test_unbounded", time.Minute)
	for i := range 100 {
		c.SetWithTTL(string(rune('a'+i%26))+string(rune('a'+i/26)), i, time.Minute)
	}
	if c.Len() == 0 {
		t.Fatal("expected entries to be retained when unbounded")
	}
}
