// file: internal/server/search_reconciler_test.go
// version: 1.0.0
// guid: 9a1e7c40-2b83-4f16-90ad-6c4b1f2e8d55
// last-edited: 2026-08-09
//
// Tests for search-index reconciliation after a dropped index event.
//
// The regression these defend against is not hypothetical: prod dropped
// 56,537 index operations in seven days with nothing repairing them,
// because the "startup reindex heals gaps" claim in three comments was
// false (buildSearchIndexIfEmpty only runs on an EMPTY index).
//
// Drops are forced deterministically with a zero-capacity indexQueue and no
// worker running: enqueueIndex's select then always takes the default
// branch. That is far more reliable than trying to saturate a 1024-deep
// channel from a test.

package server

import (
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/search"
)

// newDropOnlyServer returns a server whose index queue always drops, plus
// the underlying store and index. No worker goroutine is started, so every
// enqueueIndex call lands in the dirty set instead of the channel.
func newDropOnlyServer(t *testing.T) (*Server, *database.PebbleStore, *search.BleveIndex) {
	t.Helper()

	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	idx, err := search.Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatalf("bleve: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })

	srv := NewServer(store)
	srv.setSearchIndex(idx)
	// Zero capacity + no worker => the select always hits default.
	srv.indexQueue = make(chan indexRequest)

	return srv, store, idx
}

func TestNextBatchSize(t *testing.T) {
	tests := []struct {
		name    string
		backlog int
		want    int
	}{
		{"empty", 0, 0},
		{"negative is treated as empty", -5, 0},
		{"tiny backlog clears in one tick", 3, 3},
		{"below the floor clears entirely", 400, 400},
		{"at the floor", 500, 500},
		// 4,000/10 = 400, below the floor, so the floor wins.
		{"floor beats the proportional rate", 4000, reconcileMinBatch},
		// 20,000/10 = 2,000 — proportional range.
		{"proportional in the middle", 20000, 2000},
		// The measured prod backlog: 56,537/10 = 5,653, above the cap.
		{"prod bulk-day backlog is capped", 56537, reconcileMaxBatch},
		{"absurd backlog stays capped", 5000000, reconcileMaxBatch},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := nextBatchSize(tc.backlog); got != tc.want {
				t.Errorf("nextBatchSize(%d) = %d, want %d", tc.backlog, got, tc.want)
			}
		})
	}
}

func TestNextBatchSize_NeverExceedsBacklog(t *testing.T) {
	// Draining more than exists would make the reconciler log a batch it
	// cannot fill and mis-report "remaining".
	for _, backlog := range []int{1, 7, 99, 499, 500, 501, 4999, 5001, 60000} {
		if got := nextBatchSize(backlog); got > backlog {
			t.Errorf("nextBatchSize(%d) = %d, which exceeds the backlog", backlog, got)
		}
	}
}

func TestReconciler_RepairsDroppedUpsert(t *testing.T) {
	srv, store, idx := newDropOnlyServer(t)

	// Write straight to the store so the book exists in the DB but was
	// never indexed — exactly the post-drop state.
	if _, err := store.CreateBook(&database.Book{
		ID: "dropped-1", Title: "Reconcile Me", FilePath: "/tmp/d1", Format: "m4b",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	before := SearchIndexDroppedCount()
	srv.enqueueIndex("dropped-1", false)

	if got := SearchIndexDroppedCount() - before; got != 1 {
		t.Fatalf("drop counter advanced by %d, want 1", got)
	}

	// The index must NOT have it yet — that is the bug being reproduced.
	if hits, _, _ := idx.Search("title:reconcile", 0, 10); len(hits) != 0 {
		t.Fatalf("book was indexed despite the drop; test cannot prove a repair")
	}

	ds := database.AsSearchIndexDirtyStore(store)
	if ds == nil {
		t.Fatal("store does not expose SearchIndexDirtyStore")
	}
	ids, err := ds.ListSearchIndexDirty(0)
	if err != nil {
		t.Fatalf("list dirty: %v", err)
	}
	if len(ids) != 1 || ids[0] != "dropped-1" {
		t.Fatalf("dirty set = %v, want [dropped-1]", ids)
	}

	srv.reconcileOnce()

	hits, _, err := idx.Search("title:reconcile", 0, 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 || hits[0].BookID != "dropped-1" {
		t.Fatalf("after reconcile, hits = %v, want [dropped-1]", hits)
	}

	if n, _ := ds.CountSearchIndexDirty(); n != 0 {
		t.Fatalf("dirty set still has %d entries after a successful repair", n)
	}
}

func TestReconciler_RemovesBookDeletedWhileDropped(t *testing.T) {
	srv, store, idx := newDropOnlyServer(t)

	// Index it directly, so the index holds a row the DB no longer will.
	if _, err := store.CreateBook(&database.Book{
		ID: "gone-1", Title: "Vanishing Act", FilePath: "/tmp/g1", Format: "m4b",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := srv.IndexBookByID("gone-1"); err != nil {
		t.Fatalf("seed index: %v", err)
	}
	if hits, _, _ := idx.Search("title:vanishing", 0, 10); len(hits) != 1 {
		t.Fatal("seed failed: book not in index")
	}

	// Delete from the store, and drop the resulting index event.
	if err := store.DeleteBook("gone-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	srv.enqueueIndex("gone-1", true)

	// Still in the index — stale, which after filter pushdown would mean a
	// deleted book appearing in filtered results.
	if hits, _, _ := idx.Search("title:vanishing", 0, 10); len(hits) != 1 {
		t.Fatal("expected the stale index entry to still be present")
	}

	srv.reconcileOnce()

	if hits, _, _ := idx.Search("title:vanishing", 0, 10); len(hits) != 0 {
		t.Fatalf("after reconcile the deleted book is still indexed: %v", hits)
	}
}

// The reconciler re-derives upsert-vs-delete from the DB instead of
// replaying the recorded del flag. This proves it: the event is enqueued as
// a DELETE, but the book still exists, so the correct repair is a re-index,
// not a removal. Replaying the flag would wrongly evict a live book.
func TestReconciler_ReDerivesIntentFromDB(t *testing.T) {
	srv, store, idx := newDropOnlyServer(t)

	if _, err := store.CreateBook(&database.Book{
		ID: "alive-1", Title: "Still Here", FilePath: "/tmp/a1", Format: "m4b",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	// del=true, but the row is present.
	srv.enqueueIndex("alive-1", true)
	srv.reconcileOnce()

	hits, _, err := idx.Search("title:still", 0, 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 || hits[0].BookID != "alive-1" {
		t.Fatalf("a live book was evicted by replaying a stale delete flag; hits = %v", hits)
	}
}

func TestReconciler_DrainsRepeatedDropsOfOneBookOnce(t *testing.T) {
	srv, store, idx := newDropOnlyServer(t)

	if _, err := store.CreateBook(&database.Book{
		ID: "hot-1", Title: "Hot Book", FilePath: "/tmp/h1", Format: "m4b",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	// A book updated repeatedly during a bulk op drops many times; the set
	// must coalesce so the backlog figure is not inflated.
	for i := 0; i < 25; i++ {
		srv.enqueueIndex("hot-1", false)
	}

	ds := database.AsSearchIndexDirtyStore(store)
	if n, _ := ds.CountSearchIndexDirty(); n != 1 {
		t.Fatalf("25 drops of one book produced %d dirty entries, want 1", n)
	}

	srv.reconcileOnce()

	if hits, _, _ := idx.Search("title:hot", 0, 10); len(hits) != 1 {
		t.Fatal("book not repaired")
	}
	if n, _ := ds.CountSearchIndexDirty(); n != 0 {
		t.Fatalf("dirty set not drained: %d remaining", n)
	}
}

func TestReconciler_NoIndexIsANoOp(t *testing.T) {
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// No search index wired up: runSearchReconciler must return rather than
	// spin a ticker forever, and reconcileOnce must not panic.
	srv := NewServer(store)
	srv.runSearchReconciler() // returns immediately when searchIndex == nil
	srv.reconcileOnce()
}

func TestReconciler_EmptyDirtySetIsANoOp(t *testing.T) {
	srv, _, _ := newDropOnlyServer(t)
	// Must not error, log spuriously, or touch the index.
	srv.reconcileOnce()
}
