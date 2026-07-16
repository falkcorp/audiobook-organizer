// file: internal/server/ai_author_reassign_test.go
// version: 1.0.0
// guid: 8b1d3f27-6c94-4a05-9e21-3d7f0a58c4b6
// last-edited: 2026-07-16

package server

import (
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// fakeReassignStore implements the narrow authorReassignStore surface so the
// test can inject read/write failures per book.
type fakeReassignStore struct {
	books      []database.BookCore
	booksErr   error
	authors    map[string][]database.BookAuthor
	authorsErr map[string]error
	setErr     map[string]error
	setCalls   map[string][]database.BookAuthor
}

func (f *fakeReassignStore) GetBooksByAuthorIDWithRoleCore(int) ([]database.BookCore, error) {
	return f.books, f.booksErr
}

func (f *fakeReassignStore) GetBookAuthors(bookID string) ([]database.BookAuthor, error) {
	if e := f.authorsErr[bookID]; e != nil {
		return nil, e
	}
	return f.authors[bookID], nil
}

func (f *fakeReassignStore) SetBookAuthors(bookID string, a []database.BookAuthor) error {
	if e := f.setErr[bookID]; e != nil {
		return e
	}
	if f.setCalls == nil {
		f.setCalls = map[string][]database.BookAuthor{}
	}
	f.setCalls[bookID] = a
	return nil
}

const mergeID, keepID = 2, 1

// Happy path: every book crediting mergeID is re-pointed to keepID; no errors,
// so the caller may delete mergeID.
func TestReassignBooksFromAuthor_AllSucceed(t *testing.T) {
	f := &fakeReassignStore{
		books: []database.BookCore{{ID: "b1"}, {ID: "b2"}},
		authors: map[string][]database.BookAuthor{
			"b1": {{BookID: "b1", AuthorID: mergeID, Role: "author"}},
			"b2": {{BookID: "b2", AuthorID: mergeID, Role: "author"}},
		},
	}
	errs := reassignBooksFromAuthor(f, mergeID, keepID)
	if len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
	for _, id := range []string{"b1", "b2"} {
		got := f.setCalls[id]
		if len(got) != 1 || got[0].AuthorID != keepID {
			t.Errorf("book %s: expected single author re-pointed to keepID %d, got %+v", id, keepID, got)
		}
	}
}

// A failed SetBookAuthors must surface as an error so the caller skips the
// author delete (prevents a dangling BookAuthor row).
func TestReassignBooksFromAuthor_SetFailureBlocksDelete(t *testing.T) {
	f := &fakeReassignStore{
		books: []database.BookCore{{ID: "b1"}, {ID: "b2"}},
		authors: map[string][]database.BookAuthor{
			"b1": {{BookID: "b1", AuthorID: mergeID}},
			"b2": {{BookID: "b2", AuthorID: mergeID}},
		},
		setErr: map[string]error{"b2": errors.New("write failed")},
	}
	errs := reassignBooksFromAuthor(f, mergeID, keepID)
	if len(errs) == 0 {
		t.Fatal("expected a non-empty error slice (must block author delete), got none")
	}
}

// A failed GetBookAuthors read must also block the delete — the prior code
// silently `continue`d, leaving the book crediting mergeID while deleting it.
func TestReassignBooksFromAuthor_AuthorsReadFailureBlocksDelete(t *testing.T) {
	f := &fakeReassignStore{
		books:      []database.BookCore{{ID: "b1"}},
		authorsErr: map[string]error{"b1": errors.New("read failed")},
	}
	errs := reassignBooksFromAuthor(f, mergeID, keepID)
	if len(errs) == 0 {
		t.Fatal("expected an error when a book's authors can't be read")
	}
}

// A failed book-list read blocks the delete too.
func TestReassignBooksFromAuthor_BookListFailureBlocksDelete(t *testing.T) {
	f := &fakeReassignStore{booksErr: errors.New("list failed")}
	errs := reassignBooksFromAuthor(f, mergeID, keepID)
	if len(errs) != 1 {
		t.Fatalf("expected exactly one error for a failed book list, got %v", errs)
	}
}

// A book already crediting keepID has the mergeID credit dropped (dedup), not
// duplicated — and this counts as success.
func TestReassignBooksFromAuthor_DedupWhenAlreadyCreditsKeep(t *testing.T) {
	f := &fakeReassignStore{
		books: []database.BookCore{{ID: "b1"}},
		authors: map[string][]database.BookAuthor{
			"b1": {
				{BookID: "b1", AuthorID: keepID, Role: "author"},
				{BookID: "b1", AuthorID: mergeID, Role: "co-author"},
			},
		},
	}
	errs := reassignBooksFromAuthor(f, mergeID, keepID)
	if len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
	got := f.setCalls["b1"]
	if len(got) != 1 || got[0].AuthorID != keepID {
		t.Errorf("expected the mergeID credit dropped, leaving only keepID, got %+v", got)
	}
}
