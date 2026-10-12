// file: internal/database/book_credit_index_test.go
// version: 1.1.0
// guid: 0c7a4e92-6d15-4b83-b2f9-3a8e1d5c9074
// last-edited: 2026-10-12

package database

import (
	"encoding/json"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/require"
)

// All names below are synthetic (public repo).

func newCreditIndexStore(t *testing.T) *PebbleStore {
	t.Helper()
	ps, err := NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })
	ps.WaitForWarmup()
	return ps
}

func creditPeople(t *testing.T, ps *PebbleStore, names ...string) []int {
	t.Helper()
	ids := make([]int, len(names))
	for i, n := range names {
		a, err := ps.CreateAuthor(n)
		require.NoError(t, err)
		ids[i] = a.ID
	}
	return ids
}

func creditBook(t *testing.T, ps *PebbleStore, title string, primary *int, rows ...BookAuthor) string {
	t.Helper()
	b, err := ps.CreateBook(&Book{Title: title, FilePath: "/synthetic/" + title + ".m4b", AuthorID: primary})
	require.NoError(t, err)
	if len(rows) > 0 {
		require.NoError(t, ps.SetBookAuthors(b.ID, rows))
	}
	return b.ID
}

// normalizedIDs orders a book's index rows the way the author: filter does.
func normalizedIDs(rows []BookAuthor, primary *int) []int {
	var ids []int
	for _, r := range NormalizeBookAuthors(rows, primary) {
		ids = append(ids, r.AuthorID)
	}
	return ids
}

func TestIsAuthorCreditRole(t *testing.T) {
	for _, role := range []string{"", "author", "Author", " co-author ", "coauthor", "primary"} {
		require.True(t, IsAuthorCreditRole(role), role)
	}
	for _, role := range []string{"narrator", "translator", "editor", "illustrator", "foreword"} {
		require.False(t, IsAuthorCreditRole(role), role)
	}
}

// The memdb answer and the authoritative Pebble scan agree, keep author roles
// only, and leave out a book with no author-role credit.
func TestBookAuthorRoleCredits_MemdbAgreesWithPebble(t *testing.T) {
	ps := newCreditIndexStore(t)
	p := creditPeople(t, ps, "Synthetic One", "Synthetic Two", "Synthetic Three")
	pair := creditBook(t, ps, "Synthetic Pair", &p[0],
		BookAuthor{AuthorID: p[1], Role: "co-author", Position: 1},
		BookAuthor{AuthorID: p[0], Role: "author", Position: 0})
	translated := creditBook(t, ps, "Synthetic Translated", &p[0],
		BookAuthor{AuthorID: p[0], Role: "author", Position: 0},
		BookAuthor{AuthorID: p[2], Role: "translator", Position: 1})
	editedOnly := creditBook(t, ps, "Synthetic Edited", nil,
		BookAuthor{AuthorID: p[2], Role: "editor", Position: 0})
	lone := creditBook(t, ps, "Synthetic No Credits", nil)

	var store BookCreditIndexStore = ps
	got, err := store.GetAllBookAuthorRoleCredits()
	require.NoError(t, err)
	require.Equal(t, []int{p[0], p[1]}, normalizedIDs(got[pair], &p[0]))
	require.Equal(t, []int{p[0]}, normalizedIDs(got[translated], &p[0]), "the translator is not an author")
	require.NotContains(t, got, editedOnly)
	require.NotContains(t, got, lone)

	scanned, err := ps.authorRoleCreditsPebbleScan()
	require.NoError(t, err)
	require.Equal(t, len(scanned), len(got))
	for id, rows := range scanned {
		require.Equal(t, normalizedIDs(rows, nil), normalizedIDs(got[id], nil), id)
	}
}

// memdb holds one junction row per person and book. For a co-author who also
// narrates, the row it keeps is the narrator one, so memdb alone would lose
// the author credit; the index must answer that book from Pebble.
func TestBookAuthorRoleCredits_AuthorWhoAlsoNarrates(t *testing.T) {
	ps := newCreditIndexStore(t)
	p := creditPeople(t, ps, "Synthetic Writer", "Synthetic Reader")
	book := creditBook(t, ps, "Synthetic Self Read", &p[0],
		BookAuthor{AuthorID: p[0], Role: "author", Position: 0},
		BookAuthor{AuthorID: p[1], Role: "co-author", Position: 1},
		BookAuthor{AuthorID: p[1], Role: "narrator", Position: 2})

	// The premise: memdb kept the narrator row and dropped the co-author one.
	txn := ps.mem().db.Txn(false)
	obj, err := txn.First(memTableBookAuthors, memIdxID, book, p[1])
	txn.Abort()
	require.NoError(t, err)
	require.NotNil(t, obj)
	require.Equal(t, "narrator", obj.(*BookAuthor).Role, "fixture premise: memdb keeps the later row for a repeated person")

	_, unresolved, err := ps.mem().authorRoleCredits()
	require.NoError(t, err)
	require.Contains(t, unresolved, book, "memdb cannot settle this book's author roles")

	got, err := ps.GetAllBookAuthorRoleCredits()
	require.NoError(t, err)
	require.Equal(t, []int{p[0], p[1]}, normalizedIDs(got[book], &p[0]))
}

// A memdb known to be short is not trusted: the index answers from the Pebble
// scan. The fixture makes the two disagree first, so the assertion can tell
// which one answered.
func TestBookAuthorRoleCredits_MemdbIncompleteFallsThroughToPebble(t *testing.T) {
	ps := newCreditIndexStore(t)
	p := creditPeople(t, ps, "Synthetic First", "Synthetic Second")
	book := creditBook(t, ps, "Synthetic Diverged", &p[0],
		BookAuthor{AuthorID: p[0], Role: "author", Position: 0},
		BookAuthor{AuthorID: p[1], Role: "co-author", Position: 1})

	// Drop the co-author's row from memdb only. The remaining row has no
	// position gap, so memdb would answer this book on its own.
	func() {
		txn := ps.mem().db.Txn(true)
		defer txn.Commit()
		obj, err := txn.First(memTableBookAuthors, memIdxID, book, p[1])
		require.NoError(t, err)
		require.NotNil(t, obj)
		require.NoError(t, txn.Delete(memTableBookAuthors, obj))
	}()
	before, err := ps.GetAllBookAuthorRoleCredits()
	require.NoError(t, err)
	require.Equal(t, []int{p[0]}, normalizedIDs(before[book], &p[0]), "fixture premise: memdb now answers without the co-author")

	ps.mem().recordLostRows(memTableBookAuthors, 1)
	after, err := ps.GetAllBookAuthorRoleCredits()
	require.NoError(t, err)
	require.Equal(t, []int{p[0], p[1]}, normalizedIDs(after[book], &p[0]), "a short memdb must be bypassed for the Pebble scan")
}

// The original failure: one undecodable book_authors value both makes memdb
// short (warmup could not load it) and used to fail the Pebble scan, so every
// author: query fell back to primary-only library-wide. Now only the bad book
// loses its credits.
func TestBookAuthorRoleCredits_UndecodableRowCostsOnlyItsBook(t *testing.T) {
	ps := newCreditIndexStore(t)
	p := creditPeople(t, ps, "Synthetic Good", "Synthetic Partner")
	good := creditBook(t, ps, "Synthetic Good Book", &p[0],
		BookAuthor{AuthorID: p[0], Role: "author", Position: 0},
		BookAuthor{AuthorID: p[1], Role: "co-author", Position: 1})
	bad := creditBook(t, ps, "Synthetic Bad Book", &p[0],
		BookAuthor{AuthorID: p[0], Role: "author", Position: 0})
	require.NoError(t, ps.db.Set(bookAuthorsKey(bad), []byte("{not json"), pebble.Sync))
	ps.mem().recordLostRows(memTableBookAuthors, 1)

	got, err := ps.GetAllBookAuthorRoleCredits()
	require.NoError(t, err, "one bad row must not fail the whole index")
	require.Equal(t, []int{p[0], p[1]}, normalizedIDs(got[good], &p[0]))
	require.NotContains(t, got, bad)

	// The per-book Pebble read used for books memdb cannot settle skips the
	// same way.
	credits := map[string][]BookAuthor{}
	require.NoError(t, ps.readAuthorRoleCreditsPebble(credits, []string{bad, good}))
	require.NotContains(t, credits, bad)
	require.Equal(t, []int{p[0], p[1]}, normalizedIDs(credits[good], &p[0]))
}

// A stored tie that includes the primary author orders the same from memdb
// (which iterates by author ID) and from Pebble (stored order), because both
// are ordered by NormalizeBookAuthors with the book's AuthorID. The tie is
// written raw: SetBookAuthors renumbers positions, so a tie built through it
// would prove nothing.
func TestBookAuthorRoleCredits_PrimaryTieOrdersTheSameOnBothPaths(t *testing.T) {
	ps := newCreditIndexStore(t)
	p := creditPeople(t, ps, "Synthetic Low", "Synthetic High")
	primary := p[1] // the higher ID, so memdb's ID order puts it second
	book := creditBook(t, ps, "Synthetic Tie", &primary)
	raw := []BookAuthor{
		{BookID: book, AuthorID: p[0], Role: "author", Position: 0},
		{BookID: book, AuthorID: p[1], Role: "author", Position: 0},
	}
	data, err := json.Marshal(raw)
	require.NoError(t, err)
	require.NoError(t, ps.db.Set(bookAuthorsKey(book), data, pebble.Sync))
	ps.ReplaceBookAuthorsInMemDB(book, raw)

	mem, unresolved, err := ps.mem().authorRoleCredits()
	require.NoError(t, err)
	require.NotContains(t, unresolved, book, "a tie is not a gap: memdb answers it")
	scanned, err := ps.authorRoleCreditsPebbleScan()
	require.NoError(t, err)

	want := []int{primary, p[0]}
	require.Equal(t, want, normalizedIDs(mem[book], &primary))
	require.Equal(t, want, normalizedIDs(scanned[book], &primary))
}

// A credit-only write advances the library generation and records the book, so
// generation-keyed list caches (an author: page) drop their entry.
func TestSetBookAuthors_BumpsLibraryGeneration(t *testing.T) {
	ps := newCreditIndexStore(t)
	p := creditPeople(t, ps, "Synthetic Lead", "Synthetic Added")
	book := creditBook(t, ps, "Synthetic Credited", &p[0])

	gen, ok := LibraryGenerationOf(ps)
	require.True(t, ok)
	before := gen.Value()
	require.NoError(t, ps.SetBookAuthors(book, []BookAuthor{
		{AuthorID: p[0], Role: "author", Position: 0},
		{AuthorID: p[1], Role: "co-author", Position: 1},
	}))
	require.Greater(t, gen.Value(), before)
	ids, _, ok := ps.BooksChangedSince(before)
	require.True(t, ok)
	require.Contains(t, ids, book)
}
