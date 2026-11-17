// file: internal/server/metadata_bulk_fetch_retired_test.go
// version: 1.0.0
// guid: 5e2c8a14-9b73-4d06-a3f1-6c0d7b9e2a48
// last-edited: 2026-11-17

package server

import (
	"context"
	"strings"
	"sync"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
)

// A retired book (in the trash, or merged into another book) takes no provider
// call from the bulk metadata fetch: no apply will accept what the search
// finds. It is left out of the work list (no walk of the source chain, so no
// ledger row), and the summary counts it as a retired skip. A live book in the
// same selection is still fetched.

func TestBulkFetchByIDs_SkipsRetiredBooks(t *testing.T) {
	disableMetadataSourcesForTest(t)

	books := map[string]*database.Book{
		"b-trashed": {ID: "b-trashed", Title: "Year One", MarkedForDeletion: new(true)},
		"b-merged":  {ID: "b-merged", Title: "The Killing Joke", MergedIntoBookID: new("b-plain")},
		"b-plain":   {ID: "b-plain", Title: "Knightfall"},
	}
	store, _, _ := newFastpathMockStore(books, nil)
	var ledgerMu sync.Mutex
	ledger := map[string]string{}
	store.CreateOperationResultFunc = func(r *database.OperationResult) error {
		ledgerMu.Lock()
		ledger[r.BookID] = r.Status
		ledgerMu.Unlock()
		return nil
	}
	srv := &Server{store: store, metadataFetchService: metafetch.NewService(store)}
	rec := &recordingProgress{}

	if _, err := srv.runBulkMetadataFetchForBookIDs(context.Background(), "op-bulk-retired",
		[]string{"b-trashed", "b-merged", "b-plain"}, operations.BulkMetadataFetchParams{}, store, rec); err != nil {
		t.Fatalf("run: %v", err)
	}

	for _, id := range []string{"b-trashed", "b-merged"} {
		if st, ok := ledger[id]; ok {
			t.Errorf("retired book %s was fetched: ledger = %q", id, st)
		}
	}
	if got := ledger["b-plain"]; got != metafetch.FetchStatusNotFound {
		t.Errorf("ledger[b-plain] = %q, want %q (the live book must still be fetched)", got, metafetch.FetchStatusNotFound)
	}
	if !strings.Contains(rec.last(), "retired 2") {
		t.Errorf("final summary %q does not report the retired count", rec.last())
	}
}

func TestBulkFetchAll_SkipsRetiredBooks(t *testing.T) {
	disableMetadataSourcesForTest(t)

	store := &database.MockStore{
		GetAllBooksCoreFunc: func(_, _ int) ([]database.BookCore, error) {
			return []database.BookCore{
				{ID: "b1", Title: "Year One", MarkedForDeletion: new(true)},
				{ID: "b2", Title: "The Killing Joke", MergedIntoBookID: new("b3")},
			}, nil
		},
		GetAllAuthorsFunc:       func() ([]database.Author, error) { return nil, nil },
		GetOperationResultsFunc: func(string) ([]database.OperationResult, error) { return nil, nil },
	}
	srv := &Server{store: store, metadataFetchService: metafetch.NewService(store)}
	rec := &recordingProgress{}

	// An already-cancelled context: with the skip the work list is empty, so
	// the run takes the "all books already cached" exit before it looks at the
	// context; without it the retired books reach the worker pool and the run
	// returns context.Canceled.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	incomplete, err := srv.runBulkMetadataFetchAll(ctx, "op-all-retired", operations.BulkMetadataFetchParams{}, store, rec)
	if err != nil || incomplete {
		t.Fatalf("run = (%v, %v), want (false, nil): retired books must never reach the worker pool", incomplete, err)
	}
	if !strings.Contains(rec.last(), "retired 2") {
		t.Errorf("final summary %q does not report the retired count", rec.last())
	}
}
