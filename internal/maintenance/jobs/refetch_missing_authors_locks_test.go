// file: internal/maintenance/jobs/refetch_missing_authors_locks_test.go
// version: 1.0.0
// guid: 6cb2084f-a61a-4cf9-8257-af314fbf0ebb
// last-edited: 2026-11-18

package jobs

import (
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// lockFlipStore reads field states from the embedded store until the Nth call,
// then reports the book's author as user-locked: a lock the user saves between
// the job's advisory read and the write. failReads makes every read fail.
type lockFlipStore struct {
	*database.PebbleStore
	calls     atomic.Int32
	lockAfter int32
	failReads bool
}

func (s *lockFlipStore) GetMetadataFieldStates(bookID string) ([]database.MetadataFieldState, error) {
	n := s.calls.Add(1)
	if s.failReads {
		return nil, errors.New("field states: store closed")
	}
	if s.lockAfter > 0 && n > s.lockAfter {
		return []database.MetadataFieldState{{BookID: bookID, Field: database.FieldKeyAuthorName, OverrideLocked: true}}, nil
	}
	return s.PebbleStore.GetMetadataFieldStates(bookID)
}

func newRefetchPebble(t *testing.T) *database.PebbleStore {
	t.Helper()
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestLinkTagAuthors_UserLockedAuthorIsNotLinkedAndNothingIsCreated(t *testing.T) {
	st := newRefetchPebble(t)
	locked, err := st.CreateBook(&database.Book{Title: "Year One", FilePath: "/l/y1.m4b", Format: "m4b"})
	require.NoError(t, err)
	open, err := st.CreateBook(&database.Book{Title: "Year Two", FilePath: "/l/y2.m4b", Format: "m4b"})
	require.NoError(t, err)
	require.NoError(t, st.UpsertMetadataFieldState(&database.MetadataFieldState{
		BookID: locked.ID, Field: database.FieldKeyAuthorName, OverrideLocked: true,
	}))

	out, err := linkTagAuthors(st, locked.ID, "Selina Kyle")
	require.NoError(t, err)
	require.Equal(t, tagAuthorsHeld, out)
	got, err := st.GetBookByID(locked.ID)
	require.NoError(t, err)
	require.Nil(t, got.AuthorID, "a user-locked author must not be set")
	// The lock is read before the resolve: a refused link must not leave the
	// author row the resolve would have created.
	orphan, err := st.GetAuthorByName("Selina Kyle")
	require.NoError(t, err)
	require.Nil(t, orphan, "no author row may be created for a held book")

	// Control: an unlocked book gets its author in the same store.
	out, err = linkTagAuthors(st, open.ID, "Harvey Bullock")
	require.NoError(t, err)
	require.Equal(t, tagAuthorsLinked, out)
	got, err = st.GetBookByID(open.ID)
	require.NoError(t, err)
	require.NotNil(t, got.AuthorID)
}

func TestLinkTagAuthors_UnreadableLocksWriteNothing(t *testing.T) {
	st := &lockFlipStore{PebbleStore: newRefetchPebble(t), failReads: true}
	b, err := st.CreateBook(&database.Book{Title: "Year One", FilePath: "/l/y1.m4b", Format: "m4b"})
	require.NoError(t, err)

	out, err := linkTagAuthors(st, b.ID, "Selina Kyle")
	require.Equal(t, tagAuthorsFailed, out)
	require.ErrorIs(t, err, database.ErrFieldLocksUnavailable)
	got, gerr := st.GetBookByID(b.ID)
	require.NoError(t, gerr)
	require.Nil(t, got.AuthorID)
	orphan, aerr := st.GetAuthorByName("Selina Kyle")
	require.NoError(t, aerr)
	require.Nil(t, orphan, "no author row may be created when locks cannot be read")
}

// The advisory read before the resolve can go stale: a lock saved after it is
// caught by the read under the book's write lock, and the book is left alone.
func TestLinkTagAuthors_LockSavedAfterTheAdvisoryReadStillHoldsTheWrite(t *testing.T) {
	st := &lockFlipStore{PebbleStore: newRefetchPebble(t), lockAfter: 1}
	b, err := st.CreateBook(&database.Book{Title: "Year One", FilePath: "/l/y1.m4b", Format: "m4b"})
	require.NoError(t, err)

	out, err := linkTagAuthors(st, b.ID, "Selina Kyle")
	require.NoError(t, err)
	require.Equal(t, tagAuthorsHeld, out)
	got, gerr := st.GetBookByID(b.ID)
	require.NoError(t, gerr)
	require.Nil(t, got.AuthorID, "the lock read under the write lock must hold the write")
}
