// file: internal/server/absauth/lru_test.go
// version: 1.0.0
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
