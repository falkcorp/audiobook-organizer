// file: internal/server/absauth/lru_test.go
// version: 1.1.0
// guid: 1fc15002-a2c5-4b9e-b399-c7a3b7c4b6ac
// last-edited: 2026-10-22

package absauth

import "testing"

func TestLRU_EvictsTheLeastRecentlyWritten(t *testing.T) {
	l := NewLRU[int](3)
	l.Put("a", 1)
	l.Put("b", 2)
	l.Put("c", 3)
	l.Touch("a") // b is now the coldest
	l.Put("d", 4)
	if _, ok := l.Get("b"); ok {
		t.Fatal("b should have been evicted")
	}
	for _, k := range []string{"a", "c", "d"} {
		if _, ok := l.Get(k); !ok {
			t.Fatalf("%s missing", k)
		}
	}
	if l.Len() != 3 {
		t.Fatalf("len %d", l.Len())
	}
	l.Put("a", 10) // replacing never evicts
	if v, _ := l.Get("a"); v != 10 || l.Len() != 3 {
		t.Fatalf("a=%d len=%d", v, l.Len())
	}
}

func TestLRU_SweepStopsAtTheFirstLiveEntry(t *testing.T) {
	l := NewLRU[int](10)
	for i := 1; i <= 6; i++ {
		l.Put(string(rune('a'+i-1)), i)
	}
	visited := 0
	n := l.Sweep(func(v int) bool { visited++; return v <= 2 })
	if n != 2 || l.Len() != 4 {
		t.Fatalf("removed %d, len %d", n, l.Len())
	}
	if visited != 3 {
		t.Fatalf("a sweep must look only at what it removes plus one live entry, looked at %d", visited)
	}
	l.Delete("c")
	l.Delete("zzz") // absent: no-op
	if _, ok := l.Get("c"); ok || l.Len() != 3 {
		t.Fatalf("len %d", l.Len())
	}
}

// N4 / N3: at the bound the coldest UNPINNED entry goes first; a pinned one only
// when everything is pinned.
func TestLRU_EvictsUnpinnedBeforePinned(t *testing.T) {
	l := NewLRU[int](4)
	l.SetPinned(func(v int) bool { return v >= 100 })
	l.Put("hot1", 100) // oldest of all
	l.Put("hot2", 101)
	l.Put("c1", 1)
	l.Put("c2", 2)
	for i := 0; i < 50; i++ { // a stream of new keys
		l.Put("n"+string(rune('A'+i)), 3)
	}
	for _, k := range []string{"hot1", "hot2"} {
		if _, ok := l.Get(k); !ok {
			t.Fatalf("pinned %s was evicted while unpinned entries existed", k)
		}
	}
	if l.Len() != 4 {
		t.Fatalf("len %d", l.Len())
	}
	// Everything pinned: the coldest pinned entry goes, as before.
	l2 := NewLRU[int](3)
	l2.SetPinned(func(int) bool { return true })
	l2.Put("a", 1)
	l2.Put("b", 2)
	l2.Put("c", 3)
	l2.Put("d", 4)
	if _, ok := l2.Get("a"); ok || l2.Len() != 3 {
		t.Fatalf("with all pinned the coldest must go: len %d", l2.Len())
	}
}

// A value changed in place and re-filed with Touch moves between the tiers.
func TestLRU_TouchRefilesAnEntryWhoseValueChanged(t *testing.T) {
	type counter struct{ n int }
	l := NewLRU[*counter](2)
	l.SetPinned(func(c *counter) bool { return c.n >= 5 })
	a, b := &counter{}, &counter{}
	l.Put("a", a)
	l.Put("b", b)
	a.n = 5
	l.Touch("a") // now pinned
	l.Put("c", &counter{})
	if _, ok := l.Get("a"); !ok {
		t.Fatal("a became pinned and must outlive the unpinned b")
	}
	if _, ok := l.Get("b"); ok {
		t.Fatal("b is unpinned and the coldest: it goes")
	}
	a.n = 0
	l.Touch("a") // unpinned again
	l.Put("d", &counter{})
	l.Put("e", &counter{})
	if _, ok := l.Get("a"); ok {
		t.Fatal("a is unpinned again and the coldest")
	}
}

// N2: a pinned entry (a login in flight) at the cold end does not stop the sweep
// of the unpinned entries.
func TestLRU_SweepIsNotBlockedByAPinnedColdEntry(t *testing.T) {
	type v struct{ busy, stale bool }
	l := NewLRU[*v](100)
	l.SetPinned(func(x *v) bool { return x.busy })
	l.Put("busy", &v{busy: true}) // coldest of all
	for i := 0; i < 20; i++ {
		l.Put(string(rune('a'+i)), &v{stale: true})
	}
	n := l.Sweep(func(x *v) bool { return x.stale && !x.busy })
	if n != 20 || l.Len() != 1 {
		t.Fatalf("removed %d, len %d; the busy entry must stay and not shield the rest", n, l.Len())
	}
}
