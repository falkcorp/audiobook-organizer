// file: internal/server/indexed_store_test.go
// version: 1.2.0
// last-edited: 2026-08-15
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
		wg.Add(1)
		go func() {
			defer wg.Done()
			srv.enqueueIndex("b1", false)
			srv.enqueueIndex("b2", true)
		}()
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
