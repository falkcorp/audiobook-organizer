// file: internal/database/pebble_scan_prefix_page_test.go
// version: 1.0.0
// guid: c0c043a1-4b1d-4cc2-8ddb-61a8be59cea2
// last-edited: 2026-09-19

package database

import (
	"fmt"
	"slices"
	"testing"
)

func newRawPebble(t *testing.T) *PebbleStore {
	t.Helper()
	s, err := NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// seedRaw writes n keys prefix+"%03d" plus neighbours on both sides of the
// prefix, so a scan that leaks past its bounds shows up as extra keys.
func seedRaw(t *testing.T, s *PebbleStore, prefix string, n int) []string {
	t.Helper()
	for _, k := range []string{"aaa:before", "zzz:after", prefix[:len(prefix)-1]} {
		if err := s.SetRaw(k, []byte("neighbour")); err != nil {
			t.Fatal(err)
		}
	}
	var want []string
	for i := range n {
		k := fmt.Sprintf("%s%03d", prefix, i)
		if err := s.SetRaw(k, []byte(k)); err != nil {
			t.Fatal(err)
		}
		want = append(want, k)
	}
	return want
}

// drain pages through prefix with the given limit and returns every key in
// order plus the size of each page.
func drain(t *testing.T, s *PebbleStore, prefix string, limit int) (keys []string, pageSizes []int) {
	t.Helper()
	after := ""
	for range 10_000 {
		pairs, next, err := s.ScanPrefixPage(prefix, after, limit)
		if err != nil {
			t.Fatalf("ScanPrefixPage(after=%q): %v", after, err)
		}
		if len(pairs) > limit {
			t.Fatalf("page of %d exceeds limit %d", len(pairs), limit)
		}
		pageSizes = append(pageSizes, len(pairs))
		for _, kv := range pairs {
			if string(kv.Value) != kv.Key {
				t.Fatalf("value for %s = %q", kv.Key, kv.Value)
			}
			keys = append(keys, kv.Key)
		}
		if next == "" {
			return keys, pageSizes
		}
		if len(pairs) == 0 || next != pairs[len(pairs)-1].Key {
			t.Fatalf("next=%q is not the last key of a non-empty page (%d pairs)", next, len(pairs))
		}
		after = next
	}
	t.Fatal("scan never terminated")
	return nil, nil
}

func TestScanPrefixPage_AcrossPageBoundaries(t *testing.T) {
	s := newRawPebble(t)
	want := seedRaw(t, s, "pg:", 10)
	got, sizes := drain(t, s, "pg:", 3)
	if !slices.Equal(got, want) {
		t.Fatalf("keys = %v, want %v", got, want)
	}
	if !slices.Equal(sizes, []int{3, 3, 3, 1}) {
		t.Fatalf("page sizes = %v, want [3 3 3 1]", sizes)
	}
}

func TestScanPrefixPage_ExactMultipleEndsWithoutEmptyPage(t *testing.T) {
	s := newRawPebble(t)
	want := seedRaw(t, s, "pg:", 9)
	got, sizes := drain(t, s, "pg:", 3)
	if !slices.Equal(got, want) {
		t.Fatalf("keys = %v, want %v", got, want)
	}
	if !slices.Equal(sizes, []int{3, 3, 3}) {
		t.Fatalf("page sizes = %v, want [3 3 3] (no trailing empty page)", sizes)
	}
}

func TestScanPrefixPage_Empty(t *testing.T) {
	s := newRawPebble(t)
	seedRaw(t, s, "pg:", 0)
	pairs, next, err := s.ScanPrefixPage("pg:", "", 5)
	if err != nil || len(pairs) != 0 || next != "" {
		t.Fatalf("empty prefix: pairs=%d next=%q err=%v", len(pairs), next, err)
	}
}

func TestScanPrefixPage_AfterIsExclusiveAndNeedNotExist(t *testing.T) {
	s := newRawPebble(t)
	seedRaw(t, s, "pg:", 5)
	pairs, _, err := s.ScanPrefixPage("pg:", "pg:001", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 3 || pairs[0].Key != "pg:002" {
		t.Fatalf("after existing key: got %d pairs starting %v", len(pairs), pairs)
	}
	// A cursor whose key was deleted in the meantime (Prune deletes behind
	// its cursor) resumes at the next key rather than restarting.
	if err := s.DeleteRaw("pg:002"); err != nil {
		t.Fatal(err)
	}
	pairs, _, err = s.ScanPrefixPage("pg:", "pg:002", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 2 || pairs[0].Key != "pg:003" {
		t.Fatalf("after deleted key: got %v", pairs)
	}
}

func TestScanPrefixPage_TrailingFFPrefix(t *testing.T) {
	s := newRawPebble(t)
	prefix := "ff\xff"
	for _, k := range []string{prefix + "a", prefix + "b", "fg", "ff"} {
		if err := s.SetRaw(k, []byte(k)); err != nil {
			t.Fatal(err)
		}
	}
	pairs, next, err := s.ScanPrefixPage(prefix, "", 10)
	if err != nil || next != "" || len(pairs) != 2 {
		t.Fatalf("0xff prefix: pairs=%v next=%q err=%v", pairs, next, err)
	}
}

func TestScanPrefixPage_RejectsNonPositiveLimit(t *testing.T) {
	s := newRawPebble(t)
	for _, l := range []int{0, -1} {
		if _, _, err := s.ScanPrefixPage("pg:", "", l); err == nil {
			t.Errorf("limit %d accepted", l)
		}
	}
}

func TestDeleteRawBatch(t *testing.T) {
	s := newRawPebble(t)
	seedRaw(t, s, "pg:", 4)
	if err := s.DeleteRawBatch([]string{"pg:000", "pg:002", "pg:missing"}); err != nil {
		t.Fatalf("DeleteRawBatch: %v", err)
	}
	if err := s.DeleteRawBatch(nil); err != nil {
		t.Fatalf("empty batch: %v", err)
	}
	got, _ := drain(t, s, "pg:", 10)
	if !slices.Equal(got, []string{"pg:001", "pg:003"}) {
		t.Fatalf("remaining = %v", got)
	}
}
