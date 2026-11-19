// file: internal/plugins/maintenance/author_relink_locks_test.go
// version: 1.2.1
// guid: 0c4f8a36-b2d9-4e71-8a05-7d3e9b1f6c42
// last-edited: 2026-11-19

package maintenance

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

const relinkAllParams = `{"apply":true,"relink_title_as_author":true,"delete_title_as_author":true}`

// batmanRelinkFixture is the title-as-author shape in Gotham: the junk row T 100
// "Hush 2" credits only its self-titled book, its numbered twin S 200 "01 Hush 2"
// strips to T's name, and the real author Alfred Pennyworth (300) already has a
// row. Both junk books are named by their provider, so each would be relinked to
// Alfred. Fixtures here are Batman storylines and characters only.
func batmanRelinkFixture() *relinkFixture {
	f := &relinkFixture{
		books: []database.BookCore{
			{ID: "bk-t", Title: "Hush 2: A Gotham Adventure", AuthorID: new(100), FilePath: "/lib/Hush 2/Hush 2"},
			{ID: "bk-s", Title: "Hush 2", AuthorID: new(200), FilePath: "/lib/01 Hush 2/Hush 2"},
			{ID: "bk-real", Title: "Hush", AuthorID: new(300), FilePath: "/lib/Alfred Pennyworth/Hush"},
		},
		files:   map[string][]database.BookFile{},
		fetched: map[string]string{},
		authors: []database.Author{
			{ID: 100, Name: "Hush 2"},
			{ID: 200, Name: "01 Hush 2"},
			{ID: 300, Name: "Alfred Pennyworth"},
		},
	}
	f.fetched["bk-t"] = "Alfred Pennyworth"
	f.fetched["bk-s"] = "Alfred Pennyworth"
	return f
}

func requireBookUntouched(t *testing.T, f *relinkFixture, calls *stripMergeCalls, id string) {
	t.Helper()
	require.Nil(t, calls.setAuthors[id], "the credits of %s were rewritten", id)
	_, ok := calls.updated[id]
	require.False(t, ok, "the primary author of %s was rewritten", id)
	requireJournalVoided(t, f, id)
}

// requireJournalVoided: a row journaled before a write that did not happen must
// be voided (the journal is written first, so a held or failed book has rows;
// they must describe nothing). Voiding rewrites the row under its own id, so the
// last row seen per id is the one that stands.
func requireJournalVoided(t *testing.T, f *relinkFixture, id string) {
	t.Helper()
	last := map[string]database.OperationChange{}
	for _, c := range f.journal {
		if c.BookID == id {
			last[c.ID] = c
		}
	}
	for rowID, c := range last {
		require.True(t, c.Voided, "journal row %s (%s) of the untouched book %s stands: it describes a write that did not happen", rowID, c.ChangeType, id)
	}
}

// A book whose author the user locked is not relinked, journaled or given a
// created author; its junk row stays because a book still credits it. The
// unlocked book beside it is relinked, so the hold is per book.
func TestAuthorStripMerge_RelinkUserLockedAuthorIsLeftAlone(t *testing.T) {
	f := batmanRelinkFixture()
	f.locked = map[string]bool{"bk-t": true}
	calls, summary := f.run(t, relinkAllParams)

	requireBookUntouched(t, f, calls, "bk-t")
	require.Equal(t, []int{300}, credits(calls, "bk-s"), "control: the unlocked book was not relinked")
	require.NotContains(t, calls.deleted, 100, "the junk row was deleted although a locked book still credits it")
	wantSummary(t, summary, "relink-skipped-user-locked=1 ", "relinked=1 ", "relink-failed=0 ")
}

// The same op then deletes the junk row, and a book that still credits it is
// unlinked first. A locked book that has another author beside the junk row is
// not "held as a sole credit": without its own lock check the unlink would
// rewrite its credits and promote its primary (the AuthorID column the lock
// covers). It is held like a sole-credit book is: untouched, and the row kept.
func TestAuthorStripMerge_UnlinkLeavesAUserLockedBookWithACoAuthorAlone(t *testing.T) {
	f := deleteOnlyCoAuthorFixture()
	f.locked = map[string]bool{"bk-t": true}
	calls, summary := f.run(t, relinkAllParams)

	requireBookUntouched(t, f, calls, "bk-t")
	require.NotContains(t, calls.deleted, 100, "the junk row was deleted although a locked book still credits it")
	wantSummary(t, summary, "relink-skipped-user-locked=1 ", "books-held-user-locked=1 ", "rows-held-user-locked=1 ", "failed=0 ")
}

const deleteOnlyParams = `{"apply":true,"delete_title_as_author":true}`

// deleteOnlyCoAuthorFixture is the same shape with the relink off, so the delete
// reaches the unlink itself: bk-t credits the junk row beside a real co-author.
func deleteOnlyCoAuthorFixture() *relinkFixture {
	f := batmanRelinkFixture()
	f.extra = []database.Author{{ID: 400, Name: "Harley Quinn"}}
	f.coCredits = map[string][]database.BookAuthor{
		"bk-t": {{BookID: "bk-t", AuthorID: 400, Role: "author", Position: 1}},
	}
	return f
}

// Control for the tests below: with no lock the unlink rewrites the credits and
// the row is deleted, so they are exercising a path that does run.
func TestAuthorStripMerge_UnlinkUnlockedCoAuthorBookIsUnlinkedAndRowDeleted(t *testing.T) {
	f := deleteOnlyCoAuthorFixture()
	calls, _ := f.run(t, deleteOnlyParams)

	require.Equal(t, []int{400}, credits(calls, "bk-t"), "control: the junk credit was not removed")
	require.Contains(t, calls.deleted, 100, "control: the junk row was not deleted")
}

// An unreadable lock set stops the unlink before it writes or deletes anything.
func TestAuthorStripMerge_UnlinkUnreadableLocksWriteNothingAndKeepTheRow(t *testing.T) {
	f := deleteOnlyCoAuthorFixture()
	f.lockErr = map[string]error{"bk-t": errors.New("field-state store unavailable")}
	calls, summary := f.run(t, deleteOnlyParams)

	requireBookUntouched(t, f, calls, "bk-t")
	require.NotContains(t, calls.deleted, 100, "the junk row was deleted although a lock read failed")
	wantSummary(t, summary, "failed=1 ")
}

// A lock the user saves after every earlier check and before the unlink's one
// commit holds the book: the whole write is refused, so there is nothing to put
// back. (Round 1 wrote the credits and then the primary and restored the credits
// when the second write was held; that code is gone, and so is its window.)
func TestAuthorStripMerge_UnlinkLockSavedBeforeTheCommitHoldsTheBook(t *testing.T) {
	f := deleteOnlyCoAuthorFixture()
	f.lockAtWrite = map[string]bool{"bk-t": true}
	calls, summary := f.run(t, deleteOnlyParams)

	requireBookUntouched(t, f, calls, "bk-t")
	require.NotContains(t, calls.deleted, 100, "the junk row was deleted although the book was locked before the commit")
	wantSummary(t, summary, "books-held-user-locked=1 ", "rows-held-user-locked=1 ", "deleted=0 ", "failed=0 ")
}

// A lock set that cannot be read inside the commit is a failure, not a hold, and
// writes nothing.
func TestAuthorStripMerge_UnlinkUnreadableLocksInsideTheCommitWriteNothing(t *testing.T) {
	f := deleteOnlyCoAuthorFixture()
	f.lockErrAtWrite = map[string]error{"bk-t": errors.New("field states for bk-t: store closed")}
	calls, summary := f.run(t, deleteOnlyParams)

	requireBookUntouched(t, f, calls, "bk-t")
	require.NotContains(t, calls.deleted, 100, "the junk row was deleted although a lock read failed")
	wantSummary(t, summary, "failed=1 ", "books-held-user-locked=0 ")
}

// A lock set that cannot be read fails the book: nothing is written for it.
func TestAuthorStripMerge_RelinkUnreadableLocksWriteNothing(t *testing.T) {
	f := batmanRelinkFixture()
	f.lockErr = map[string]error{"bk-t": fmt.Errorf("field states for bk-t: store closed")}
	calls, summary := f.run(t, relinkAllParams)

	requireBookUntouched(t, f, calls, "bk-t")
	require.Equal(t, []int{300}, credits(calls, "bk-s"), "control: the other book was not relinked")
	wantSummary(t, summary, "relink-failed=1 ", "relink-skipped-user-locked=0 ")
}

// A lock saved after the relink's earlier checks and before its one commit holds
// the whole move: no credit and no primary is written, because they are one
// write. The held book's rows (journal included) are voided, not left standing.
func TestAuthorStripMerge_RelinkLockSavedBeforeTheCommitHoldsTheBook(t *testing.T) {
	f := batmanRelinkFixture()
	f.lockAtWrite = map[string]bool{"bk-t": true}
	calls, summary := f.run(t, relinkAllParams)

	requireBookUntouched(t, f, calls, "bk-t")
	require.Equal(t, []int{300}, credits(calls, "bk-s"), "control: the other book was not relinked")
	require.NotContains(t, calls.deleted, 100, "the junk row was deleted although the locked book still has it as its primary")
	wantSummary(t, summary, "relink-skipped-user-locked=1 ", "relinked=1 ", "relink-failed=0 ")
}

// The lock read inside the commit fails closed on its own: an error there is a
// failure (not a hold), and nothing was written for the book.
func TestAuthorStripMerge_RelinkUnreadableLocksInsideTheCommitWriteNothing(t *testing.T) {
	f := batmanRelinkFixture()
	f.lockErrAtWrite = map[string]error{"bk-t": errors.New("field states for bk-t: store closed")}
	calls, summary := f.run(t, relinkAllParams)

	requireBookUntouched(t, f, calls, "bk-t")
	require.NotContains(t, calls.deleted, 100, "the junk row was deleted although a book still has it")
	wantSummary(t, summary, "relink-failed=1 ", "relink-skipped-user-locked=0 ")
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

// An author merge is all or nothing per author: one book whose author the user
// locked holds the whole merge BEFORE any book moves, so the author is never left
// half merged. The held merge reports errAuthorMergeUserLocked (callers count it
// as held, not failed), moves no book, and keeps the author row.
func TestMergeAuthorInto_UserLockedAuthorBookHoldsTheWholeMerge(t *testing.T) {
	w := newMergeUndoWorld(t, "Joker Lock From", "Joker Lock Into", 0)
	lockAuthor(t, w.st, w.books[0])

	n, _, err := mergeAuthorIntoVia(context.Background(), t6g1HeadStore{w.st}, t6g1HeadWriter{w.st}, w.from, w.into, false, "op-merge-lock", slog.Default())

	require.Error(t, err)
	require.True(t, errors.Is(err, errAuthorMergeUserLocked), "err = %v", err)
	require.Equal(t, 0, n, "a held author moved a book")
	requireMergeHeld(t, w, w.books[0], "")
	requireMergeHeld(t, w, w.books[1], "")
}

// The co-author-only book is held by the author lock too: the junk credit is not
// its primary, but the credit list is part of what author_name locks.
func TestMergeAuthorInto_ALockedCoAuthorBookHoldsTheWholeMerge(t *testing.T) {
	w := newMergeUndoWorld(t, "Joker Lock Co From", "Joker Lock Co Into", 0)
	lockAuthor(t, w.st, w.books[1])

	n, _, err := mergeAuthorIntoVia(context.Background(), t6g1HeadStore{w.st}, t6g1HeadWriter{w.st}, w.from, w.into, false, "op-merge-lock-co", slog.Default())

	require.True(t, errors.Is(err, errAuthorMergeUserLocked), "err = %v", err)
	require.Equal(t, 0, n)
	requireMergeHeld(t, w, w.books[0], "")
	requireMergeHeld(t, w, w.books[1], "")
}

// A lock saved after the pre-scan holds that one book at the write: the book
// before it moved (and is counted so the caller invalidates its caches), the held
// book keeps its credits and primary, and the author row stays. Which of the two
// books is held is whichever the merge reaches second (lockAtSecondCommit), so the
// test does not depend on the order the store lists them in.
func TestMergeAuthorInto_ALockSavedMidRunHoldsThatBookAndCountsTheMovedOnes(t *testing.T) {
	w := newMergeUndoWorld(t, "Joker Late From", "Joker Late Into", 0)
	db, held := lockAtSecondCommit(t, w.st)

	n, _, err := mergeAuthorIntoVia(context.Background(), t6g1HeadStore{w.st}, t6g1HeadWriter{db}, w.from, w.into, false, "op-merge-lock-late", slog.Default())

	require.True(t, errors.Is(err, errAuthorMergeUserLocked), "err = %v", err)
	require.Equal(t, 1, n, "the book before the held one was not counted as moved")
	heldID := held()
	require.Contains(t, w.books, heldID, "the mid-run lock was never saved")
	moved := w.books[0]
	if heldID == moved {
		moved = w.books[1]
	}
	requireMergeHeld(t, w, heldID, moved)
}

// descendingListing lists a person's books by DESCENDING id, the order the sort in
// mergeAuthorIntoVia must undo.
type descendingListing struct{ t6g1HeadStore }

func (d descendingListing) GetBooksByAuthorIDForRelinkCore(authorID int) ([]database.BookCore, error) {
	books, err := d.t6g1HeadStore.GetBooksByAuthorIDForRelinkCore(authorID)
	slices.SortFunc(books, func(a, b database.BookCore) int { return strings.Compare(b.ID, a.ID) })
	return books, err
}

// The merge visits a person's books in id order whatever order the store lists
// them in, so what a mid-run lock stops the merge at is the same on every run.
func TestMergeAuthorInto_MovesBooksInIDOrderWhateverTheListingOrder(t *testing.T) {
	w := newMergeUndoWorld(t, "Joker Order From", "Joker Order Into", 0)
	var order []string
	db := commitHookDB{PebbleStore: w.st, beforeCommit: func(id string) { order = append(order, id) }}

	_, _, err := mergeAuthorIntoVia(context.Background(), descendingListing{t6g1HeadStore{w.st}}, t6g1HeadWriter{db}, w.from, w.into, false, "op-merge-order", slog.Default())

	require.NoError(t, err)
	require.Len(t, order, 2)
	require.Equal(t, slices.Sorted(slices.Values(w.books)), order, "the books were moved in the store's listing order, not id order")
}

// A preview reports the same hold, so it does not promise a merge the apply
// cannot finish, and writes nothing.
func TestMergeAuthorInto_PreviewReportsTheUserLockedBook(t *testing.T) {
	w := newMergeUndoWorld(t, "Joker Lock Prev From", "Joker Lock Prev Into", 0)
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
	w := newMergeUndoWorld(t, "Joker Lock Err From", "Joker Lock Err Into", 0)
	store := mergeLockStore{t6g1HeadStore: t6g1HeadStore{w.st}, unreadable: map[string]bool{w.books[0]: true}}

	_, _, err := mergeAuthorIntoVia(context.Background(), store, t6g1HeadWriter{w.st}, w.from, w.into, false, "op-merge-lock-err", slog.Default())

	require.Error(t, err)
	require.False(t, errors.Is(err, errAuthorMergeUserLocked), "an unreadable lock was reported as a hold: %v", err)
	requireMergeHeld(t, w, w.books[0], "")
}
