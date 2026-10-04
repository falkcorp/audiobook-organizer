// file: internal/maintenance/jobs/refetch_missing_authors_combined_test.go
// version: 1.0.0
// guid: 9491d68a-a546-43db-9cd6-39abba52f631
// last-edited: 2026-10-04

package jobs

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func TestLinkTagAuthors_SplitsAMultiAuthorTag(t *testing.T) {
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	chaney, err := st.CreateAuthor("J. N. Chaney")
	require.NoError(t, err)
	b, err := st.CreateBook(&database.Book{Title: "Mission Creep", FilePath: "/l/mc.m4b", Format: "m4b"})
	require.NoError(t, err)

	out, err := linkTagAuthors(st, b.ID, "J.N. Chaney, Jonathan P. Brazee")
	require.NoError(t, err)
	require.Equal(t, tagAuthorsLinked, out)
	got, err := st.GetBookByID(b.ID)
	require.NoError(t, err)
	require.Equal(t, chaney.ID, *got.AuthorID)
	brazee, err := st.GetAuthorByName("Jonathan P. Brazee")
	require.NoError(t, err)
	require.NotNil(t, brazee)
	cs, err := st.GetBookAuthors(b.ID)
	require.NoError(t, err)
	require.Equal(t, []database.BookAuthor{{BookID: b.ID, AuthorID: chaney.ID, Role: "author", Position: 0},
		{BookID: b.ID, AuthorID: brazee.ID, Role: "author", Position: 1}}, cs)
	combined, err := st.GetAuthorByName("J. N. Chaney, Jonathan P. Brazee")
	require.NoError(t, err)
	require.Nil(t, combined)
}

func TestLinkTagAuthors_RefusesAnUnsplittableCombinedOfExistingAuthors(t *testing.T) {
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	for _, n := range []string{"Shirtaloon", "Travis Deverell"} {
		_, err := st.CreateAuthor(n)
		require.NoError(t, err)
	}
	b, err := st.CreateBook(&database.Book{Title: "HWFWM", FilePath: "/l/h.m4b", Format: "m4b"})
	require.NoError(t, err)
	out, err := linkTagAuthors(st, b.ID, "Shirtaloon, Travis Deverell")
	require.NoError(t, err)
	require.Equal(t, tagAuthorsCombined, out)
	got, err := st.GetBookByID(b.ID)
	require.NoError(t, err)
	require.Nil(t, got.AuthorID)
}
