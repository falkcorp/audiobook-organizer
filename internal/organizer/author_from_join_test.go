// file: internal/organizer/author_from_join_test.go
// version: 1.1.0
// guid: 7c1e2d94-6a3f-4b81-9d20-3e5a8c7f1b64
// last-edited: 2026-09-05

package organizer

import (
	"fmt"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/authorname"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// intp is a small helper for the *int scalar AuthorID.
func intp(i int) *int { return &i }

// authorJoinStore is a hand-wired OrganizerStore that lets each test decide
// exactly what the join table and the scalar author resolution return, so the
// precedence between them can be observed in isolation.
type authorJoinStore struct {
	joins       map[string][]database.BookAuthor
	joinErr     error
	authorsByID map[int]*database.Author
	authorErr   error
}

func (s *authorJoinStore) GetBookAuthors(bookID string) ([]database.BookAuthor, error) {
	if s.joinErr != nil {
		return nil, s.joinErr
	}
	return s.joins[bookID], nil
}
func (s *authorJoinStore) GetAuthorByID(id int) (*database.Author, error) {
	if s.authorErr != nil {
		return nil, s.authorErr
	}
	return s.authorsByID[id], nil
}
func (s *authorJoinStore) GetSeriesByID(int) (*database.Series, error)      { return nil, nil }
func (s *authorJoinStore) GetBookByFileHash(string) (*database.Book, error) { return nil, nil }
func (s *authorJoinStore) GetBookByFilePath(string) (*database.Book, error) { return nil, nil }

// The core Part-3 guarantee: an applied-then-rescanned book carries the correct
// author only in the join table; the scalar AuthorID (and book.Author, hydrated
// from it) has reverted to the file's tag author. resolveAuthorName must return
// the JOIN author, not the reverted scalar.
func TestResolveAuthorName_JoinBeatsRevertedScalar(t *testing.T) {
	o := &Organizer{}
	o.SetStore(&authorJoinStore{
		joins: map[string][]database.BookAuthor{
			"b1": {{BookID: "b1", AuthorID: 100, Position: 0}},
		},
		authorsByID: map[int]*database.Author{
			100: {ID: 100, Name: "Applied Author"}, // the user's applied author
			200: {ID: 200, Name: "Tag Author"},     // what the rescan reverted to
		},
	})
	book := &database.Book{
		ID:       "b1",
		AuthorID: intp(200),                                     // reverted scalar
		Author:   &database.Author{ID: 200, Name: "Tag Author"}, // hydrated from scalar
	}
	if got := o.resolveAuthorName(book); got != "Applied Author" {
		t.Fatalf("resolveAuthorName = %q, want the join author %q", got, "Applied Author")
	}
}

// The lowest Position is the primary author. A co-author at position 1 (or, for
// organizer-copied books, another position-0 row appended later) must not win.
func TestResolveAuthorName_JoinPrimaryIsLowestPosition(t *testing.T) {
	o := &Organizer{}
	o.SetStore(&authorJoinStore{
		joins: map[string][]database.BookAuthor{
			"b1": {
				{BookID: "b1", AuthorID: 2, Position: 1}, // co-author listed first
				{BookID: "b1", AuthorID: 1, Position: 0}, // primary
			},
		},
		authorsByID: map[int]*database.Author{
			1: {ID: 1, Name: "Primary"},
			2: {ID: 2, Name: "CoAuthor"},
		},
	})
	book := &database.Book{ID: "b1"}
	if got := o.resolveAuthorName(book); got != "Primary" {
		t.Fatalf("resolveAuthorName = %q, want %q", got, "Primary")
	}
}

// The load-bearing tie-break: organizer-copied books have EVERY join row at
// Position 0 (service.go copies AuthorID+Role but not Position), so lowest-
// Position alone cannot pick a primary -- the code relies on GetBookAuthors
// preserving stored order and apply appending the primary FIRST. This pins that
// claim: two rows both at Position 0, stored [primary, coauthor], primary wins.
// If someone "simplifies" the loop to a bare index or re-sorts, this fails.
func TestResolveAuthorName_AllPositionZeroPicksFirstStored(t *testing.T) {
	o := &Organizer{}
	o.SetStore(&authorJoinStore{
		joins: map[string][]database.BookAuthor{
			"b1": {
				{BookID: "b1", AuthorID: 1, Position: 0}, // primary, stored first
				{BookID: "b1", AuthorID: 2, Position: 0}, // co-author, same position
			},
		},
		authorsByID: map[int]*database.Author{
			1: {ID: 1, Name: "Primary"},
			2: {ID: 2, Name: "CoAuthor"},
		},
	})
	book := &database.Book{ID: "b1"}
	if got := o.resolveAuthorName(book); got != "Primary" {
		t.Fatalf("resolveAuthorName = %q, want first-stored primary %q at an all-zero-Position join", got, "Primary")
	}
}

// Every non-real join result must fall through to the scalar path unchanged, so
// a scan-created book (no join rows) or a broken join never REGRESSES a book
// that resolves fine off its scalar. Each row here would return "" from the
// join and must yield the scalar author.
func TestResolveAuthorName_FallsThroughToScalar(t *testing.T) {
	cases := []struct {
		name  string
		store *authorJoinStore
	}{
		{
			name:  "no join rows",
			store: &authorJoinStore{joins: nil, authorsByID: map[int]*database.Author{9: {ID: 9, Name: "Scalar Author"}}},
		},
		{
			name:  "join read error",
			store: &authorJoinStore{joinErr: fmt.Errorf("boom"), authorsByID: map[int]*database.Author{9: {ID: 9, Name: "Scalar Author"}}},
		},
		{
			name: "dangling join row (author id resolves to nil)",
			store: &authorJoinStore{
				joins:       map[string][]database.BookAuthor{"b1": {{BookID: "b1", AuthorID: 404, Position: 0}}},
				authorsByID: map[int]*database.Author{9: {ID: 9, Name: "Scalar Author"}}, // 404 absent
			},
		},
		{
			name: "join resolves only to the placeholder",
			store: &authorJoinStore{
				joins: map[string][]database.BookAuthor{"b1": {{BookID: "b1", AuthorID: 1, Position: 0}}},
				authorsByID: map[int]*database.Author{
					1: {ID: 1, Name: authorname.Placeholder},
					9: {ID: 9, Name: "Scalar Author"},
				},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := &Organizer{}
			o.SetStore(tc.store)
			book := &database.Book{ID: "b1", AuthorID: intp(9)} // Author obj nil -> scalar lookup
			if got := o.resolveAuthorName(book); got != "Scalar Author" {
				t.Fatalf("resolveAuthorName = %q, want scalar fall-through %q", got, "Scalar Author")
			}
			// And the placeholder gate must stay TRUE off the scalar: a broken
			// join must not defer a book that has a perfectly good scalar author.
			if !o.HasResolvedAuthor(book) {
				t.Fatalf("HasResolvedAuthor = false; a working scalar must keep the gate open")
			}
		})
	}
}
