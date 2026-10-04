// file: internal/metafetch/apply_authors_combined_test.go
// version: 1.1.0
// guid: 4cc97c58-fb54-465a-bc6d-5358ffddf182
// last-edited: 2026-10-04

package metafetch

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

func combinedCredits(t *testing.T, st *database.PebbleStore, bookID string) []string {
	t.Helper()
	cs, err := st.GetBookAuthors(bookID)
	require.NoError(t, err)
	var out []string
	for _, c := range cs {
		a, err := st.GetAuthorByID(c.AuthorID)
		require.NoError(t, err)
		out = append(out, a.Name)
	}
	return out
}

// A provider credit naming two people credits each of them; it never mints a
// combined author row or appends one beside the separate authors (the
// "Mission Creep" shape found on production on 2026-10-04).
func TestApplyMetadataToBook_MultiAuthorProviderCreditIsSplit(t *testing.T) {
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	svc := NewService(st)
	chaney, err := st.CreateAuthor("J. N. Chaney")
	require.NoError(t, err)
	brazee, err := st.CreateAuthor("Jonathan P. Brazee")
	require.NoError(t, err)

	// Already credited to both: nothing is added.
	both, err := st.CreateBook(&database.Book{Title: "Mission Creep", FilePath: "/l/mc.m4b", Format: "m4b", AuthorID: &chaney.ID})
	require.NoError(t, err)
	require.NoError(t, st.SetBookAuthors(both.ID, []database.BookAuthor{
		{BookID: both.ID, AuthorID: chaney.ID, Role: "author", Position: 0},
		{BookID: both.ID, AuthorID: brazee.ID, Role: "author", Position: 1}}))
	_, err = svc.ApplyMetadataToBook(both, metadata.BookMetadata{Author: "J.N. Chaney, Jonathan P. Brazee"})
	require.NoError(t, err)
	require.Equal(t, []string{"J. N. Chaney", "Jonathan P. Brazee"}, combinedCredits(t, st, both.ID))

	// Credited to one: the other is appended after it, in one write.
	one, err := st.CreateBook(&database.Book{Title: "Second", FilePath: "/l/s.m4b", Format: "m4b", AuthorID: &chaney.ID})
	require.NoError(t, err)
	require.NoError(t, st.SetBookAuthors(one.ID, []database.BookAuthor{{BookID: one.ID, AuthorID: chaney.ID, Role: "author", Position: 0}}))
	_, err = svc.ApplyMetadataToBook(one, metadata.BookMetadata{Author: "J.N. Chaney, Jonathan P. Brazee"})
	require.NoError(t, err)
	require.Equal(t, []string{"J. N. Chaney", "Jonathan P. Brazee"}, combinedCredits(t, st, one.ID))
	cs, err := st.GetBookAuthors(one.ID)
	require.NoError(t, err)
	require.Equal(t, 1, cs[1].Position)

	// No author yet: both credited, the first is the primary.
	none, err := st.CreateBook(&database.Book{Title: "Third", FilePath: "/l/t.m4b", Format: "m4b"})
	require.NoError(t, err)
	_, err = svc.ApplyMetadataToBook(none, metadata.BookMetadata{Author: "J.N. Chaney, Jonathan P. Brazee"})
	require.NoError(t, err)
	require.Equal(t, []string{"J. N. Chaney", "Jonathan P. Brazee"}, combinedCredits(t, st, none.ID))
	require.NotNil(t, none.AuthorID)
	require.Equal(t, chaney.ID, *none.AuthorID)

	for _, n := range []string{"J.N. Chaney, Jonathan P. Brazee", "J. N. Chaney, Jonathan P. Brazee"} {
		a, err := st.GetAuthorByName(n)
		require.NoError(t, err)
		require.Nil(t, a, "no combined row %q", n)
	}
}

// A combined credit too long to split (four names), whose parts exist, credits
// no one: the book keeps its credits and no combined row is created.
func TestApplyMetadataToBook_UnsplittableCombinedOfExistingAuthorsIsNotCreated(t *testing.T) {
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	svc := NewService(st)
	s, err := st.CreateAuthor("Amy Adams")
	require.NoError(t, err)
	for _, n := range []string{"Ben Brown", "Cat Cole", "Dan Dorn"} {
		_, err = st.CreateAuthor(n)
		require.NoError(t, err)
	}
	b, err := st.CreateBook(&database.Book{Title: "HWFWM", FilePath: "/l/h.m4b", Format: "m4b", AuthorID: &s.ID})
	require.NoError(t, err)
	require.NoError(t, st.SetBookAuthors(b.ID, []database.BookAuthor{{BookID: b.ID, AuthorID: s.ID, Role: "author", Position: 0}}))
	_, err = svc.ApplyMetadataToBook(b, metadata.BookMetadata{Author: "Amy Adams, Ben Brown, Cat Cole, Dan Dorn"})
	require.NoError(t, err)
	require.Equal(t, []string{"Amy Adams"}, combinedCredits(t, st, b.ID))
	a, err := st.GetAuthorByName("Amy Adams, Ben Brown, Cat Cole, Dan Dorn")
	require.NoError(t, err)
	require.Nil(t, a)
}
