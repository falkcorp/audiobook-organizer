// file: internal/server/registry_search_observer_test.go
// version: 2.0.0
// guid: c4e1f0a2-7d3b-4f58-9a61-2b8e5d7c3f90
// last-edited: 2026-10-13

package server

import (
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/search"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

// Regression for search serving stale titles: NewServer handed the service
// registry the BARE store (Override("store", ...)) and only installed the
// search-indexing decorator on s.store later, in Start. Every registry-built
// service -- audiobooks, metafetch, merge, every plugin op -- captured the bare
// store at Build time, so their UpdateBook calls never re-indexed.
//
// Indexing is now an observer installed on the store itself (NewServer,
// installSearchObserver), so there is no wrapper to hold the wrong side of: the
// registry, the server and the global store are one raw store, and every write
// through any of them reaches the index.
//
// These tests build the server through NewServer and switch indexing live
// through startSearchIndexing, the same method Start calls, then write through
// the store the REGISTRY hands its services.

// isRawStore reports whether s is not a decorator: it exposes no Unwrap, so a
// plain type assertion sees everything the store implements.
func isRawStore(s database.Store) bool {
	_, wrapped := s.(database.StoreUnwrapper)
	return !wrapped
}

// queuedIDs drains the recording queue (no worker runs in the tests that use
// it) and returns the book IDs that were enqueued, in order. Like the worker it
// lifts each ID's pending mark, so a later write to the same book queues again.
func queuedIDs(srv *Server) []string {
	var ids []string
	for {
		select {
		case r := <-srv.indexQueue:
			srv.indexRequestPopped(r.bookID) // what the worker does on a pop
			ids = append(ids, r.bookID)
		default:
			return ids
		}
	}
}

func newRegistryIndexTestServer(t *testing.T) (*Server, *search.BleveIndex) {
	t.Helper()
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

	prevGlobal := database.GetGlobalStore()
	t.Cleanup(func() { database.SetGlobalStore(prevGlobal) })

	srv := newTestServer(t, store)
	return srv, idx
}

func registryStore(t *testing.T, srv *Server) database.Store {
	t.Helper()
	if srv.container == nil {
		t.Fatal("NewServer did not build a service registry container")
	}
	st, ok := serviceregistry.TryGet[database.Store](srv.container, serviceregistry.KeyStore)
	if !ok || st == nil {
		t.Fatal("registry has no store under KeyStore")
	}
	return st
}

func searchIDs(t *testing.T, idx *search.BleveIndex, q string) []string {
	t.Helper()
	hits, _, err := idx.Search(q, 0, 10)
	if err != nil {
		t.Fatalf("search %q: %v", q, err)
	}
	ids := make([]string, 0, len(hits))
	for _, h := range hits {
		ids = append(ids, h.BookID)
	}
	return ids
}

func TestRegistryStore_UpdateBookReindexesTitle(t *testing.T) {
	srv, idx := newRegistryIndexTestServer(t)
	regStore := registryStore(t, srv)

	// The registry, the server and (after Start) the global store must be one
	// object: any copy is a store some component writes through un-indexed.
	if regStore != srv.store {
		t.Fatalf("registry store %T is not the server's store %T", regStore, srv.store)
	}
	if !isRawStore(regStore) {
		t.Fatalf("registry store %T is a decorator; the server's store must be the raw store", regStore)
	}

	srv.setSearchIndex(idx)
	srv.startSearchIndexing()
	t.Cleanup(func() {
		srv.bgCancel()
		srv.closeIndexQueue()
		srv.bgWG.Wait()
	})

	if got := database.GetGlobalStore(); got != srv.store {
		t.Fatalf("global store %T is not the server's store after startSearchIndexing", got)
	}
	if !isRawStore(srv.store) {
		t.Fatalf("after startSearchIndexing the store %T is a decorator", srv.store)
	}

	if _, err := regStore.CreateBook(&database.Book{
		ID: "b1", Title: "Original Heading", FilePath: "/library/b1.m4b", Format: "m4b",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	drainQueue(t, srv)
	if ids := searchIDs(t, idx, "title:original"); len(ids) != 1 || ids[0] != "b1" {
		t.Fatalf("after create, title:original hits = %v, want [b1]", ids)
	}

	book, err := regStore.GetBookByID("b1")
	if err != nil || book == nil {
		t.Fatalf("get: %v", err)
	}
	book.Title = "Zebra Quantum"
	if _, err := regStore.UpdateBook("b1", book); err != nil {
		t.Fatalf("update: %v", err)
	}
	drainQueue(t, srv)

	if ids := searchIDs(t, idx, "title:zebra"); len(ids) != 1 || ids[0] != "b1" {
		t.Errorf("after UpdateBook through the registry store, title:zebra hits = %v, want [b1]", ids)
	}
	if ids := searchIDs(t, idx, "title:original"); len(ids) != 0 {
		t.Errorf("after UpdateBook, the stale title still matches: title:original hits = %v, want none", ids)
	}
}

// Each write through the registry store must enqueue an index event for that
// book, and for no other. No worker runs here, so the queue holds every
// enqueue. A write can enqueue more than once (a book-row write and a junction
// write each report it); the worker de-duplicates a batch by book ID, so the
// assertion is "at least one, only for this book", not an exact count.
func TestRegistryStore_EveryWriteReachesTheQueue(t *testing.T) {
	srv, idx := newRegistryIndexTestServer(t)
	regStore := registryStore(t, srv)

	srv.setSearchIndex(idx)
	srv.indexQueue = make(chan indexRequest, 16)
	t.Cleanup(srv.closeIndexQueue)

	onlyB1 := func(step string) {
		t.Helper()
		ids := queuedIDs(srv)
		if len(ids) == 0 {
			t.Fatalf("%s: nothing was enqueued", step)
		}
		for _, id := range ids {
			if id != "b1" {
				t.Fatalf("%s: enqueued %q, want only b1 (all: %v)", step, id, ids)
			}
		}
	}

	if _, err := regStore.CreateBook(&database.Book{
		ID: "b1", Title: "Once", FilePath: "/library/once.m4b", Format: "m4b",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	onlyB1("CreateBook")

	book, err := regStore.GetBookByID("b1")
	if err != nil || book == nil {
		t.Fatalf("get: %v", err)
	}
	book.Title = "Twice"
	if _, err := regStore.UpdateBook("b1", book); err != nil {
		t.Fatalf("update: %v", err)
	}
	onlyB1("UpdateBook")

	// The same holds for the server's own store and the store Start installs
	// globally: all three are the one object.
	if _, err := srv.store.UpdateBook("b1", book); err != nil {
		t.Fatalf("update via server store: %v", err)
	}
	onlyB1("UpdateBook via the server store")
}

// Before Start creates the queue the observer must enqueue nothing -- it is
// installed in NewServer, long before the Bleve index exists -- and the store
// must still take the write.
func TestRegistryStore_InertBeforeStart(t *testing.T) {
	srv, _ := newRegistryIndexTestServer(t)
	regStore := registryStore(t, srv)

	if _, err := regStore.CreateBook(&database.Book{
		ID: "b1", Title: "Early", FilePath: "/library/early.m4b", Format: "m4b",
	}); err != nil {
		t.Fatalf("create before Start: %v", err)
	}
	if srv.indexQueue != nil {
		t.Fatalf("index queue exists before Start")
	}
	if got, err := regStore.GetBookByID("b1"); err != nil || got == nil || got.Title != "Early" {
		t.Fatalf("write before Start did not reach the inner store: book=%v err=%v", got, err)
	}
}
