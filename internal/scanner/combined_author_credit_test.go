// file: internal/scanner/combined_author_credit_test.go
// version: 1.0.0
// guid: 3f2cc271-d7d5-4284-9075-9184112447ee
// last-edited: 2026-10-04

package scanner

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/authorcredit"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func usePebbleForCombined(t *testing.T, authors ...string) *database.PebbleStore {
	t.Helper()
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	origStore := database.GetGlobalStore()
	database.SetGlobalStore(st)
	SetStore(st)
	t.Cleanup(func() { database.SetGlobalStore(origStore); SetStore(nil); _ = st.Close() })
	for _, n := range authors {
		_, err := st.CreateAuthor(n)
		require.NoError(t, err)
	}
	return st
}

func authorNamesOf(t *testing.T, st *database.PebbleStore, ids []int) []string {
	t.Helper()
	var out []string
	for _, id := range ids {
		a, err := st.GetAuthorByID(id)
		require.NoError(t, err)
		require.NotNil(t, a)
		out = append(out, a.Name)
	}
	return out
}

// A tag naming two existing authors resolves to both, never to one combined
// author row (1,597 of them on production on 2026-10-04 came from here).
func TestResolveAuthorIDs_SplitsAMultiAuthorTag(t *testing.T) {
	st := usePebbleForCombined(t, "J. N. Chaney", "Jonathan P. Brazee")
	ids, err := resolveAuthorIDs("J.N. Chaney, Jonathan P. Brazee")
	require.NoError(t, err)
	require.Equal(t, []string{"J. N. Chaney", "Jonathan P. Brazee"}, authorNamesOf(t, st, ids))
	for _, n := range []string{"J.N. Chaney, Jonathan P. Brazee", "J. N. Chaney, Jonathan P. Brazee"} {
		a, err := st.GetAuthorByName(n)
		require.NoError(t, err)
		require.Nil(t, a, "no combined row %q", n)
	}
	// resolveAuthorID is the primary alone.
	id, err := resolveAuthorID("J.N. Chaney, Jonathan P. Brazee")
	require.NoError(t, err)
	require.Equal(t, ids[0], *id)
}

// A tag with one part that is no author yet is not split at scan time (new
// authors from a split come only through the fixer's reviewed rows): the
// whole string is looked up and created, as before.
func TestResolveAuthorIDs_UnknownPartKeepsTheWholeString(t *testing.T) {
	st := usePebbleForCombined(t, "J. N. Chaney")
	ids, err := resolveAuthorIDs("J.N. Chaney, Jonathan P. Brazee")
	require.NoError(t, err)
	require.Len(t, ids, 1)
	b, err := st.GetAuthorByName("Jonathan P. Brazee")
	require.NoError(t, err)
	require.Nil(t, b, "no author created from a split")
}

// A combined tag the splitter will not split, whose parts are existing
// authors, is no author rather than a new combined row.
func TestResolveAuthorIDs_RefusesAnUnsplittableCombinedOfExistingAuthors(t *testing.T) {
	st := usePebbleForCombined(t, "Shirtaloon", "Travis Deverell")
	ids, err := resolveAuthorIDs("Shirtaloon, Travis Deverell")
	require.NoError(t, err, "never a failed save")
	require.Empty(t, ids)
	a, err := st.GetAuthorByName("Shirtaloon, Travis Deverell")
	require.NoError(t, err)
	require.Nil(t, a)
}

// The co-authors go into the junction in order, add-only, and only when the
// row's primary is the tag's first author.
func TestCreditScannedAuthors(t *testing.T) {
	st := usePebbleForCombined(t, "J. N. Chaney", "Jonathan P. Brazee")
	ids, err := resolveAuthorIDs("J.N. Chaney, Jonathan P. Brazee")
	require.NoError(t, err)
	b, err := st.CreateBook(&database.Book{Title: "Mission Creep", AuthorID: &ids[0], Format: "m4b", FilePath: "/lib/mc.m4b"})
	require.NoError(t, err)
	creditScannedAuthors(st, b.ID, b.AuthorID, ids)
	cs, err := st.GetBookAuthors(b.ID)
	require.NoError(t, err)
	require.Equal(t, []database.BookAuthor{{BookID: b.ID, AuthorID: ids[0], Role: "author", Position: 0},
		{BookID: b.ID, AuthorID: ids[1], Role: "author", Position: 1}}, cs)
	// A rescan repeats it: nothing changes.
	creditScannedAuthors(st, b.ID, b.AuthorID, ids)
	again, err := st.GetBookAuthors(b.ID)
	require.NoError(t, err)
	require.Equal(t, cs, again)

	// A row whose primary is someone else (a locked or foreign-edited author)
	// gets no co-authors from the scan.
	other, err := st.CreateAuthor("Someone Else")
	require.NoError(t, err)
	b2, err := st.CreateBook(&database.Book{Title: "Other", AuthorID: &other.ID, Format: "m4b", FilePath: "/lib/o.m4b"})
	require.NoError(t, err)
	creditScannedAuthors(st, b2.ID, b2.AuthorID, ids)
	cs2, err := st.GetBookAuthors(b2.ID)
	require.NoError(t, err)
	require.Empty(t, cs2)
}

// A rescan never puts back a co-author on a book whose author the user locked,
// nor when the locks could not be read (treated as locked).
func TestRescanMayCreditAuthors(t *testing.T) {
	require.True(t, rescanMayCreditAuthors(map[string]bool{}, true))
	require.True(t, rescanMayCreditAuthors(map[string]bool{database.FieldKeyTitle: true}, true))
	require.False(t, rescanMayCreditAuthors(map[string]bool{database.FieldKeyAuthorName: true}, true))
	require.False(t, rescanMayCreditAuthors(database.AllUserLockableFieldsLocked(), false))
	require.False(t, rescanMayCreditAuthors(map[string]bool{}, false))
}

// The review of #3717 cases through the real scanner path: every one has a
// part that is no author yet, so none is split and no author is created from
// a split; each keeps one credit, the whole string, as before.
func TestResolveAuthorIDs_ReviewCases(t *testing.T) {
	st := usePebbleForCombined(t, "Annabelle Hawthorne", "Cassius Lange", "Damien Hanson", "Terry Pratchett")
	for _, s := range []string{"Dragon Born", "Star Wars", "Master Class"} {
		_, err := st.CreateSeries(s, nil)
		require.NoError(t, err)
	}
	authorcredit.ResetTitleCache()
	for _, n := range []string{"Dante King (Dragon Born)", "Alphabet Squadron (Star Wars)",
		"Annabelle Hawthorne, Virgil Knightley(Master Class)", "Cassius Lange, LitForge Press, Damien Hanson",
		"Terry Pratchett, Full Cast"} {
		ids, err := resolveAuthorIDs(n)
		require.NoError(t, err, n)
		require.Len(t, ids, 1, n)
	}
	for _, n := range []string{"Dragon Born", "Star Wars", "Master Class", "Virgil Knightley", "LitForge Press",
		"Full Cast", "Dante King", "Alphabet Squadron"} {
		a, err := st.GetAuthorByName(n)
		require.NoError(t, err)
		require.Nil(t, a, "%s must not be created from a split", n)
	}
}
