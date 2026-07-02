// file: internal/server/metadata_ops_fastpath_test.go
// version: 1.1.0
// guid: 7d6c8a2e-9f3b-4a1d-8e5c-2b6f4a9d1c3e
// last-edited: 2026-07-01

// Package server tests for TASK-02 (MAYDEPLOY-H5): runBulkMetadataFetchForBookIDs
// should resolve authors per-book via GetAuthorByID when len(bookIDs) < 100,
// and keep using the GetAllAuthors()-backed map for len(bookIDs) >= 100.
//
// config.AppConfig.MetadataSources is emptied for these tests so
// BuildSourceChain() returns no sources — the fetch loop then does no real
// (network-bound) searching and every book resolves to "not_found"
// deterministically. That keeps the tests hermetic and fast while still
// exercising the exact author-resolution branch under test: call counts and
// the specific author IDs requested from the mock store prove which code
// path ran and that it resolved the correct author for each book.
package server

import (
	"context"
	"fmt"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
)

// fastpathNoopProgress is a minimal operations.ProgressReporter that discards
// all reporting calls — sufficient for exercising runBulkMetadataFetchForBookIDs
// without a real op-registry.
type fastpathNoopProgress struct{}

func (fastpathNoopProgress) UpdateProgress(_, _ int, _ string) error { return nil }
func (fastpathNoopProgress) Log(_, _ string, _ *string) error        { return nil }
func (fastpathNoopProgress) IsCanceled() bool                        { return false }

// disableMetadataSourcesForTest clears config.AppConfig.MetadataSources so
// BuildSourceChain() returns an empty chain and runBulkMetadataFetchForBookIDs
// never makes a real network call — every book deterministically resolves to
// "not_found" without exercising any HTTP client.
func disableMetadataSourcesForTest(t *testing.T) {
	t.Helper()
	orig := config.AppConfig.MetadataSources
	config.AppConfig.MetadataSources = nil
	t.Cleanup(func() { config.AppConfig.MetadataSources = orig })
}

// newFastpathMockStore builds a MockStore over the given books/authors and
// counts + records calls to GetAllAuthors / GetAuthorByID so tests can assert
// both which read pattern was used and that it was given the right author ID.
func newFastpathMockStore(books map[string]*database.Book, authors map[int]*database.Author) (store *database.MockStore, getAllAuthorsCalls *int, getAuthorByIDs *[]int) {
	getAllAuthorsCalls = new(int)
	getAuthorByIDs = new([]int)
	store = &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			b, ok := books[id]
			if !ok {
				return nil, nil
			}
			return b, nil
		},
		GetAllAuthorsFunc: func() ([]database.Author, error) {
			*getAllAuthorsCalls++
			var out []database.Author
			for _, a := range authors {
				out = append(out, *a)
			}
			return out, nil
		},
		GetAuthorByIDFunc: func(id int) (*database.Author, error) {
			*getAuthorByIDs = append(*getAuthorByIDs, id)
			a, ok := authors[id]
			if !ok {
				return nil, nil
			}
			return a, nil
		},
	}
	return store, getAllAuthorsCalls, getAuthorByIDs
}

func containsInt(haystack []int, needle int) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}

func TestRunBulkMetadataFetchForBookIDs_FastPath_UnderThreshold(t *testing.T) {
	disableMetadataSourcesForTest(t)

	authorOne := &database.Author{ID: 1, Name: "Author One"}
	authorTwo := &database.Author{ID: 2, Name: "Author Two"}
	books := map[string]*database.Book{
		"b1": {ID: "b1", Title: "Book One", AuthorID: &authorOne.ID},
		"b2": {ID: "b2", Title: "Book Two", AuthorID: &authorTwo.ID},
		"b3": {ID: "b3", Title: "Book Three"}, // AuthorID == nil
	}
	authors := map[int]*database.Author{1: authorOne, 2: authorTwo}

	store, allAuthorsCalls, authorByIDCalls := newFastpathMockStore(books, authors)
	mfs := metafetch.NewService(store)
	srv := &Server{store: store, metadataFetchService: mfs}

	err := srv.runBulkMetadataFetchForBookIDs(
		context.Background(),
		"op-fastpath-under",
		[]string{"b1", "b2", "b3"},
		operations.BulkMetadataFetchParams{},
		store,
		fastpathNoopProgress{},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if *allAuthorsCalls != 0 {
		t.Errorf("expected GetAllAuthors NOT to be called for len(bookIDs)<100, got %d calls", *allAuthorsCalls)
	}
	// Two books have a non-nil AuthorID, so GetAuthorByID should be called
	// exactly twice — once per resolvable book — with exactly those IDs. The
	// nil-AuthorID book never triggers a lookup.
	if len(*authorByIDCalls) != 2 {
		t.Fatalf("expected GetAuthorByID to be called exactly twice, got %d calls: %v", len(*authorByIDCalls), *authorByIDCalls)
	}
	if !containsInt(*authorByIDCalls, 1) {
		t.Errorf("expected GetAuthorByID(1) to be called for Book One, calls=%v", *authorByIDCalls)
	}
	if !containsInt(*authorByIDCalls, 2) {
		t.Errorf("expected GetAuthorByID(2) to be called for Book Two, calls=%v", *authorByIDCalls)
	}
}

func TestRunBulkMetadataFetchForBookIDs_MapPath_AtThreshold(t *testing.T) {
	disableMetadataSourcesForTest(t)

	const n = 100
	books := make(map[string]*database.Book, n)
	authors := make(map[int]*database.Author, n)
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("book-%03d", i)
		authorID := i
		authors[authorID] = &database.Author{ID: authorID, Name: fmt.Sprintf("Author %03d", i)}
		books[id] = &database.Book{ID: id, Title: fmt.Sprintf("Title %03d", i), AuthorID: &authorID}
		ids = append(ids, id)
	}

	store, allAuthorsCalls, authorByIDCalls := newFastpathMockStore(books, authors)
	mfs := metafetch.NewService(store)
	srv := &Server{store: store, metadataFetchService: mfs}

	err := srv.runBulkMetadataFetchForBookIDs(
		context.Background(),
		"op-fastpath-at-threshold",
		ids,
		operations.BulkMetadataFetchParams{},
		store,
		fastpathNoopProgress{},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if *allAuthorsCalls != 1 {
		t.Errorf("expected GetAllAuthors to be called exactly once for len(bookIDs)>=100, got %d calls", *allAuthorsCalls)
	}
	if len(*authorByIDCalls) != 0 {
		t.Errorf("expected GetAuthorByID NOT to be called for len(bookIDs)>=100, got %d calls: %v", len(*authorByIDCalls), *authorByIDCalls)
	}
}

func TestRunBulkMetadataFetchForBookIDs_NilAuthorID_BothBranches(t *testing.T) {
	disableMetadataSourcesForTest(t)

	book := &database.Book{ID: "solo", Title: "Solo Book"} // AuthorID == nil
	books := map[string]*database.Book{"solo": book}
	authors := map[int]*database.Author{}

	store, allAuthorsCalls, authorByIDCalls := newFastpathMockStore(books, authors)
	mfs := metafetch.NewService(store)
	srv := &Server{store: store, metadataFetchService: mfs}

	err := srv.runBulkMetadataFetchForBookIDs(
		context.Background(),
		"op-fastpath-nil-author",
		[]string{"solo"},
		operations.BulkMetadataFetchParams{},
		store,
		fastpathNoopProgress{},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// len(bookIDs)=1 < 100, so this exercises the fast path, but the book has
	// AuthorID == nil so neither resolution mechanism should ever be invoked.
	if *allAuthorsCalls != 0 {
		t.Errorf("expected GetAllAuthors NOT to be called, got %d calls", *allAuthorsCalls)
	}
	if len(*authorByIDCalls) != 0 {
		t.Errorf("expected GetAuthorByID NOT to be called for a book with nil AuthorID, got %d calls: %v", len(*authorByIDCalls), *authorByIDCalls)
	}
}
