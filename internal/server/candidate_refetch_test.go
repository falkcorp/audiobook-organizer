// file: internal/server/candidate_refetch_test.go
// version: 1.0.0
// guid: 74a3c673-3586-4bc4-861e-c56db3fd0ab5
// last-edited: 2026-10-05

package server

import (
	"context"
	"testing"

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
