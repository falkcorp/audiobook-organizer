// file: internal/server/registry_indexed_store_test.go
// version: 1.0.0
// guid: c4e1f0a2-7d3b-4f58-9a61-2b8e5d7c3f90
// last-edited: 2026-09-13

package server

import (
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/search"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

// Regression for search serving stale titles: NewServer handed the service
// registry the BARE store (Override("store", resolvedStore)) and only wrapped
// s.store in indexedStore later, in Start. Every registry-built service --
// audiobooks, metafetch, merge, every plugin op -- captured the bare store at
// Build time, so their UpdateBook calls never re-indexed.
//
// These tests build the server through NewServer and switch indexing live
// through startSearchIndexing, the same method Start calls, then write through
// the store the REGISTRY hands its services.

// indexedLayers counts indexedStore layers on the decorator chain of s. The
// correct answer is exactly 1: 0 means writes skip the index, 2 means every
// write is enqueued twice.
func indexedLayers(t *testing.T, s database.Store) int {
	t.Helper()
	n := 0
	for depth := 0; s != nil && depth < 16; depth++ {
		if _, ok := s.(*indexedStore); ok {
			n++
		}
		u, ok := s.(database.StoreUnwrapper)
		if !ok {
			break
		}
		s = u.Unwrap()
	}
	return n
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

	srv := NewServer(store)
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
	if n := indexedLayers(t, regStore); n != 1 {
		t.Fatalf("registry store has %d indexedStore layers, want exactly 1", n)
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
	if n := indexedLayers(t, srv.store); n != 1 {
		t.Fatalf("after startSearchIndexing the store has %d indexedStore layers, want exactly 1", n)
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

// Each write through the registry store must enqueue exactly one index event.
// No worker runs here, so the queue length is the number of enqueues.
func TestRegistryStore_IndexesExactlyOncePerWrite(t *testing.T) {
	srv, idx := newRegistryIndexTestServer(t)
	regStore := registryStore(t, srv)

	srv.setSearchIndex(idx)
	srv.indexQueue = make(chan indexRequest, 16)
	t.Cleanup(srv.closeIndexQueue)

	if _, err := regStore.CreateBook(&database.Book{
		ID: "b1", Title: "Once", FilePath: "/library/once.m4b", Format: "m4b",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := len(srv.indexQueue); got != 1 {
		t.Fatalf("after one CreateBook the queue holds %d events, want 1", got)
	}

	book, err := regStore.GetBookByID("b1")
	if err != nil || book == nil {
		t.Fatalf("get: %v", err)
	}
	book.Title = "Twice"
	if _, err := regStore.UpdateBook("b1", book); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got := len(srv.indexQueue); got != 2 {
		t.Fatalf("after CreateBook+UpdateBook the queue holds %d events, want 2", got)
	}

	// The same holds for the server's own store and the store Start installs
	// globally: all three are the one decorated object.
	if _, err := srv.store.UpdateBook("b1", book); err != nil {
		t.Fatalf("update via server store: %v", err)
	}
	if got := len(srv.indexQueue); got != 3 {
		t.Fatalf("after a third write the queue holds %d events, want 3", got)
	}
}

// Before Start creates the queue the decorator must forward writes untouched
// and enqueue nothing -- it is installed in NewServer, long before the Bleve
// index exists.
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
