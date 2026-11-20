// file: internal/books/store/book_file_unless_path_held_test.go
// version: 1.0.0
// guid: 3e8a51c4-7d02-4f96-b1a8-c40d9e27f653
// last-edited: 2026-11-19

package store

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// unlessHeldFixture is a book with one row at from and a second book that a test
// creates the intruding row under.
type unlessHeldFixture struct {
	ps       *PebbleStore
	book     *Book
	other    *Book
	row      *BookFile
	from, to string
}

func newUnlessHeldFixture(t *testing.T) *unlessHeldFixture {
	t.Helper()
	st, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	ps := AsPebbleStore(st)
	require.NotNil(t, ps)
	book, err := ps.CreateBook(&Book{Title: "Batman: Year One", FilePath: "/lib/unless/year-one"})
	require.NoError(t, err)
	other, err := ps.CreateBook(&Book{Title: "Batman: The Long Halloween", FilePath: "/lib/unless/long-halloween"})
	require.NoError(t, err)
	row := &BookFile{BookID: book.ID, FilePath: "/lib/unless/year-one/missing.m4b", Format: "m4b"}
	require.NoError(t, ps.CreateBookFile(row))
	return &unlessHeldFixture{ps: ps, book: book, other: other, row: row,
		from: row.FilePath, to: "/lib/unless/year-one/found.m4b"}
}

func (f *unlessHeldFixture) moveTo(path string) func(*BookFile) error {
	return func(r *BookFile) error { r.FilePath = path; return nil }
}

// intrudeAtCommit makes another book's row appear at path after the callback ran
// and before the commit takes any lock: the window the caller's own pre-check can
// never see. The row id avoids the moved row's stripe, which the writer holds.
func (f *unlessHeldFixture) intrudeAtCommit(t *testing.T, path string) *BookFile {
	t.Helper()
	intruder := &BookFile{ID: distinctStripeID(t, f.row.ID, f.book.ID, f.other.ID), BookID: f.other.ID, FilePath: path, Format: "m4b"}
	var fired atomic.Bool
	bookFileBeforeCommitHook = func() {
		if fired.Swap(true) {
			return
		}
		require.NoError(t, f.ps.CreateBookFile(intruder))
	}
	t.Cleanup(func() { bookFileBeforeCommitHook = nil })
	return intruder
}

// The row that took the path inside the window keeps its single-owner key; the
// move is refused and writes nothing. Plain ModifyBookFile, run on the same
// setup, takes the key from the intruder: that is the state the guard exists to
// prevent, and it makes the guarded case's pass mean something.
func TestModifyBookFileUnlessPathHeld_RefusesARowThatTookThePathBeforeTheCommit(t *testing.T) {
	f := newUnlessHeldFixture(t)
	intruder := f.intrudeAtCommit(t, f.to)

	got, err := f.ps.ModifyBookFileUnlessPathHeld(f.book.ID, f.row.ID, f.moveTo(f.to))
	require.ErrorIs(t, err, ErrBookFilePathHeld)
	require.Nil(t, got)

	require.Equal(t, intruder.BookID+":"+intruder.ID, indexOwner(t, f.ps, pathKeyOf(f.to)), "the intruder keeps the key")
	cur, err := f.ps.getBookFileByID(f.book.ID, f.row.ID)
	require.NoError(t, err)
	require.Equal(t, f.from, cur.FilePath, "the refused row did not move")
}

func TestModifyBookFile_PlainWriteTakesThePathKeyFromARowThatTookItBeforeTheCommit(t *testing.T) {
	f := newUnlessHeldFixture(t)
	f.intrudeAtCommit(t, f.to)

	_, err := f.ps.ModifyBookFile(f.book.ID, f.row.ID, f.moveTo(f.to))
	require.NoError(t, err)
	require.Equal(t, f.book.ID+":"+f.row.ID, indexOwner(t, f.ps, pathKeyOf(f.to)),
		"two rows now hold %s, the key names %s", f.to, f.row.ID)
}

// A free path moves like ModifyBookFile, and the key follows the row.
func TestModifyBookFileUnlessPathHeld_MovesToAFreePath(t *testing.T) {
	f := newUnlessHeldFixture(t)
	got, err := f.ps.ModifyBookFileUnlessPathHeld(f.book.ID, f.row.ID, f.moveTo(f.to))
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, f.to, got.FilePath)
	require.Equal(t, f.book.ID+":"+f.row.ID, indexOwner(t, f.ps, pathKeyOf(f.to)))
}

// A key that names a row which has since left the path holds nothing.
func TestModifyBookFileUnlessPathHeld_AStaleKeyHoldsNothing(t *testing.T) {
	f := newUnlessHeldFixture(t)
	gone := &BookFile{BookID: f.other.ID, FilePath: f.to, Format: "m4b"}
	require.NoError(t, f.ps.CreateBookFile(gone))
	_, err := f.ps.ModifyBookFile(f.other.ID, gone.ID, f.moveTo("/lib/unless/long-halloween/elsewhere.m4b"))
	require.NoError(t, err)

	_, err = f.ps.ModifyBookFileUnlessPathHeld(f.book.ID, f.row.ID, f.moveTo(f.to))
	require.NoError(t, err)
	require.Equal(t, f.book.ID+":"+f.row.ID, indexOwner(t, f.ps, pathKeyOf(f.to)))
}

// A row that holds the path already (a second matched file's row at it) is
// refused before the callback's own check could run, retired or live: the
// reconcile guard refuses any holder.
func TestModifyBookFileUnlessPathHeld_RefusesAHolderThatIsAlreadyThere(t *testing.T) {
	f := newUnlessHeldFixture(t)
	require.NoError(t, f.ps.CreateBookFile(&BookFile{BookID: f.other.ID, FilePath: f.to, Format: "m4b"}))
	_, err := f.ps.ModifyBookFileUnlessPathHeld(f.book.ID, f.row.ID, f.moveTo(f.to))
	require.ErrorIs(t, err, ErrBookFilePathHeld)
}

// A write that does not change the path is not a move: no check, no refusal,
// even when the path key names another row.
func TestModifyBookFileUnlessPathHeld_ASamePathWriteIsNotChecked(t *testing.T) {
	f := newUnlessHeldFixture(t)
	got, err := f.ps.ModifyBookFileUnlessPathHeld(f.book.ID, f.row.ID, func(r *BookFile) error { r.Format = "mp3"; return nil })
	require.NoError(t, err)
	require.Equal(t, "mp3", got.Format)
	got, err = f.ps.ModifyBookFileUnlessPathHeld(f.book.ID, f.row.ID, func(*BookFile) error { return ErrSkipBookFileWrite })
	require.NoError(t, err)
	require.Equal(t, f.from, got.FilePath)
}

// The check and the apply are one step with respect to every writer of a path
// key: a row created at the path between verify and apply is held out until the
// move has committed (the creator must reach the single-owner mutex, and cannot
// finish inside the window). Without the check under the mutex a creator could
// commit in the window and the move would still take its key.
func TestModifyBookFileUnlessPathHeld_HoldsWritersOutOfTheVerifyApplyWindow(t *testing.T) {
	f := newUnlessHeldFixture(t)
	creator := &BookFile{ID: distinctStripeID(t, f.row.ID, f.book.ID, f.other.ID), BookID: f.other.ID, FilePath: f.to, Format: "m4b"}

	done := make(chan error, 1)
	var reachedMutex, finishedInsideWindow atomic.Bool
	installSingleOwnerHooks(t, func(contended <-chan struct{}) {
		go func() { done <- f.ps.CreateBookFile(creator) }()
		select {
		case <-contended:
			reachedMutex.Store(true)
		case err := <-done:
			finishedInsideWindow.Store(true)
			done <- err
		case <-time.After(10 * time.Second):
		}
	})
	_, err := f.ps.ModifyBookFileUnlessPathHeld(f.book.ID, f.row.ID, f.moveTo(f.to))
	require.NoError(t, err)
	require.False(t, finishedInsideWindow.Load(), "a creator committed inside the move's verify-to-apply window")
	require.True(t, reachedMutex.Load(), "the creator never reached the single-owner index mutex while the move held it")
	select {
	case <-done: // what CreateBookFile does at a held path is its own contract
	case <-time.After(10 * time.Second):
		t.Fatal("the creator never finished after the move released the mutex")
	}
	cur, gerr := f.ps.getBookFileByID(f.book.ID, f.row.ID)
	require.NoError(t, gerr)
	require.Equal(t, f.to, cur.FilePath)
}
