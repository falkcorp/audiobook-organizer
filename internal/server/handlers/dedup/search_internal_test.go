// file: internal/server/handlers/dedup/search_internal_test.go
// version: 1.0.0
// guid: 8b3d5e17-4a92-4c06-9f38-1d7b2e5a09c4
// last-edited: 2026-09-01

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
// bulk readers were called at all. A mock that merely returns empty slices
// could not tell "did not read the library" from "read it and found nothing".
type countingStore struct {
	DedupStore
	books       []database.BookCore
	authors     []database.Author
	booksErr    error
	authorsErr  error
	bookCalls   int
	authorCalls int
}

func (c *countingStore) GetAllBooksCore(limit, offset int) ([]database.BookCore, error) {
	c.bookCalls++
	return c.books, c.booksErr
}

func (c *countingStore) GetAllAuthors() ([]database.Author, error) {
	c.authorCalls++
	return c.authors, c.authorsErr
}

func TestResolveBookIDsMatchingEmptyNeedleNeverReadsTheLibrary(t *testing.T) {
	for _, needle := range []string{"", "   ", "\t\n"} {
		s := &countingStore{books: []database.BookCore{{ID: "b1", Title: "Dune"}}}
		got, err := resolveBookIDsMatching(s, needle)
		if err != nil {
			t.Fatalf("needle %q: unexpected error %v", needle, err)
		}
		if got != nil {
			t.Fatalf("needle %q: want nil set (means \"no search\"), got %v", needle, got)
		}
		// The point of the guard: a blank needle must not pay for a
		// full-library walk on every request that happens to carry one.
		if s.bookCalls != 0 || s.authorCalls != 0 {
			t.Fatalf("needle %q: read the library anyway (books=%d authors=%d)",
				needle, s.bookCalls, s.authorCalls)
		}
	}
}

func TestResolveBookIDsMatchingPropagatesErrors(t *testing.T) {
	t.Run("authors", func(t *testing.T) {
		s := &countingStore{authorsErr: errors.New("boom")}
		if _, err := resolveBookIDsMatching(s, "dune"); err == nil {
			t.Fatal("want error, got nil -- a swallowed error here degrades search silently")
		}
		if s.bookCalls != 0 {
			t.Fatal("must not read books after the author read failed")
		}
	})
	t.Run("books", func(t *testing.T) {
		s := &countingStore{booksErr: errors.New("boom")}
		if _, err := resolveBookIDsMatching(s, "dune"); err == nil {
			t.Fatal("want error, got nil")
		}
	})
}

func TestResolveBookIDsMatchingDanglingAuthorDoesNotPanic(t *testing.T) {
	seven := 7
	missing := 999
	s := &countingStore{
		books: []database.BookCore{
			{ID: "ok", Title: "x", AuthorID: &seven},
			// AuthorID pointing at a row GetAllAuthors does not return.
			// Production carries a documented population of these.
			{ID: "dangling", Title: "x", AuthorID: &missing},
			{ID: "none", Title: "x"},
		},
		authors: []database.Author{{ID: 7, Name: "Neil Gaiman"}},
	}
	got, err := resolveBookIDsMatching(s, "gaiman")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want exactly the resolvable author match, got %v", got)
	}
	if _, ok := got["ok"]; !ok {
		t.Fatalf("want book \"ok\", got %v", got)
	}
}
