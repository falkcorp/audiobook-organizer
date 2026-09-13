// file: internal/database/author_primary_repoint_test.go
// version: 1.0.0
// guid: 9a4d27e3-1c85-4b6f-8e02-5f3b7c9d1a46
// last-edited: 2026-09-13

package database

import (
	"errors"
	"fmt"
	"testing"
)

// These run against a real, warm PebbleStore: the repoint crosses UpdateBook,
// the name index, CreateAuthor's lock and the memdb mirror, none of which a
// MockStore models. A self-deadlock between DeleteAuthor and CreateAuthor (both
// take nameIdx.author) would hang here and nowhere else.

func mustAuthor(t *testing.T, s Store, name string) *Author {
	t.Helper()
	a, err := s.CreateAuthor(name)
	if err != nil || a == nil {
		t.Fatalf("CreateAuthor(%q): %v", name, err)
	}
	return a
}

func mustBook(t *testing.T, s Store, id string, scalar *int, joins ...int) {
	t.Helper()
	if _, err := s.CreateBook(&Book{ID: id, Title: "T " + id, FilePath: "/lib/" + id + ".m4b", AuthorID: scalar}); err != nil {
		t.Fatalf("CreateBook(%s): %v", id, err)
	}
	if len(joins) == 0 {
		return
	}
	ba := make([]BookAuthor, len(joins))
	for i, a := range joins {
		ba[i] = BookAuthor{BookID: id, AuthorID: a, Role: "author", Position: i}
	}
	if err := s.SetBookAuthors(id, ba); err != nil {
		t.Fatalf("SetBookAuthors(%s): %v", id, err)
	}
}

// assertScalar checks the Pebble row AND the memdb projection agree on id, and
// that id is a live, non-zero author.
func assertScalar(t *testing.T, s Store, bookID string, want int) {
	t.Helper()
	b, err := s.GetBookByID(bookID)
	if err != nil || b == nil {
		t.Fatalf("GetBookByID(%s): %v", bookID, err)
	}
	if b.AuthorID == nil || *b.AuthorID == 0 {
		t.Fatalf("book %s AuthorID cleared/zero (%v); it must be repointed", bookID, b.AuthorID)
	}
	if *b.AuthorID != want {
		t.Fatalf("book %s AuthorID = %d, want %d", bookID, *b.AuthorID, want)
	}
	if b.Author == nil || b.Author.ID != want {
		t.Fatalf("book %s denormalized Author = %+v, want id %d", bookID, b.Author, want)
	}
	if a, _ := s.GetAuthorByID(want); a == nil {
		t.Fatalf("book %s AuthorID %d does not resolve: dangling", bookID, want)
	}
	cores, err := s.GetAllBooksCoreComplete(1000, 0)
	if err != nil {
		t.Fatalf("GetAllBooksCoreComplete: %v", err)
	}
	for _, c := range cores {
		if c.ID == bookID {
			if c.AuthorID == nil || *c.AuthorID != want {
				t.Fatalf("memdb projection of %s has AuthorID %v, Pebble has %d: mirror missed", bookID, c.AuthorID, want)
			}
			return
		}
	}
	t.Fatalf("book %s missing from memdb projection", bookID)
}

func TestDeleteAuthor_RepointsScalarToNextAuthorByJoinPosition(t *testing.T) {
	s, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	a := mustAuthor(t, s, "Primary Person")
	b := mustAuthor(t, s, "Lower Id Third")
	c := mustAuthor(t, s, "Second Position")
	// Positions: a=0, c=1, b=2. The successor is c by POSITION, not b by id.
	mustBook(t, s, "bk1", &a.ID, a.ID, c.ID, b.ID)

	if err := s.DeleteAuthor(a.ID); err != nil {
		t.Fatalf("DeleteAuthor: %v", err)
	}
	assertScalar(t, s, "bk1", c.ID)
}

func TestDeleteAuthor_FallsBackToIndexResolvedUnknownAuthor(t *testing.T) {
	s, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	a := mustAuthor(t, s, "Only Author")
	// Two rows named Unknown Author, as prod has. The rename takes the name
	// index over, so the index resolves to u2, NOT the lower id u1.
	u1 := mustAuthor(t, s, UnknownAuthorName)
	u2 := mustAuthor(t, s, "placeholder twin")
	if err := s.UpdateAuthorName(u2.ID, "UNKNOWN AUTHOR"); err != nil {
		t.Fatalf("UpdateAuthorName: %v", err)
	}
	if r, _ := s.GetAuthorByName(UnknownAuthorName); r == nil || r.ID != u2.ID {
		t.Fatalf("fixture: index should resolve to %d, got %+v (u1=%d)", u2.ID, r, u1.ID)
	}
	mustBook(t, s, "bk1", &a.ID, a.ID)

	if err := s.DeleteAuthor(a.ID); err != nil {
		t.Fatalf("DeleteAuthor: %v", err)
	}
	assertScalar(t, s, "bk1", u2.ID)
}

func TestDeleteAuthor_CreatesUnknownAuthorWhenAbsent_ScalarOnlyBook(t *testing.T) {
	s, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	a := mustAuthor(t, s, "Legacy Scalar Only")
	// No junction row at all: only the legacy scalar credits a. The join sweep
	// never sees this book, which is the population the old code dangled.
	mustBook(t, s, "bk1", &a.ID)

	if err := s.DeleteAuthor(a.ID); err != nil {
		t.Fatalf("DeleteAuthor: %v", err)
	}
	u, err := s.GetAuthorByName(UnknownAuthorName)
	if err != nil || u == nil {
		t.Fatalf("Unknown Author was not created: %+v %v", u, err)
	}
	assertScalar(t, s, "bk1", u.ID)
	if gone, _ := s.GetAuthorByID(a.ID); gone != nil {
		t.Fatalf("author %d still exists after DeleteAuthor", a.ID)
	}
}

func TestDeleteAuthor_SkipsDanglingJoinSuccessor(t *testing.T) {
	s, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	a := mustAuthor(t, s, "Doomed")
	c := mustAuthor(t, s, "Survivor")
	// Position 1 names an id with no row; the successor must skip it.
	mustBook(t, s, "bk1", &a.ID, a.ID, 987654, c.ID)
	if err := s.DeleteAuthor(a.ID); err != nil {
		t.Fatalf("DeleteAuthor: %v", err)
	}
	assertScalar(t, s, "bk1", c.ID)
}

func TestDeleteAuthor_RefusesToDeleteUnknownPlaceholderThatIsTheLastAuthor(t *testing.T) {
	s, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	u := mustAuthor(t, s, UnknownAuthorName)
	mustBook(t, s, "bk1", &u.ID, u.ID)

	err := s.DeleteAuthor(u.ID)
	if !errors.Is(err, ErrNoPrimaryAuthorSuccessor) {
		t.Fatalf("DeleteAuthor(placeholder) err = %v, want ErrNoPrimaryAuthorSuccessor", err)
	}
	if still, _ := s.GetAuthorByID(u.ID); still == nil {
		t.Fatalf("refused delete must leave the author row in place")
	}
	if b, _ := s.GetBookByID("bk1"); b == nil || b.AuthorID == nil || *b.AuthorID != u.ID {
		t.Fatalf("refused delete must leave the scalar untouched, got %+v", b)
	}
}

func TestGetBookIDsCreditingAuthorDurable_JunctionAndScalar(t *testing.T) {
	s, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	a := mustAuthor(t, s, "Credited")
	o := mustAuthor(t, s, "Other")
	mustBook(t, s, "junction-only", &o.ID, o.ID, a.ID)
	mustBook(t, s, "scalar-only", &a.ID)
	mustBook(t, s, "unrelated", &o.ID, o.ID)

	ids, err := s.GetBookIDsCreditingAuthorDurable(a.ID)
	if err != nil {
		t.Fatalf("GetBookIDsCreditingAuthorDurable: %v", err)
	}
	if fmt.Sprint(ids) != "[junction-only scalar-only]" {
		t.Fatalf("durable credits = %v, want [junction-only scalar-only]", ids)
	}
}

func TestNextPrimaryAuthorID(t *testing.T) {
	live := map[int]int{2: 2, 3: 3, 4: 40} // 4 is tombstoned onto 40
	resolve := func(id int) (int, bool) { v, ok := live[id]; return v, ok }
	joins := []BookAuthor{{AuthorID: 1, Position: 0}, {AuthorID: 9, Position: 1}, {AuthorID: 4, Position: 2}, {AuthorID: 2, Position: 3}}
	if got, ok := NextPrimaryAuthorID(joins, 1, resolve); !ok || got != 40 {
		t.Fatalf("NextPrimaryAuthorID = %d,%v; want 40 (tombstone-resolved, first live by position)", got, ok)
	}
	if got, ok := NextPrimaryAuthorID([]BookAuthor{{AuthorID: 1}}, 1, resolve); ok || got != 0 {
		t.Fatalf("no successor must be (0,false), got %d,%v", got, ok)
	}
}
