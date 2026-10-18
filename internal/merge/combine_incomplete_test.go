// file: internal/merge/combine_incomplete_test.go
// version: 1.1.0
// guid: 3d698376-7775-4e75-b18b-bdb85372ac0e
// last-edited: 2026-10-18

package merge

import (
	"errors"
	"fmt"
	"testing"

	ulid "github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// failAbsorbedRetire fails the soft-delete of one absorbed book, after the
// combine has already moved every file onto the survivor.
type failAbsorbedRetire struct {
	*database.PebbleStore
	absorbed string
	// landed: the soft-delete commits, then the call reports an error.
	landed bool
}

func (s failAbsorbedRetire) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	if id == s.absorbed {
		if s.landed {
			if _, err := s.PebbleStore.ModifyBook(id, fn); err != nil {
				return nil, err
			}
		}
		return nil, errors.New("disk full")
	}
	return s.PebbleStore.ModifyBook(id, fn)
}

// A combine that fails after its undo pointer is journaled returns a
// *CombineIncompleteError naming the journal and the operation, and that
// operation's journal undoes what ran. A failure before the pointer (nothing
// written) is a plain error with no ids.
func TestCombineBooks_FailureAfterPointerCarriesTheUndoIDs(t *testing.T) {
	store := setupTestStore(t).(*database.PebbleStore)
	keep, gone := ulid.Make().String(), ulid.Make().String()
	for _, id := range []string{keep, gone} {
		_, err := store.CreateBook(&database.Book{ID: id, Title: "B", Format: "mp3", FilePath: fmt.Sprintf("/tmp/incomplete/%s.mp3", id)})
		require.NoError(t, err)
	}

	_, err := NewService(store).CombineBooksWithOptions([]string{keep, gone}, "not-there", nil, CombineOptions{})
	var incomplete *CombineIncompleteError
	require.Error(t, err)
	require.False(t, errors.As(err, &incomplete), "a combine refused before its pointer carries no undo ids: %v", err)

	_, err = NewService(failAbsorbedRetire{store, gone, false}).CombineBooksWithOptions([]string{keep, gone}, keep, nil, CombineOptions{OperationID: "op-incomplete"})
	require.ErrorAs(t, err, &incomplete)
	require.Equal(t, "op-incomplete", incomplete.OperationID)
	require.NotEmpty(t, incomplete.JournalID)
	require.ErrorContains(t, err, "disk full", "the cause is kept")

	ms := NewService(store)
	j, err := ms.GetCombineJournal(incomplete.JournalID)
	require.NoError(t, err)
	require.Equal(t, CombineJournalPending, j.Status)
	files, err := store.GetBookFiles(keep)
	require.NoError(t, err)
	require.Len(t, files, 2, "the combine moved the absorbed book's file before it stopped")

	_, err = ms.UndoCombine(incomplete.JournalID)
	require.NoError(t, err)
	files, err = store.GetBookFiles(gone)
	require.NoError(t, err)
	require.Len(t, files, 1, "the undo the error points at moves the file back")
}

// NIT 2: a soft-delete of an absorbed book that committed and then reported an
// error. The journaled stamp is kept, so the undo finds the retired book
// carrying it and restores it (the split-book merge keeps it the same way).
func TestCombineBooks_SoftDeleteLandedThenFailedIsUndone(t *testing.T) {
	store := setupTestStore(t).(*database.PebbleStore)
	keep, gone := ulid.Make().String(), ulid.Make().String()
	for _, id := range []string{keep, gone} {
		_, err := store.CreateBook(&database.Book{ID: id, Title: "B", Format: "mp3", FilePath: fmt.Sprintf("/tmp/landed/%s.mp3", id)})
		require.NoError(t, err)
	}
	_, err := NewService(failAbsorbedRetire{store, gone, true}).CombineBooksWithOptions([]string{keep, gone}, keep, nil, CombineOptions{OperationID: "op-landed"})
	var incomplete *CombineIncompleteError
	require.ErrorAs(t, err, &incomplete)
	b, err := store.GetBookByID(gone)
	require.NoError(t, err)
	require.True(t, b.IsSoftDeleted(), "fixture: the soft-delete landed")

	_, err = NewService(store).UndoCombine(incomplete.JournalID)
	require.NoError(t, err)
	b, err = store.GetBookByID(gone)
	require.NoError(t, err)
	require.False(t, b.IsSoftDeleted(), "the undo restores the book the failed call did retire")
	files, err := store.GetBookFiles(gone)
	require.NoError(t, err)
	require.Len(t, files, 1)
}
