// file: internal/database/pebble_store_search_dirty_test.go
// version: 1.0.0
// guid: b3960a75-0209-4970-a35e-e139faf95d0b
// last-edited: 2026-08-09
//
// Tests for the search-index reconciliation dirty set.
//
// These are data-loss tests in the same spirit as the dataloss_*_test.go
// suite: the set exists precisely so that an index update which was already
// lost once cannot be lost a second time, so the properties under test are
// "survives", "is idempotent" and "does not over-delete".

package database

import (
	"fmt"
	"path/filepath"
	"testing"
)

func newDirtyTestStore(t *testing.T) *PebbleStore {
	t.Helper()
	s, err := NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestSearchIndexDirty_MarkListClear(t *testing.T) {
	s := newDirtyTestStore(t)

	if n, err := s.CountSearchIndexDirty(); err != nil || n != 0 {
		t.Fatalf("empty set: count=%d err=%v, want 0/nil", n, err)
	}

	for _, id := range []string{"book-b", "book-a", "book-c"} {
		if err := s.MarkSearchIndexDirty(id); err != nil {
			t.Fatalf("MarkSearchIndexDirty(%s): %v", id, err)
		}
	}

	n, err := s.CountSearchIndexDirty()
	if err != nil || n != 3 {
		t.Fatalf("count after 3 marks: %d (err=%v), want 3", n, err)
	}

	// Key order, not insertion order — the reconciler relies on a stable
	// order so partial drains make forward progress.
	ids, err := s.ListSearchIndexDirty(0)
	if err != nil {
		t.Fatalf("ListSearchIndexDirty: %v", err)
	}
	want := []string{"book-a", "book-b", "book-c"}
	if len(ids) != len(want) {
		t.Fatalf("list = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("list = %v, want %v", ids, want)
		}
	}

	if err := s.ClearSearchIndexDirty("book-b"); err != nil {
		t.Fatalf("ClearSearchIndexDirty: %v", err)
	}
	if n, _ := s.CountSearchIndexDirty(); n != 2 {
		t.Fatalf("count after clear: %d, want 2", n)
	}

	// Clearing an absent key must not error — the reconciler clears
	// optimistically and a double-clear is a normal race, not a fault.
	if err := s.ClearSearchIndexDirty("book-b"); err != nil {
		t.Fatalf("second clear should be a no-op, got %v", err)
	}
	if err := s.ClearSearchIndexDirty("never-existed"); err != nil {
		t.Fatalf("clearing absent key should be a no-op, got %v", err)
	}
}

func TestSearchIndexDirty_MarkIsIdempotent(t *testing.T) {
	s := newDirtyTestStore(t)

	// A book dropped repeatedly during a bulk operation must occupy exactly
	// one slot; otherwise the backlog figure that drives the adaptive batch
	// size would be inflated by duplicates.
	for i := 0; i < 50; i++ {
		if err := s.MarkSearchIndexDirty("same-book"); err != nil {
			t.Fatalf("mark %d: %v", i, err)
		}
	}
	if n, _ := s.CountSearchIndexDirty(); n != 1 {
		t.Fatalf("count after 50 marks of one ID: %d, want 1", n)
	}
}

func TestSearchIndexDirty_LimitBoundsTheBatch(t *testing.T) {
	s := newDirtyTestStore(t)

	const total = 2500
	for i := 0; i < total; i++ {
		if err := s.MarkSearchIndexDirty(fmt.Sprintf("book-%05d", i)); err != nil {
			t.Fatalf("mark %d: %v", i, err)
		}
	}
	if n, _ := s.CountSearchIndexDirty(); n != total {
		t.Fatalf("count = %d, want %d", n, total)
	}

	ids, err := s.ListSearchIndexDirty(500)
	if err != nil {
		t.Fatalf("ListSearchIndexDirty: %v", err)
	}
	if len(ids) != 500 {
		t.Fatalf("limited list returned %d, want 500", len(ids))
	}
	if ids[0] != "book-00000" {
		t.Fatalf("limited list should start at the head, got %s", ids[0])
	}

	// Draining the head then re-listing must advance, not repeat.
	for _, id := range ids {
		if err := s.ClearSearchIndexDirty(id); err != nil {
			t.Fatalf("clear %s: %v", id, err)
		}
	}
	next, err := s.ListSearchIndexDirty(500)
	if err != nil {
		t.Fatalf("second ListSearchIndexDirty: %v", err)
	}
	if next[0] != "book-00500" {
		t.Fatalf("after draining the head, next batch starts at %s, want book-00500", next[0])
	}
	if n, _ := s.CountSearchIndexDirty(); n != total-500 {
		t.Fatalf("count after draining 500: %d, want %d", n, total-500)
	}
}

func TestSearchIndexDirty_SurvivesReopen(t *testing.T) {
	// The whole reason the set is persisted rather than in-memory: a crash or
	// restart mid-bulk-operation must not lose the record of what to repair.
	dir := filepath.Join(t.TempDir(), "db")

	s, err := NewPebbleStore(dir)
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	for _, id := range []string{"survive-1", "survive-2"} {
		if err := s.MarkSearchIndexDirty(id); err != nil {
			t.Fatalf("mark: %v", err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := NewPebbleStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = reopened.Close() }()

	ids, err := reopened.ListSearchIndexDirty(0)
	if err != nil {
		t.Fatalf("list after reopen: %v", err)
	}
	if len(ids) != 2 || ids[0] != "survive-1" || ids[1] != "survive-2" {
		t.Fatalf("after reopen got %v, want [survive-1 survive-2]", ids)
	}
}

func TestSearchIndexDirty_EmptyIDIsIgnored(t *testing.T) {
	s := newDirtyTestStore(t)

	if err := s.MarkSearchIndexDirty(""); err != nil {
		t.Fatalf("marking empty ID should be a no-op, got %v", err)
	}
	if err := s.ClearSearchIndexDirty(""); err != nil {
		t.Fatalf("clearing empty ID should be a no-op, got %v", err)
	}
	// An empty ID must not create a key, or it would be drained forever:
	// GetBookByID("") returns nothing, so it could never be cleared.
	if n, _ := s.CountSearchIndexDirty(); n != 0 {
		t.Fatalf("empty ID created %d keys, want 0", n)
	}
}

func TestAsSearchIndexDirtyStore(t *testing.T) {
	s := newDirtyTestStore(t)

	if got := AsSearchIndexDirtyStore(s); got == nil {
		t.Fatal("AsSearchIndexDirtyStore(*PebbleStore) = nil, want non-nil")
	}
	if got := AsSearchIndexDirtyStore(nil); got != nil {
		t.Fatal("AsSearchIndexDirtyStore(nil) should be nil")
	}
	// A store that does not implement the capability must yield nil rather
	// than panicking — callers nil-check, per the AsBookmarkStore contract.
	if got := AsSearchIndexDirtyStore(struct{}{}); got != nil {
		t.Fatal("AsSearchIndexDirtyStore(non-store) should be nil")
	}
}
