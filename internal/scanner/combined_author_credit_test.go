// file: internal/scanner/combined_author_credit_test.go
// version: 1.0.0
// guid: 3f2cc271-d7d5-4284-9075-9184112447ee
// last-edited: 2026-10-04

package scanner

import (
	"testing"

	"github.com/stretchr/testify/require"

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

// A tag naming two people resolves to both authors, never to one combined
// author row (1,597 of them on production on 2026-10-04 came from here).
func TestResolveAuthorIDs_SplitsAMultiAuthorTag(t *testing.T) {
	st := usePebbleForCombined(t, "J. N. Chaney")
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
	st := usePebbleForCombined(t)
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
