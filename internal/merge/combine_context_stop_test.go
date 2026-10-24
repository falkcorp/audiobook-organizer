// file: internal/merge/combine_context_stop_test.go
// version: 1.0.1
// guid: 7d0c4e19-b2a6-4f83-9a15-e6c1d8f3a057
// last-edited: 2026-10-24

package merge

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// cancelAfterBulkMoveStore applies MoveBookFilesToBookBulk for real and then
// ends the combine's context: the op's timeout (or a shutdown) landing in the
// one window where every absorbed book that had files has none, and none has
// been retired yet.
type cancelAfterBulkMoveStore struct {
	database.Store
	cancel context.CancelFunc
	done   atomic.Bool
}

func (s *cancelAfterBulkMoveStore) Unwrap() database.Store { return s.Store }

func (s *cancelAfterBulkMoveStore) MoveBookFilesToBookBulk(moves []database.BookFileMove, target string) error {
	err := s.Store.MoveBookFilesToBookBulk(moves, target)
	s.cancel()
	s.done.Store(true)
	return err
}

// cancelled reports whether the bulk move ran and the context was ended.
func (s *cancelAfterBulkMoveStore) cancelled() bool { return s.done.Load() }

// A combine whose context ends right after the bulk file move must not leave an
// absorbed book live with no files. The per-book loop used to stop at the next
// context check, which sits after the move: every absorbed book that had files
// then stayed live and empty, in the library, until someone reverted the
// operation.
//
// The virtual-file book (absB: no book_file rows, nothing moved) is listed
// BEFORE the one with files, so a loop that only skipped "the books with nothing
// moved" would still strand the later one.
func TestCombineBooks_ContextEndedAfterTheBulkMoveStillRetiresEveryMovedBook(t *testing.T) {
	store := setupTestStore(t)
	f := seedUndoFixture(t, store)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ms := NewService(&cancelAfterBulkMoveStore{Store: store, cancel: cancel})

	res, err := ms.CombineBooksWithOptions([]string{f.survivor, f.absB, f.absA}, f.survivor, nil, CombineOptions{Context: ctx})

	require.Nil(t, res)
	require.ErrorIs(t, err, context.Canceled)
	incomplete, ok := errors.AsType[*CombineIncompleteError](err)
	require.True(t, ok, "a stop after the undo pointer is journaled is a CombineIncompleteError: %v", err)

	// The book whose files moved is complete: retired, owns nothing, and its
	// files are the survivor's.
	moved, err := store.GetBookByID(f.absA)
	require.NoError(t, err)
	require.NotNil(t, moved)
	require.True(t, moved.IsSoftDeleted(), "absorbed book %s was left live with no files", f.absA)
	left, err := store.GetBookFiles(f.absA)
	require.NoError(t, err)
	require.Empty(t, left)
	onSurvivor, err := store.GetBookFiles(f.survivor)
	require.NoError(t, err)
	require.Len(t, onSurvivor, 2+1, "the two moved files and the survivor's own virtual file")

	// The book the move did not touch is exactly as it was: live, its own path.
	untouched, err := store.GetBookByID(f.absB)
	require.NoError(t, err)
	require.NotNil(t, untouched)
	require.False(t, untouched.IsSoftDeleted())
	require.Equal(t, "/tmp/undo/b.mp3", untouched.FilePath)

	// The journal is Pending, names only what ran, and the revert puts it back.
	j, err := ms.GetCombineJournal(incomplete.JournalID)
	require.NoError(t, err)
	require.Equal(t, CombineJournalPending, j.Status)
	require.Len(t, j.Absorbed, 1)
	require.Equal(t, f.absA, j.Absorbed[0].BookID)

	_, err = ms.UndoCombine(incomplete.JournalID)
	require.NoError(t, err)
	back, err := store.GetBookByID(f.absA)
	require.NoError(t, err)
	require.False(t, back.IsSoftDeleted())
	restored, err := store.GetBookFiles(f.absA)
	require.NoError(t, err)
	require.Len(t, restored, 2)
}

// A context that is already done refuses the combine before the journal is
// written: nothing is changed and nothing is left to revert.
func TestCombineBooks_ContextAlreadyDoneRefusesBeforeTheFirstWrite(t *testing.T) {
	store := setupTestStore(t)
	f := seedUndoFixture(t, store)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	res, err := NewService(store).CombineBooksWithOptions([]string{f.survivor, f.absA, f.absB}, f.survivor, nil, CombineOptions{Context: ctx})

	require.Nil(t, res)
	require.ErrorIs(t, err, context.Canceled)
	_, isIncomplete := errors.AsType[*CombineIncompleteError](err)
	require.False(t, isIncomplete, "no undo pointer exists to hand back: %v", err)
	for _, id := range []string{f.absA, f.absB} {
		b, gerr := store.GetBookByID(id)
		require.NoError(t, gerr)
		require.False(t, b.IsSoftDeleted(), "%s", id)
	}
	files, err := store.GetBookFiles(f.absA)
	require.NoError(t, err)
	require.Len(t, files, 2, "no file moved")
}

// countJournalWritesAfterStopStore counts the combine-journal writes made once
// the bulk move ran and the context ended.
type countJournalWritesAfterStopStore struct {
	*cancelAfterBulkMoveStore
	writes int
}

func (s *countJournalWritesAfterStopStore) SetRaw(key string, value []byte) error {
	if strings.HasPrefix(key, combineJournalKey("")) && s.cancelAfterBulkMoveStore.cancelled() {
		s.writes++
	}
	return s.cancelAfterBulkMoveStore.Store.SetRaw(key, value)
}

// A book the stop left untouched costs no journal write: nothing was written for
// it, so there is nothing to persist about it, and a write that failed there
// would return before the books whose files moved are retired. The combine's
// writes after the stop are the same with and without such a book in it.
func TestCombineBooks_ContextEndedNoJournalWriteForAnUntouchedBook(t *testing.T) {
	writesAfterStop := func(withUntouched bool) int {
		store := setupTestStore(t)
		f := seedUndoFixture(t, store)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		cs := &countJournalWritesAfterStopStore{cancelAfterBulkMoveStore: &cancelAfterBulkMoveStore{Store: store, cancel: cancel}}
		ids := []string{f.survivor, f.absA}
		if withUntouched {
			ids = []string{f.survivor, f.absB, f.absA}
		}

		_, err := NewService(cs).CombineBooksWithOptions(ids, f.survivor, nil, CombineOptions{Context: ctx})

		require.ErrorIs(t, err, context.Canceled)
		moved, gerr := store.GetBookByID(f.absA)
		require.NoError(t, gerr)
		require.True(t, moved.IsSoftDeleted(), "the moved book %s was stranded live with no files", f.absA)
		return cs.writes
	}

	without := writesAfterStop(false)
	require.Positive(t, without, "fixture: the stop is followed by journal writes")
	require.Equal(t, without, writesAfterStop(true), "an untouched book adds no journal write")
}
