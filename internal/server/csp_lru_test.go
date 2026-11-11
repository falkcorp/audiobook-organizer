// file: internal/server/csp_lru_test.go
// version: 2.0.0
// guid: 1fc15002-a2c5-4b9e-b399-c7a3b7c4b6ac
// last-edited: 2026-11-10

package server

import "testing"

func TestVisitorLRU_EvictsTheLeastRecentlyWritten(t *testing.T) {
	l := newVisitorLRU[int](3)
	l.put("a", 1)
	l.put("b", 2)
	l.put("c", 3)
	l.put("a", 10) // rewriting a makes b the coldest, and never evicts
	if v, _ := l.get("a"); v != 10 || len(l.m) != 3 {
		t.Fatalf("a=%d len=%d", v, len(l.m))
	}
	l.put("d", 4)
	if _, ok := l.get("b"); ok {
		t.Fatal("b should have been evicted")
	}
	for _, k := range []string{"a", "c", "d"} {
		if _, ok := l.get(k); !ok {
			t.Fatalf("%s missing", k)
		}
	}
	if len(l.m) != 3 || l.l.Len() != 3 {
		t.Fatalf("map %d, list %d", len(l.m), l.l.Len())
	}
	if _, ok := newVisitorLRU[int](0).get("x"); ok {
		t.Fatal("an empty table found a key")
	}
}

func TestVisitorLRU_SweepStopsAtTheFirstLiveEntry(t *testing.T) {
	l := newVisitorLRU[int](10)
	for i := 1; i <= 6; i++ {
		l.put(string(rune('a'+i-1)), i)
	}
	visited := 0
	n := l.sweep(func(v int) bool { visited++; return v <= 2 })
	if n != 2 || len(l.m) != 4 {
		t.Fatalf("removed %d, len %d", n, len(l.m))
	}
	if visited != 3 {
		t.Fatalf("a sweep must look only at what it removes plus one live entry, looked at %d", visited)
	}
	if n := l.sweep(func(int) bool { return true }); n != 4 || len(l.m) != 0 || l.l.Len() != 0 {
		t.Fatalf("a full sweep removed %d, left map %d list %d", n, len(l.m), l.l.Len())
	}
}

// A size below one still holds one entry.
func TestVisitorLRU_HoldsAtLeastOne(t *testing.T) {
	l := newVisitorLRU[int](-3)
	l.put("a", 1)
	l.put("b", 2)
	if _, ok := l.get("a"); ok || len(l.m) != 1 {
		t.Fatalf("len %d", len(l.m))
	}
}
