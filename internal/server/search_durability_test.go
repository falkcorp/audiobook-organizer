// file: internal/server/search_durability_test.go
// version: 1.0.0
// guid: 4b9e2d70-1c58-4f3a-a6d4-7e0b3c9f5a12
// last-edited: 2026-10-13

package server

import (
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/search"
)

// Tests that a write to a book is never left with neither a queued request
// nor a durable dirty mark. The index channel and the pending set are memory
// only; the dirty set is what survives a restart.

func dirtyIDs(t *testing.T, store database.Store) []string {
	t.Helper()
	ds := database.AsSearchIndexDirtyStore(store)
	if ds == nil {
		t.Fatal("store has no dirty set")
	}
	ids, err := ds.ListSearchIndexDirty(0)
	must(t, err)
	return ids
}

func clearDirty(t *testing.T, store database.Store) {
	t.Helper()
	ds := database.AsSearchIndexDirtyStore(store)
	for _, id := range dirtyIDs(t, store) {
		must(t, ds.ClearSearchIndexDirty(id))
	}
}

func setBookTitle(t *testing.T, s *database.PebbleStore, id, title string) {
	t.Helper()
	_, err := s.ModifyBook(id, func(b *database.Book) error { b.Title = title; return nil })
	must(t, err)
}

// The index worker wedges (it did in production, for hours, in September): the
// queue fills and every queued book keeps its pending mark. A later write to a
// queued book coalesces into the request that may never be popped, so it must
// be recorded durably; a restart (memory dropped) then repairs it from the
// dirty set and the index carries the book's final title.
func TestEnqueueIndex_CoalescedWriteOnWedgedQueueSurvivesRestart(t *testing.T) {
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

	// Phase 1: a healthy server indexes the book under its old title.
	srv1 := newTestServer(t, store)
	srv1.setSearchIndex(idx)
	srv1.indexQueue = make(chan indexRequest, 8)
	done := make(chan struct{})
	go func() { srv1.runIndexWorker(); close(done) }()
	must(t, createBook(store, "wedged-0", "Oldtitle Book"))
	drainQueue(t, srv1)
	srv1.closeIndexQueue()
	<-done
	if hits, _, _ := idx.Search("title:oldtitle", 0, 10); len(hits) != 1 {
		t.Fatalf("seed failed: old title hits = %v", hits)
	}
	clearDirty(t, store)

	// Phase 2: the worker is wedged (no worker runs) and the queue fills.
	srv2 := newTestServer(t, store)
	srv2.setSearchIndex(idx)
	srv2.indexQueue = make(chan indexRequest, 8)
	t.Cleanup(srv2.closeIndexQueue)
	setBookTitle(t, store, "wedged-0", "Firstedit Book") // queued, not popped
	for i := 1; i < 8; i++ {
		must(t, createBook(store, fmt.Sprintf("wedged-%d", i), "Filler"))
	}
	if got, want := len(srv2.indexQueue), cap(srv2.indexQueue); got != want {
		t.Fatalf("queue holds %d of %d; the test needs it full", got, want)
	}
	if ids := dirtyIDs(t, store); len(ids) != 0 {
		t.Fatalf("dirty set = %v before the coalesced write; the test needs it empty", ids)
	}
	setBookTitle(t, store, "wedged-0", "Finaledit Book") // coalesces into the stuck request
	if ids := dirtyIDs(t, store); !slices.Equal(ids, []string{"wedged-0"}) {
		t.Fatalf("a write coalesced into a full wedged queue was not recorded durably; dirty set = %v", ids)
	}

	// Phase 3: restart. Memory (queue, pending set) is gone; the dirty set is not.
	srv3 := newTestServer(t, store)
	srv3.setSearchIndex(idx)
	srv3.reconcileOnce()
	if hits, _, _ := idx.Search("title:finaledit", 0, 10); len(hits) != 1 || hits[0].BookID != "wedged-0" {
		t.Fatalf("after the restart the index does not carry the final title; hits = %v", hits)
	}
	if hits, _, _ := idx.Search("title:oldtitle", 0, 10); len(hits) != 0 {
		t.Fatalf("the old title is still indexed: %v", hits)
	}
}

// Coalescing on a queue that still has room records nothing durable: the
// pending request will be popped and will read the latest row.
func TestEnqueueIndex_CoalescedWriteOnRoomyQueueRecordsNothing(t *testing.T) {
	srv, store := newObserverServer(t)
	must(t, createBook(store, "roomy-0", "Roomy"))
	clearDirty(t, store)
	srv.indexQueue = make(chan indexRequest, 8)
	t.Cleanup(srv.closeIndexQueue)
	setBookTitle(t, store, "roomy-0", "Roomy One")
	setBookTitle(t, store, "roomy-0", "Roomy Two")
	if ids := dirtyIDs(t, store); len(ids) != 0 {
		t.Fatalf("coalescing on a queue with room marked %v dirty", ids)
	}
	if got := len(srv.indexQueue); got != 1 {
		t.Fatalf("queue holds %d requests, want 1", got)
	}
}

// Shutdown closes the queue while ops that outlived the registry's bounded
// shutdown keep writing; nothing will pop a request, so the write is recorded.
func TestEnqueueIndex_AfterQueueCloseMarksTheBookDirty(t *testing.T) {
	srv, store := newObserverServer(t)
	must(t, createBook(store, "closing-0", "Closing"))
	clearDirty(t, store)
	srv.indexQueue = make(chan indexRequest, 8)
	srv.closeIndexQueue()
	setBookTitle(t, store, "closing-0", "Closing Edited")
	if ids := dirtyIDs(t, store); !slices.Equal(ids, []string{"closing-0"}) {
		t.Fatalf("a write after the queue closed was not recorded; dirty set = %v", ids)
	}
}

// Whatever is still pending at Shutdown (the worker was wedged, or the wait for
// it timed out) is written to the dirty set.
func TestMarkPendingIndexDirty_RecordsEveryQueuedBook(t *testing.T) {
	srv, store := newObserverServer(t)
	for i := range 3 {
		must(t, createBook(store, fmt.Sprintf("pending-%d", i), "Pending"))
	}
	clearDirty(t, store)
	srv.indexQueue = make(chan indexRequest, 8)
	t.Cleanup(srv.closeIndexQueue)
	for i := range 3 {
		srv.enqueueIndex(fmt.Sprintf("pending-%d", i))
	}
	if ids := dirtyIDs(t, store); len(ids) != 0 {
		t.Fatalf("dirty set = %v before the shutdown snapshot", ids)
	}
	srv.markPendingIndexDirty()
	want := []string{"pending-0", "pending-1", "pending-2"}
	if ids := dirtyIDs(t, store); !slices.Equal(ids, want) {
		t.Fatalf("dirty set = %v, want %v", ids, want)
	}
}

// midReadStore runs hook once, right after the first batch read returns: the
// moment between a reconcile pass reading a row and finishing with its key.
type midReadStore struct {
	*database.PebbleStore
	once sync.Once
	hook func()
}

func (m *midReadStore) GetBooksByIDs(ids []string) ([]database.Book, error) {
	books, err := m.PebbleStore.GetBooksByIDs(ids)
	m.once.Do(m.hook)
	return books, err
}

// A write that lands after the reconciler has read a row but before it is done
// with the row's dirty key must not be erased: the pass indexed the OLD row, and
// nothing else would ever repair the newer write.
func TestReconciler_WriteBetweenReadAndClearStaysDirty(t *testing.T) {
	pebble, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { _ = pebble.Close() })
	idx, err := search.Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatalf("bleve: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })

	store := &midReadStore{PebbleStore: pebble}
	srv := newTestServer(t, store)
	srv.setSearchIndex(idx)
	srv.indexQueue = make(chan indexRequest) // always drops: every write marks its book dirty
	must(t, createBook(pebble, "race-0", "Beforewrite Book"))
	if ids := dirtyIDs(t, store); !slices.Equal(ids, []string{"race-0"}) {
		t.Fatalf("dirty set = %v, want [race-0]", ids)
	}
	store.hook = func() { setBookTitle(t, pebble, "race-0", "Afterwrite Book") }

	srv.reconcileOnce() // reads "Beforewrite", the hook rewrites the book, the pass finishes
	if ids := dirtyIDs(t, store); !slices.Equal(ids, []string{"race-0"}) {
		t.Fatalf("the write made during the pass was erased from the dirty set: %v", ids)
	}
	srv.reconcileOnce()
	if hits, _, _ := idx.Search("title:afterwrite", 0, 10); len(hits) != 1 || hits[0].BookID != "race-0" {
		t.Fatalf("the next pass did not index the newer title; hits = %v", hits)
	}
	if ids := dirtyIDs(t, store); len(ids) != 0 {
		t.Fatalf("dirty set = %v after the repairing pass", ids)
	}
}
