// file: internal/metadata/enhanced_combined_author_test.go
// version: 1.1.1
// guid: 4cccd595-4313-4075-bffd-0e7d842088af
// last-edited: 2026-10-04

package metadata

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func TestBatchUpdateMetadata_MultiAuthorNameIsSplit(t *testing.T) {
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	b, err := st.CreateBook(&database.Book{Title: "Mission Creep", FilePath: "/l/mc.m4b", Format: "m4b"})
	require.NoError(t, err)
	for _, n := range []string{"Big Finish Productions", "Nicholas Briggs", "J. N. Chaney", "Jonathan P. Brazee"} {
		_, err := st.CreateAuthor(n)
		require.NoError(t, err)
	}
	b2, err := st.CreateBook(&database.Book{Title: "HWFWM", FilePath: "/l/h.m4b", Format: "m4b"})
	require.NoError(t, err)

	errs, ok := BatchUpdateMetadata([]MetadataUpdate{
		{BookID: b.ID, Updates: map[string]any{"author": "J.N. Chaney, Jonathan P. Brazee"}},
		{BookID: b2.ID, Updates: map[string]any{"author": "Big Finish Productions, Nicholas Briggs"}},
	}, st, false)
	require.Empty(t, errs)
	require.Equal(t, 2, ok)

	chaney, err := st.GetAuthorByName("J. N. Chaney")
	require.NoError(t, err)
	require.NotNil(t, chaney)
	brazee, err := st.GetAuthorByName("Jonathan P. Brazee")
	require.NoError(t, err)
	require.NotNil(t, brazee)
	got, err := st.GetBookByID(b.ID)
	require.NoError(t, err)
	require.Equal(t, chaney.ID, *got.AuthorID)
	cs, err := st.GetBookAuthors(b.ID)
	require.NoError(t, err)
	require.Len(t, cs, 2)
	require.Equal(t, brazee.ID, cs[1].AuthorID)
	combined, err := st.GetAuthorByName("J. N. Chaney, Jonathan P. Brazee")
	require.NoError(t, err)
	require.Nil(t, combined)

	// The unsplittable combined name of existing authors changes nothing.
	got2, err := st.GetBookByID(b2.ID)
	require.NoError(t, err)
	require.Nil(t, got2.AuthorID)
	c2, err := st.GetAuthorByName("Big Finish Productions, Nicholas Briggs")
	require.NoError(t, err)
	require.Nil(t, c2)
}
