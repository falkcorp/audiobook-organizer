// file: internal/server/indexed_store_test.go
// version: 1.3.1
// last-edited: 2026-08-30
// guid: 6e3f5a2b-8c5a-4a70-b8c5-3d7e0f1b9a89

package server

import (
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/search"
)

// drainQueue blocks until the worker has processed every in-flight
// request or the timeout fires. Test-only helper.
//
// Correctness: enqueueIndex increments indexWorkerBusy before adding
// to the channel; the worker decrements it after completing each item.
// Checking busy == 0 is therefore race-free regardless of where the
// worker is in its dequeue-process cycle.
func drainQueue(t *testing.T, srv *Server) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&srv.indexWorkerBusy) == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("index queue did not drain within timeout")
}

func TestIndexedStore_CreateReindexes(t *testing.T) {
	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	idx, err := search.Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatalf("bleve: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })

	srv := NewServer(store)
	srv.setSearchIndex(idx)
	srv.indexQueue = make(chan indexRequest, 32)
	done := make(chan struct{})
	go func() {
		srv.runIndexWorker()
		close(done)
	}()

	wrapped := &indexedStore{Store: store, server: srv}

	created, err := wrapped.CreateBook(&database.Book{
		ID: "b1", Title: "Search Target", FilePath: "/tmp/b1", Format: "m4b",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID != "b1" {
		t.Errorf("created ID = %q, want b1", created.ID)
	}

	drainQueue(t, srv)

	hits, _, err := idx.Search("title:search", 0, 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 || hits[0].BookID != "b1" {
		t.Errorf("after create, hits = %v, want [b1]", hits)
	}

	srv.closeIndexQueue()
	<-done
}

func TestIndexedStore_DeleteRemovesFromIndex(t *testing.T) {
	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	idx, err := search.Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatalf("bleve: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })

	srv := NewServer(store)
	srv.setSearchIndex(idx)
	srv.indexQueue = make(chan indexRequest, 32)
	done := make(chan struct{})
	go func() {
		srv.runIndexWorker()
		close(done)
	}()

	wrapped := &indexedStore{Store: store, server: srv}

	_, _ = wrapped.CreateBook(&database.Book{
		ID: "b1", Title: "Delete Me", FilePath: "/tmp/b1", Format: "m4b",
	})
	drainQueue(t, srv)

	if err := wrapped.DeleteBook("b1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	drainQueue(t, srv)

	hits, _, _ := idx.Search("title:delete", 0, 10)
	if len(hits) != 0 {
		t.Errorf("after delete, hits = %v, want empty", hits)
	}

	srv.closeIndexQueue()
	<-done
}

// TestIndexedStore_SoftDeleteIsUnsearchableWithoutReconcile proves the
// IMMEDIATE, synchronous-enqueue path — indexedStore.UpdateBook — removes a
// soft-deleted book from the search index by itself, with no
// reconcileSearchIndexCoverage or reconcileOnce pass run in between. That is
// a different surface than TestSearchCoverage_StaleDocsAreDeleted
// (search_coverage_test.go), which covers the periodic reconciler catching
// an ALREADY-stale doc; this test would still pass if the reconciler were
// deleted entirely.
//
// Mirrors internal/merge/service.go's SoftDeleteBook: set
// MarkedForDeletion=true and call store.UpdateBook — nothing else.
func TestIndexedStore_SoftDeleteIsUnsearchableWithoutReconcile(t *testing.T) {
	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	idx, err := search.Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatalf("bleve: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })

	srv := NewServer(store)
	srv.setSearchIndex(idx)
	srv.indexQueue = make(chan indexRequest, 32)
	done := make(chan struct{})
	go func() {
		srv.runIndexWorker()
		close(done)
	}()

	wrapped := &indexedStore{Store: store, server: srv}

	_, _ = wrapped.CreateBook(&database.Book{
		ID: "b1", Title: "Vanishing Treatise", FilePath: "/tmp/b1", Format: "m4b",
	})
	// Control book: never touched, must remain findable throughout. Without
	// this, an assertion that the deleted book's title yields 0 hits would
	// be vacuous — the query could return 0 hits for unrelated reasons (a
	// broken index, an empty index, a typo in the query).
	_, _ = wrapped.CreateBook(&database.Book{
		ID: "b2", Title: "Control Survivor", FilePath: "/tmp/b2", Format: "m4b",
	})
	drainQueue(t, srv)

	// Sanity: both books are findable before the soft-delete.
	hits, _, _ := idx.Search("title:vanishing", 0, 10)
	if len(hits) != 1 || hits[0].BookID != "b1" {
		t.Fatalf("before soft-delete, vanishing-title hits = %v, want [b1]", hits)
	}
	hits, _, _ = idx.Search("title:control", 0, 10)
	if len(hits) != 1 || hits[0].BookID != "b2" {
		t.Fatalf("before soft-delete, control-title hits = %v, want [b2]", hits)
	}

	// Soft-delete b1 exactly as SoftDeleteBook does: flip the flag, call
	// UpdateBook. No reconcileSearchIndexCoverage, no reconcileOnce.
	deleted := &database.Book{
		ID: "b1", Title: "Vanishing Treatise", FilePath: "/tmp/b1", Format: "m4b",
		MarkedForDeletion: boolPtr(true),
	}
	if _, err := wrapped.UpdateBook("b1", deleted); err != nil {
		t.Fatalf("soft-delete update: %v", err)
	}
	drainQueue(t, srv)

	// The soft-deleted book's title must return zero hits...
	hits, _, _ = idx.Search("title:vanishing", 0, 10)
	if len(hits) != 0 {
		t.Errorf("after soft-delete, vanishing-title hits = %v, want empty", hits)
	}
	// ...while the untouched control book is still findable in the same
	// index, proving the query mechanism itself still works and the empty
	// result above is not vacuous.
	hits, _, _ = idx.Search("title:control", 0, 10)
	if len(hits) != 1 || hits[0].BookID != "b2" {
		t.Errorf("after soft-delete, control-title hits = %v, want [b2]", hits)
	}

	srv.closeIndexQueue()
	<-done
}

// TestIndexedStoreUpdateBook_RestoreStillReindexes is the anti-over-
// suppression guard for the fix above: soft-delete then restore
// (MarkedForDeletion true->false) via UpdateBook must still land the book
// back in the search index. Without this test, a fix that always enqueues a
// delete on ANY MarkedForDeletion field touch (rather than checking its
// current value) would pass the delete-path test while silently breaking
// restore.
func TestIndexedStoreUpdateBook_RestoreStillReindexes(t *testing.T) {
	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	idx, err := search.Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatalf("bleve: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })

	srv := NewServer(store)
	srv.setSearchIndex(idx)
	srv.indexQueue = make(chan indexRequest, 32)
	done := make(chan struct{})
	go func() {
		srv.runIndexWorker()
		close(done)
	}()

	wrapped := &indexedStore{Store: store, server: srv}

	_, _ = wrapped.CreateBook(&database.Book{
		ID: "b1", Title: "Restorable Ledger", FilePath: "/tmp/b1", Format: "m4b",
	})
	drainQueue(t, srv)

	// Soft-delete.
	if _, err := wrapped.UpdateBook("b1", &database.Book{
		ID: "b1", Title: "Restorable Ledger", FilePath: "/tmp/b1", Format: "m4b",
		MarkedForDeletion: boolPtr(true),
	}); err != nil {
		t.Fatalf("soft-delete update: %v", err)
	}
	drainQueue(t, srv)

	hits, _, _ := idx.Search("title:restorable", 0, 10)
	if len(hits) != 0 {
		t.Fatalf("after soft-delete, hits = %v, want empty", hits)
	}

	// Restore.
	if _, err := wrapped.UpdateBook("b1", &database.Book{
		ID: "b1", Title: "Restorable Ledger", FilePath: "/tmp/b1", Format: "m4b",
		MarkedForDeletion: boolPtr(false),
	}); err != nil {
		t.Fatalf("restore update: %v", err)
	}
	drainQueue(t, srv)

	hits, _, _ = idx.Search("title:restorable", 0, 10)
	if len(hits) != 1 || hits[0].BookID != "b1" {
		t.Errorf("after restore, hits = %v, want [b1]", hits)
	}

	srv.closeIndexQueue()
	<-done
}

func TestIndexedStore_EnqueueSafeAfterClose(t *testing.T) {
	// Regression: closing the queue then calling enqueueIndex must
	// be safe (no panic on send-on-closed-channel).
	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	idx, err := search.Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatalf("bleve: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })

	srv := NewServer(store)
	srv.setSearchIndex(idx)
	srv.indexQueue = make(chan indexRequest, 32)
	done := make(chan struct{})
	go func() {
		srv.runIndexWorker()
		close(done)
	}()

	srv.closeIndexQueue()
	<-done

	// Concurrent enqueue calls after close should all no-op.
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Go(func() {
			srv.enqueueIndex("b1", false)
			srv.enqueueIndex("b2", true)
		})
	}
	wg.Wait()
	// If we got here without panicking, the test passes.
}

func TestIndexedStore_UpdateReindexes(t *testing.T) {
	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	idx, err := search.Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatalf("bleve: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })

	srv := NewServer(store)
	srv.setSearchIndex(idx)
	srv.indexQueue = make(chan indexRequest, 32)
	done := make(chan struct{})
	go func() {
		srv.runIndexWorker()
		close(done)
	}()

	wrapped := &indexedStore{Store: store, server: srv}

	_, _ = wrapped.CreateBook(&database.Book{
		ID: "b1", Title: "Original Title", FilePath: "/tmp/b1", Format: "m4b",
	})
	drainQueue(t, srv)

	// Update title.
	updated := &database.Book{ID: "b1", Title: "New Title", FilePath: "/tmp/b1", Format: "m4b"}
	if _, err := wrapped.UpdateBook("b1", updated); err != nil {
		t.Fatalf("update: %v", err)
	}
	drainQueue(t, srv)

	// Old title should no longer match.
	hits, _, _ := idx.Search("title:original", 0, 10)
	if len(hits) != 0 {
		t.Errorf("after update, old title still matches: %v", hits)
	}
	// New title should match.
	hits, _, _ = idx.Search("title:new", 0, 10)
	if len(hits) != 1 || hits[0].BookID != "b1" {
		t.Errorf("after update, new title hits = %v, want [b1]", hits)
	}

	srv.closeIndexQueue()
	<-done
}
