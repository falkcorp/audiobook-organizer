// file: internal/server/search_reconciler_drain_test.go
// version: 1.0.0
// guid: 5b1e8d3c-7a2f-4e90-b6c4-9d0a2f7e1b38
// last-edited: 2026-09-25
//
// Drain, restart and live-write tests for the batched search reconciler
// (the 2026-09-25 wedge: a 100k dirty set that never drained and a live
// ModifyBook that never reached the index).

package server

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/search"
)

// openDiskPair opens an ON-DISK Pebble store and Bleve index at fixed paths,
// so a test can close both and reopen them as a restart would.
func openDiskPair(t *testing.T, dbPath, idxPath string) (*database.PebbleStore, *search.BleveIndex) {
	t.Helper()
	store, err := database.NewPebbleStore(dbPath)
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	idx, err := search.Open(idxPath)
	if err != nil {
		_ = store.Close()
		t.Fatalf("bleve: %v", err)
	}
	return store, idx
}

func dirtyCount(t *testing.T, store database.Store) int {
	t.Helper()
	ds := database.AsSearchIndexDirtyStore(store)
	if ds == nil {
		t.Fatal("store has no dirty set")
	}
	n, err := ds.CountSearchIndexDirty()
	if err != nil {
		t.Fatalf("count dirty: %v", err)
	}
	return n
}

func TestReconciler_DrainsLargeBacklogInBatchesAcrossRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("writes 1,200 books to an on-disk store")
	}
	dir := t.TempDir()
	dbPath, idxPath := filepath.Join(dir, "db"), filepath.Join(dir, "library.bleve")
	const total = 1200

	store, idx := openDiskPair(t, dbPath, idxPath)
	ds := database.AsSearchIndexDirtyStore(store)
	for i := range total {
		id := fmt.Sprintf("bk-%05d", i)
		if _, err := store.CreateBook(&database.Book{ID: id, Title: fmt.Sprintf("Drain Title %d", i), FilePath: "/tmp/" + id, Format: "m4b"}); err != nil {
			t.Fatalf("create: %v", err)
		}
		if err := ds.MarkSearchIndexDirty(id); err != nil {
			t.Fatalf("mark: %v", err)
		}
	}

	srv := NewServer(store)
	srv.setSearchIndex(idx)
	drained, remaining := srv.reconcileOnce()
	// 1,200/10 = 120 < floor, so one pass is exactly the floor.
	if drained != reconcileMinBatch || remaining != total-reconcileMinBatch {
		t.Fatalf("first pass drained=%d remaining=%d, want %d/%d", drained, remaining, reconcileMinBatch, total-reconcileMinBatch)
	}
	if n, _ := idx.DocCount(); n != reconcileMinBatch {
		t.Fatalf("index after first pass has %d docs, want %d", n, reconcileMinBatch)
	}

	// Restart: close everything without any drain-on-shutdown step.
	srv.bgCancel()
	if err := idx.Close(); err != nil {
		t.Fatalf("close index: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	store2, idx2 := openDiskPair(t, dbPath, idxPath)
	t.Cleanup(func() { _ = idx2.Close(); _ = store2.Close() })
	if n, _ := idx2.DocCount(); n != reconcileMinBatch {
		t.Fatalf("after restart the index has %d docs, want the %d the first pass committed", n, reconcileMinBatch)
	}
	if got := dirtyCount(t, store2); got != total-reconcileMinBatch {
		t.Fatalf("after restart the dirty set has %d keys, want %d", got, total-reconcileMinBatch)
	}

	srv2 := NewServer(store2)
	srv2.setSearchIndex(idx2)
	for pass := 0; pass < 10; pass++ {
		if _, rem := srv2.reconcileOnce(); rem == 0 {
			break
		}
	}
	if n, _ := idx2.DocCount(); n != total {
		t.Fatalf("after draining, index has %d docs, want %d", n, total)
	}
	if got := dirtyCount(t, store2); got != 0 {
		t.Fatalf("dirty set still has %d keys", got)
	}
}

func TestReconciler_RemovesSoftDeletedBook(t *testing.T) {
	srv, store, idx := newDropOnlyServer(t)
	if _, err := store.CreateBook(&database.Book{ID: "trashed", Title: "Trash Me", FilePath: "/tmp/t", Format: "m4b"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := idx.IndexBook(search.BookDocument{BookID: "trashed", Title: "Trash Me"}); err != nil {
		t.Fatalf("seed index: %v", err)
	}
	yes := true
	if _, err := store.UpdateBook("trashed", &database.Book{ID: "trashed", Title: "Trash Me", FilePath: "/tmp/t", Format: "m4b", MarkedForDeletion: &yes}); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	srv.markIndexDirty("trashed")
	srv.reconcileOnce()
	if hits, _, _ := idx.Search("title:trash", 0, 10); len(hits) != 0 {
		t.Fatalf("soft-deleted book is still searchable: %v", hits)
	}
}

func TestReconciler_MarksRebuiltOnlyAfterCoverageSeeded(t *testing.T) {
	srv, _, idx := newDropOnlyServer(t)
	if !idx.Rebuilding() {
		t.Fatal("fresh index should report rebuilding")
	}
	srv.reconcileOnce() // empty dirty set, coverage not yet seeded
	if !idx.Rebuilding() {
		t.Fatal("an empty dirty set before coverage seeding cleared the rebuilding marker")
	}
	srv.reconcileSearchIndexCoverage() // no books -> coverage OK -> seeded
	srv.reconcileOnce()
	if idx.Rebuilding() {
		t.Fatal("rebuilding marker not cleared after coverage seeded and the dirty set drained")
	}
}

// The live-write half of the 2026-09-25 report: a book retitled through
// ModifyBook on the decorated store must be searchable under its new title
// within seconds, via the running worker.
func TestIndexedStore_ModifyBookReachesIndex(t *testing.T) {
	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
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
	srv.indexQueue = make(chan indexRequest, 32)
	done := make(chan struct{})
	go func() { srv.runIndexWorker(); close(done) }()
	t.Cleanup(func() { srv.closeIndexQueue(); <-done })

	wrapped := &indexedStore{Store: store, server: srv}
	if _, err := wrapped.CreateBook(&database.Book{ID: "m1", Title: "Old Name", FilePath: "/tmp/m1", Format: "m4b"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := wrapped.ModifyBook("m1", func(b *database.Book) error {
		b.Title = "Arcane_Chef_2__A_LitRPG_Adventure"
		return nil
	}); err != nil {
		t.Fatalf("modify: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		hits, _, err := idx.Search("title:chef", 0, 10)
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if len(hits) == 1 && hits[0].BookID == "m1" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("ModifyBook's new title not searchable within 5s; hits=%v", hits)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if hits, _, _ := idx.Search("title:old", 0, 10); len(hits) != 0 {
		t.Fatalf("old title still matches after ModifyBook: %v", hits)
	}
}
