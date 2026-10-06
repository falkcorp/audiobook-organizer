// file: internal/server/candidate_refetch_test.go
// version: 1.0.0
// guid: 74a3c673-3586-4bc4-861e-c56db3fd0ab5
// last-edited: 2026-10-05

package server

import (
	"context"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// RefetchMetadataCandidates (the lost-candidates fixer's apply) asks the
// providers for a book with no cache row, stores what they return in the
// candidate cache, and writes nothing to the book: no field, no review
// status, no history.
func TestRefetchMetadataCandidates_FetchesIntoCacheOnly(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()
	store := s.storeForWiring()

	asin := "B00HOBBIT1"
	book, err := store.CreateBook(&database.Book{Title: "The Hobbit", FilePath: "/lib/hobbit/book.m4b", ASIN: &asin})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	src := &countingSource{name: "Finds", results: []metadata.BookMetadata{{Title: "The Hobbit", Author: "J.R.R. Tolkien"}}}
	mfs := metafetch.NewService(store)
	mfs.SetOverrideSources([]metadata.MetadataSource{src})
	s.metadataFetchService = mfs

	res, err := s.RefetchMetadataCandidates(context.Background(), book.ID)
	if err != nil {
		t.Fatalf("RefetchMetadataCandidates: %v", err)
	}
	if res.Status != "matched" || res.Candidates == 0 {
		t.Fatalf("result = %+v, want matched with candidates", res)
	}
	if src.calls.Load() == 0 {
		t.Fatal("no provider was asked")
	}
	entry, err := store.GetMetadataCache(book.ID)
	if err != nil || entry == nil || len(entry.Candidates) != res.Candidates {
		t.Fatalf("cache row = %+v (err %v), want %d candidates", entry, err, res.Candidates)
	}
	after, err := store.GetBookByID(book.ID)
	if err != nil {
		t.Fatalf("GetBookByID: %v", err)
	}
	if after.Title != "The Hobbit" || after.MetadataReviewStatus != nil || after.ASIN == nil || *after.ASIN != asin {
		t.Fatalf("book changed: title %q status %v asin %v", after.Title, after.MetadataReviewStatus, after.ASIN)
	}
	hist, err := store.GetBookChangeHistory(book.ID, 10)
	if err != nil || len(hist) != 0 {
		t.Fatalf("metadata history = %+v (err %v), want none", hist, err)
	}
}

// A refetch the providers answer with nothing still records the search: the
// cache row exists afterwards and is dated, so the lost-candidates fixer's
// next plan skips the book as searched_since instead of refetching it on
// every run.
func TestRefetchMetadataCandidates_EmptyAnswerIsRecorded(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()
	store := s.storeForWiring()
	book, err := store.CreateBook(&database.Book{Title: "A Book Nobody Catalogued", FilePath: "/lib/nobody/book.m4b"})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	mfs := metafetch.NewService(store)
	mfs.SetOverrideSources([]metadata.MetadataSource{&countingSource{name: "Empty"}})
	s.metadataFetchService = mfs

	before := time.Now().Add(-time.Second)
	res, err := s.RefetchMetadataCandidates(context.Background(), book.ID)
	if err != nil {
		t.Fatalf("RefetchMetadataCandidates: %v", err)
	}
	if res.Candidates != 0 || res.Status != "no_match" {
		t.Fatalf("result = %+v, want no_match with no candidates", res)
	}
	entry, err := store.GetMetadataCache(book.ID)
	if err != nil || entry == nil {
		t.Fatalf("cache row = %+v (err %v), want a row recording the empty search", entry, err)
	}
	dated := entry.FetchedAt.After(before) || (entry.LastEmptyFetchAt != nil && entry.LastEmptyFetchAt.After(before))
	if !dated {
		t.Fatalf("cache row not dated by the refetch: fetched %v, last empty %v", entry.FetchedAt, entry.LastEmptyFetchAt)
	}
}
