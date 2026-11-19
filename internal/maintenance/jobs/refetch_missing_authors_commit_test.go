// file: internal/maintenance/jobs/refetch_missing_authors_commit_test.go
// version: 1.0.0
// guid: 39e9bceb-b565-4f8d-918b-4f70327e56ef
// last-edited: 2026-11-19

package jobs

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// creditCommitFaultStore fails the one commit that writes a book's row and its
// credits together, as a crash or a store error between "set the primary" and
// "credit the co-authors" would have left the book half written.
type creditCommitFaultStore struct {
	*database.PebbleStore
	commits int
}

func (s *creditCommitFaultStore) ModifyBookCredits(string, func(*database.Book, *database.BookCreditsEdit) error) (*database.Book, database.BookCreditsEdit, error) {
	s.commits++
	return nil, database.BookCreditsEdit{}, errors.New("injected ModifyBookCredits failure")
}

// The primary author and the co-author credits are ONE commit. When it fails the
// book is exactly as it was: still authorless, with no credit rows, so the next
// run starts from the same state. Two commits left a book with a primary and no
// co-authors (the old "partial" outcome), which no later run would repair because
// the book no longer looked authorless.
func TestLinkTagAuthors_AFailedCommitLeavesTheBookAuthorlessWithNoCredits(t *testing.T) {
	st := &creditCommitFaultStore{PebbleStore: newRefetchPebble(t)}
	_, err := st.CreateAuthor("Bruce Wayne")
	require.NoError(t, err)
	_, err = st.CreateAuthor("Dick Grayson")
	require.NoError(t, err)
	b, err := st.CreateBook(&database.Book{Title: "Year One", FilePath: "/l/y1.m4b", Format: "m4b"})
	require.NoError(t, err)

	out, err := linkTagAuthors(st, b.ID, "Bruce Wayne, Dick Grayson")

	require.Equal(t, tagAuthorsFailed, out)
	require.ErrorContains(t, err, "injected ModifyBookCredits failure")
	require.Equal(t, 1, st.commits, "the write was not one commit")
	got, gerr := st.GetBookByID(b.ID)
	require.NoError(t, gerr)
	require.Nil(t, got.AuthorID, "the primary was written although the credit commit failed")
	credits, gerr := st.GetBookAuthors(b.ID)
	require.NoError(t, gerr)
	require.Empty(t, credits, "credits were written although the commit failed")
}

// A tag naming several people sets the primary and credits every one of them in
// the one commit, in the order the tag named them. (A tag naming one person sets
// the primary only, as before: the junction of such a book is left to the credits
// backfill.)
func TestLinkTagAuthors_OneCommitWritesThePrimaryAndEveryCredit(t *testing.T) {
	st := newRefetchPebble(t)
	bruce, err := st.CreateAuthor("Bruce Wayne")
	require.NoError(t, err)
	dick, err := st.CreateAuthor("Dick Grayson")
	require.NoError(t, err)
	b, err := st.CreateBook(&database.Book{Title: "Year One", FilePath: "/l/y1.m4b", Format: "m4b"})
	require.NoError(t, err)

	out, err := linkTagAuthors(st, b.ID, "Bruce Wayne, Dick Grayson")

	require.NoError(t, err)
	require.Equal(t, tagAuthorsLinked, out)
	got, gerr := st.GetBookByID(b.ID)
	require.NoError(t, gerr)
	require.NotNil(t, got.AuthorID)
	require.Equal(t, bruce.ID, *got.AuthorID)
	credits, gerr := st.GetBookAuthors(b.ID)
	require.NoError(t, gerr)
	require.Equal(t, []database.BookAuthor{
		{BookID: b.ID, AuthorID: bruce.ID, Role: "author", Position: 0},
		{BookID: b.ID, AuthorID: dick.ID, Role: "author", Position: 1},
	}, credits)
}

// A book given an author meanwhile keeps it and gains no co-author: the decision
// is made on the fresh row inside the commit, and nothing is written.
func TestLinkTagAuthors_ABookThatGotAnAuthorMeanwhileIsLeftAlone(t *testing.T) {
	st := newRefetchPebble(t)
	other, err := st.CreateAuthor("Alfred Pennyworth")
	require.NoError(t, err)
	_, err = st.CreateAuthor("Bruce Wayne")
	require.NoError(t, err)
	_, err = st.CreateAuthor("Dick Grayson")
	require.NoError(t, err)
	b, err := st.CreateBook(&database.Book{Title: "Year One", FilePath: "/l/y1.m4b", Format: "m4b", AuthorID: new(other.ID)})
	require.NoError(t, err)

	out, err := linkTagAuthors(st, b.ID, "Bruce Wayne, Dick Grayson")

	require.NoError(t, err)
	require.Equal(t, tagAuthorsLinked, out)
	got, gerr := st.GetBookByID(b.ID)
	require.NoError(t, gerr)
	require.Equal(t, other.ID, *got.AuthorID)
	credits, gerr := st.GetBookAuthors(b.ID)
	require.NoError(t, gerr)
	for _, c := range credits {
		require.Equal(t, other.ID, c.AuthorID, "a co-author was credited to a book that already had an author")
	}
}
