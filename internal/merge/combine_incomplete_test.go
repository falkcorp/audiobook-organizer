// file: internal/merge/combine_incomplete_test.go
// version: 1.3.0
// guid: 3d698376-7775-4e75-b18b-bdb85372ac0e
// last-edited: 2026-10-26

package merge

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

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
	// Round-4 N6: the message itself names the ids, for callers that only
	// wrap or log it.
	require.ErrorContains(t, err, "journal "+incomplete.JournalID)
	require.ErrorContains(t, err, "operation op-incomplete")
	wrapped := fmt.Errorf("regroup multidisc apply: combine %q: %w", "/lib/x", err)
	require.Contains(t, wrapped.Error(), incomplete.JournalID, "a wrapping caller keeps the ids")

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

// cancelAfterRetire cancels a context once the named absorbed book has been
// retired, i.e. between two absorbed books of the combine.
type cancelAfterRetire struct {
	*database.PebbleStore
	after  string
	cancel context.CancelFunc
}

func (s cancelAfterRetire) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	b, err := s.PebbleStore.ModifyBook(id, fn)
	if err == nil && id == s.after {
		s.cancel()
	}
	return b, err
}

// N-b: the combine op's timeout (or a shutdown) cancels its context; the
// combine then stops BEFORE the next absorbed book with the merge lock
// released, as the part-way stop it already journals: an incomplete error
// wrapping the context error, a Pending journal, and an undo that puts back
// every file, including the books that were never reached.
func TestCombineBooks_ContextDoneStopsBetweenBooksAndIsUndone(t *testing.T) {
	store := setupTestStore(t).(*database.PebbleStore)
	keep, first, second := ulid.Make().String(), ulid.Make().String(), ulid.Make().String()
	for _, id := range []string{keep, first, second} {
		_, err := store.CreateBook(&database.Book{ID: id, Title: "B", Format: "mp3", FilePath: fmt.Sprintf("/tmp/ctxstop/%s.mp3", id)})
		require.NoError(t, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Order of absorption is the journal's; cancel after whichever goes first.
	svc := NewService(cancelAfterRetire{store, first, cancel})
	_, err := svc.CombineBooksWithOptions([]string{keep, first, second}, keep, nil,
		CombineOptions{OperationID: "op-ctxstop", Context: ctx})
	var incomplete *CombineIncompleteError
	require.ErrorAs(t, err, &incomplete)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, "op-ctxstop", incomplete.OperationID)

	// The lock is free again: a stopped combine must not keep holding it.
	unlock, lerr := TryLockMergeRMWWithin(time.Second)
	require.NoError(t, lerr)
	unlock()

	j, err := NewService(store).GetCombineJournal(incomplete.JournalID)
	require.NoError(t, err)
	require.Equal(t, CombineJournalPending, j.Status)

	// The book the combine never reached keeps its single-file audio as a
	// virtual segment: nothing of it was attached, retired or journaled.
	b, err := store.GetBookByID(second)
	require.NoError(t, err)
	require.False(t, b.IsSoftDeleted(), "the unreached book was not retired")
	b, err = store.GetBookByID(first)
	require.NoError(t, err)
	require.True(t, b.IsSoftDeleted(), "fixture: the first absorbed book was retired before the stop")

	_, err = NewService(store).UndoCombine(incomplete.JournalID)
	require.NoError(t, err)
	b, err = store.GetBookByID(first)
	require.NoError(t, err)
	require.False(t, b.IsSoftDeleted(), "the undo restores the book retired before the stop")
	files, err := store.GetBookFiles(first)
	require.NoError(t, err)
	require.Len(t, files, 1, "and gives it its file back")
	b, err = store.GetBookByID(second)
	require.NoError(t, err)
	require.False(t, b.IsSoftDeleted())
	require.Equal(t, fmt.Sprintf("/tmp/ctxstop/%s.mp3", second), b.FilePath, "the unreached book is untouched by the undo")
}

// A nil Context (every caller but the op) never stops the combine.
func TestCombineBooks_NilContextRunsToTheEnd(t *testing.T) {
	store := setupTestStore(t).(*database.PebbleStore)
	keep, gone := ulid.Make().String(), ulid.Make().String()
	for _, id := range []string{keep, gone} {
		_, err := store.CreateBook(&database.Book{ID: id, Title: "B", Format: "mp3", FilePath: fmt.Sprintf("/tmp/nilctx/%s.mp3", id)})
		require.NoError(t, err)
	}
	res, err := NewService(store).CombineBooksWithOptions([]string{keep, gone}, keep, nil, CombineOptions{})
	require.NoError(t, err)
	require.Equal(t, 1, res.BooksDeleted)
}
