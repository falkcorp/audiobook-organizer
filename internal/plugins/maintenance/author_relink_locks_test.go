// file: internal/plugins/maintenance/author_relink_locks_test.go
// version: 1.0.0
// guid: 0c4f8a36-b2d9-4e71-8a05-7d3e9b1f6c42
// last-edited: 2026-11-18

package maintenance

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

const relinkAllParams = `{"apply":true,"relink_title_as_author":true,"delete_title_as_author":true}`

// lockedRelinkFixture is the Arcane Chef shape with both books named by their
// provider, so each would be relinked to the existing real author.
func lockedRelinkFixture() *relinkFixture {
	f := newRelinkFixture()
	f.fetched["bk-t"] = "T.J. Ward"
	f.fetched["bk-s"] = "T.J. Ward"
	return f
}

func requireBookUntouched(t *testing.T, f *relinkFixture, calls *stripMergeCalls, id string) {
	t.Helper()
	require.Nil(t, calls.setAuthors[id], "the credits of %s were rewritten", id)
	_, ok := calls.updated[id]
	require.False(t, ok, "the primary author of %s was rewritten", id)
	for _, c := range f.journal {
		require.NotEqual(t, id, c.BookID, "a journal row was written for the held book %s", id)
	}
}

// A book whose author the user locked is not relinked, journaled or given a
// created author; its junk row stays because a book still credits it. The
// unlocked book beside it is relinked, so the hold is per book.
func TestAuthorStripMerge_RelinkUserLockedAuthorIsLeftAlone(t *testing.T) {
	f := lockedRelinkFixture()
	f.locked = map[string]bool{"bk-t": true}
	calls, summary := f.run(t, relinkAllParams)

	requireBookUntouched(t, f, calls, "bk-t")
	require.Equal(t, []int{300}, credits(calls, "bk-s"), "control: the unlocked book was not relinked")
	require.NotContains(t, calls.deleted, 100, "the junk row was deleted although a locked book still credits it")
	wantSummary(t, summary, "relink-skipped-user-locked=1 ", "relinked=1 ", "relink-failed=0 ")
}

// A lock set that cannot be read fails the book: nothing is written for it.
func TestAuthorStripMerge_RelinkUnreadableLocksWriteNothing(t *testing.T) {
	f := lockedRelinkFixture()
	f.lockErr = map[string]error{"bk-t": fmt.Errorf("field states for bk-t: store closed")}
	calls, summary := f.run(t, relinkAllParams)

	requireBookUntouched(t, f, calls, "bk-t")
	require.Equal(t, []int{300}, credits(calls, "bk-s"), "control: the other book was not relinked")
	wantSummary(t, summary, "relink-failed=1 ", "relink-skipped-user-locked=0 ")
}

// The first field-state reads are the advisory check and the candidate names;
// the third is relinkBookCredit's own check before it writes. A lock saved
// between them holds the whole move: no credit and no primary is written.
func TestAuthorStripMerge_RelinkLockSavedBeforeTheWriteHoldsTheCredits(t *testing.T) {
	f := lockedRelinkFixture()
	f.lockFlipAt = map[string]int{"bk-t": 3}
	calls, summary := f.run(t, relinkAllParams)

	require.Nil(t, calls.setAuthors["bk-t"], "the credits were written although the author was locked before the write")
	_, ok := calls.updated["bk-t"]
	require.False(t, ok, "the primary was written although the author was locked before the write")
	wantSummary(t, summary, "relink-skipped-user-locked=1 ", "relinked=1 ")
}

// The fourth read is the primary-author write's own, under the book's write lock.
// A lock saved after the credits were written still holds the primary write.
func TestAuthorStripMerge_RelinkLockSavedBeforeThePrimaryWriteHoldsIt(t *testing.T) {
	f := lockedRelinkFixture()
	f.lockFlipAt = map[string]int{"bk-t": 4}
	calls, summary := f.run(t, relinkAllParams)

	_, ok := calls.updated["bk-t"]
	require.False(t, ok, "the primary was written although the author was locked before the primary write")
	// The credits had already moved when the lock landed; they are put back, or
	// the book would credit the new author while its primary names the old row.
	require.NotContains(t, credits(calls, "bk-t"), 300, "the credits stayed moved after the primary write was held")
	require.NotNil(t, calls.setAuthors["bk-t"], "the credit write was not rolled back")
	require.NotContains(t, calls.deleted, 100, "the junk row was deleted although the locked book still has it as its primary")
	wantSummary(t, summary, "relink-skipped-user-locked=1 ", "relinked=1 ")
}

type mergeLockStore struct {
	t6g1HeadStore
	unreadable map[string]bool
}

func (s mergeLockStore) GetMetadataFieldStates(id string) ([]database.MetadataFieldState, error) {
	if s.unreadable[id] {
		return nil, fmt.Errorf("field states for %s: store closed", id)
	}
	return s.t6g1HeadStore.GetMetadataFieldStates(id)
}

func lockAuthor(t *testing.T, st *database.PebbleStore, bookID string) {
	t.Helper()
	require.NoError(t, st.UpsertMetadataFieldState(&database.MetadataFieldState{
		BookID: bookID, Field: database.FieldKeyAuthorName, OverrideLocked: true,
	}))
}

func requireMergeHeld(t *testing.T, w *mergeUndoWorld, heldBook, movedBook string) {
	t.Helper()
	credits, err := w.st.GetBookAuthors(heldBook)
	require.NoError(t, err)
	require.Equal(t, w.credits[heldBook], credits, "the held book's credits were rewritten")
	b, err := w.st.GetBookByID(heldBook)
	require.NoError(t, err)
	require.Equal(t, w.primaries[heldBook], *b.AuthorID, "the held book's primary was rewritten")
	if movedBook != "" {
		moved, err := w.st.GetBookAuthors(movedBook)
		require.NoError(t, err)
		for _, c := range moved {
			require.NotEqual(t, w.from.ID, c.AuthorID, "control: the unlocked book still credits the merged author")
		}
	}
	kept, err := w.st.GetAuthorByID(w.from.ID)
	require.NoError(t, err)
	require.NotNil(t, kept)
	require.Equal(t, w.from.ID, kept.ID, "the author was deleted although a locked book still credits it")
	require.Equal(t, w.from.Name, kept.Name)
}

// An author merge leaves a book whose author the user locked alone and keeps
// the author row (the book still credits it), reporting the hold as an error
// that names the cause; the unlocked book is merged.
func TestMergeAuthorInto_UserLockedAuthorBookIsLeftAndAuthorKept(t *testing.T) {
	w := newMergeUndoWorld(t, "Undo Lock From", "Undo Lock Into", 0)
	lockAuthor(t, w.st, w.books[0])

	n, _, err := mergeAuthorIntoVia(context.Background(), t6g1HeadStore{w.st}, t6g1HeadWriter{w.st}, w.from, w.into, false, "op-merge-lock", slog.Default())

	require.Error(t, err)
	require.True(t, errors.Is(err, errAuthorMergeUserLocked), "err = %v", err)
	require.Equal(t, 1, n, "the unlocked book was not moved")
	requireMergeHeld(t, w, w.books[0], w.books[1])
}

// A preview reports the same hold, so it does not promise a merge the apply
// cannot finish, and writes nothing.
func TestMergeAuthorInto_PreviewReportsTheUserLockedBook(t *testing.T) {
	w := newMergeUndoWorld(t, "Undo Lock Prev From", "Undo Lock Prev Into", 0)
	lockAuthor(t, w.st, w.books[0])

	_, _, err := mergeAuthorIntoVia(context.Background(), t6g1HeadStore{w.st}, t6g1HeadWriter{w.st}, w.from, w.into, true, "", slog.Default())

	require.True(t, errors.Is(err, errAuthorMergeUserLocked), "err = %v", err)
	requireMergeHeld(t, w, w.books[0], "")
	for _, id := range w.books {
		credits, gerr := w.st.GetBookAuthors(id)
		require.NoError(t, gerr)
		require.Equal(t, w.credits[id], credits, "the preview wrote book %s", id)
	}
}

// A lock set that cannot be read stops the merge before that book is moved and
// before the delete, like a failed credit move does.
func TestMergeAuthorInto_UnreadableLocksMoveNothingAndKeepTheAuthor(t *testing.T) {
	w := newMergeUndoWorld(t, "Undo Lock Err From", "Undo Lock Err Into", 0)
	store := mergeLockStore{t6g1HeadStore: t6g1HeadStore{w.st}, unreadable: map[string]bool{w.books[0]: true}}

	_, _, err := mergeAuthorIntoVia(context.Background(), store, t6g1HeadWriter{w.st}, w.from, w.into, false, "op-merge-lock-err", slog.Default())

	require.Error(t, err)
	require.False(t, errors.Is(err, errAuthorMergeUserLocked), "an unreadable lock was reported as a hold: %v", err)
	requireMergeHeld(t, w, w.books[0], "")
}
