// file: internal/server/handlers/dedup/search_internal_test.go
// version: 1.1.0
// guid: 8b3d5e17-4a92-4c06-9f38-1d7b2e5a09c4
// last-edited: 2026-10-30

// In-package tests for resolveBookIDsMatching. The HTTP-level tests in
// search_test.go cannot reach its empty-needle guard, because the handler
// refuses a blank q before calling it -- so that guard is only observable from
// inside the package, and without this file a mutant deleting it survives.

package deduphandler

import (
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// countingStore implements just enough of DedupStore to observe whether the
// projection was called at all. A mock that merely returns an empty slice could
// not tell "did not read the library" from "read it and found nothing".
type countingStore struct {
	DedupStore
	rows     []database.BookSearchRow
	rowsErr  error
	rowCalls int
}

func (c *countingStore) ListBookSearchRows() ([]database.BookSearchRow, error) {
	c.rowCalls++
	return c.rows, c.rowsErr
}

func TestResolveBookIDsMatchingEmptyNeedleNeverReadsTheLibrary(t *testing.T) {
	for _, needle := range []string{"", "   ", "\t\n"} {
		s := &countingStore{rows: []database.BookSearchRow{{ID: "b1", Title: "Dune"}}}
		got, err := resolveBookIDsMatching(s, needle)
		if err != nil {
			t.Fatalf("needle %q: unexpected error %v", needle, err)
		}
		if got != nil {
			t.Fatalf("needle %q: want nil set (means \"no search\"), got %v", needle, got)
		}
		// The point of the guard: a blank needle must not pay for a
		// full-library walk on every request that happens to carry one.
		if s.rowCalls != 0 {
			t.Fatalf("needle %q: read the library anyway (projection calls=%d)",
				needle, s.rowCalls)
		}
	}
}

func TestResolveBookIDsMatchingPropagatesErrors(t *testing.T) {
	s := &countingStore{rowsErr: errors.New("boom")}
	if _, err := resolveBookIDsMatching(s, "dune"); err == nil {
		t.Fatal("want error, got nil -- a swallowed error here degrades search silently")
	}
}

// A book whose AuthorID resolves to nothing arrives from the store with an
// empty AuthorName; it must neither match nor panic, and a book with a real
// author name must still match on it.
func TestResolveBookIDsMatchingEmptyAuthorNameDoesNotMatch(t *testing.T) {
	s := &countingStore{rows: []database.BookSearchRow{
		{ID: "ok", Title: "x", AuthorName: "Bruce WAYNE"},
		{ID: "dangling", Title: "x"},
	}}
	got, err := resolveBookIDsMatching(s, "wayne")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want exactly the author match, got %v", got)
	}
	if _, ok := got["ok"]; !ok {
		t.Fatalf("want book \"ok\", got %v", got)
	}
}
