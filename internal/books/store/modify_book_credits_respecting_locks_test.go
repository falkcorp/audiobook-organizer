// file: internal/books/store/modify_book_credits_respecting_locks_test.go
// version: 1.0.0
// guid: a2b9f5f0-670e-43c5-97ac-93a397d042f6
// last-edited: 2026-11-18

package store

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// creditsLockWorld is a real in-memory store holding one book credited to
// author A (primary) and B (co-author), plus a third author C the writes below
// move credits onto. Real, because the properties under test are atomicity and
// "the lock is read inside the callback", which a mock cannot show.
type creditsLockWorld struct {
	st      *PebbleStore
	bookID  string
	a, b, c int
}

func newCreditsLockWorld(t *testing.T) *creditsLockWorld {
	t.Helper()
	st, err := NewPebbleStoreInMemory(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	mk := func(name string) int {
		a, aerr := st.CreateAuthor(name)
		require.NoError(t, aerr)
		return a.ID
	}
	w := &creditsLockWorld{st: st, a: mk("Harvey Dent"), b: mk("Selina Kyle"), c: mk("Barbara Gordon")}
	narr := "Alfred Pennyworth"
	book, err := st.CreateBook(&Book{Title: "Year One", FilePath: "/lib/year-one/book.m4b", AuthorID: &w.a, Narrator: &narr})
	require.NoError(t, err)
	w.bookID = book.ID
	require.NoError(t, st.SetBookAuthors(book.ID, []BookAuthor{
		{BookID: book.ID, AuthorID: w.a, Role: "author", Position: 0},
		{BookID: book.ID, AuthorID: w.b, Role: "author", Position: 1},
	}))
	return w
}

func (w *creditsLockWorld) lock(t *testing.T, key string) {
	t.Helper()
	require.NoError(t, w.st.UpsertMetadataFieldState(&MetadataFieldState{BookID: w.bookID, Field: key, OverrideLocked: true}))
}

func (w *creditsLockWorld) authorIDs(t *testing.T) []int {
	t.Helper()
	as, err := w.st.GetBookAuthors(w.bookID)
	require.NoError(t, err)
	var ids []int
	for _, a := range as {
		ids = append(ids, a.AuthorID)
	}
	return ids
}

func (w *creditsLockWorld) primary(t *testing.T) int {
	t.Helper()
	b, err := w.st.GetBookByID(w.bookID)
	require.NoError(t, err)
	require.NotNil(t, b.AuthorID)
	return *b.AuthorID
}

// swapCoAuthor replaces the co-author B with C and leaves the primary alone: a
// credits-only change, the shape of an unlink or merge of a co-author row.
func (w *creditsLockWorld) swapCoAuthor(b *Book, c *BookCreditsEdit) error {
	c.Authors = []BookAuthor{
		{BookID: w.bookID, AuthorID: w.a, Role: "author", Position: 0},
		{BookID: w.bookID, AuthorID: w.c, Role: "author", Position: 1},
	}
	return nil
}

// repointPrimary moves the primary AND the credits from A to C, as one decision.
func (w *creditsLockWorld) repointPrimary(b *Book, c *BookCreditsEdit) error {
	id := w.c
	b.AuthorID = &id
	c.Authors = []BookAuthor{
		{BookID: w.bookID, AuthorID: w.c, Role: "author", Position: 0},
		{BookID: w.bookID, AuthorID: w.b, Role: "author", Position: 1},
	}
	return nil
}

// The control: an unlocked book gets the credits and the primary in full.
func TestModifyBookCreditsRespectingLocks_UnlockedBookIsWrittenAtomically(t *testing.T) {
	w := newCreditsLockWorld(t)
	written, held, err := ModifyBookCreditsRespectingLocks(w.st, w.st, w.bookID, w.repointPrimary)
	require.NoError(t, err)
	require.Empty(t, held)
	require.NotNil(t, written)
	require.Equal(t, w.c, w.primary(t))
	require.Equal(t, []int{w.c, w.b}, w.authorIDs(t))
}

// A locked author holds a credits-only change. FieldLocks.Apply sees Book
// columns only, so this is the case it cannot hold by itself: the primary is
// untouched, the credit list is not.
func TestModifyBookCreditsRespectingLocks_LockedAuthorHoldsACreditsOnlyChange(t *testing.T) {
	w := newCreditsLockWorld(t)
	w.lock(t, FieldKeyAuthorName)
	written, held, err := ModifyBookCreditsRespectingLocks(w.st, w.st, w.bookID, w.swapCoAuthor)
	require.NoError(t, err)
	require.Equal(t, []string{FieldKeyAuthorName}, held)
	require.NotNil(t, written)
	require.Equal(t, []int{w.a, w.b}, w.authorIDs(t), "the credits were rewritten although the author is locked")
	require.Equal(t, w.a, w.primary(t))
}

// A locked author holds a primary-plus-credits change whole: neither lands.
func TestModifyBookCreditsRespectingLocks_LockedAuthorHoldsThePrimaryAndTheCreditsTogether(t *testing.T) {
	w := newCreditsLockWorld(t)
	w.lock(t, FieldKeyAuthorName)
	_, held, err := ModifyBookCreditsRespectingLocks(w.st, w.st, w.bookID, w.repointPrimary)
	require.NoError(t, err)
	require.Equal(t, []string{FieldKeyAuthorName}, held)
	require.Equal(t, []int{w.a, w.b}, w.authorIDs(t))
	require.Equal(t, w.a, w.primary(t))
}

// The returned row and the hold report describe the book as read, not as the
// callback left it: a caller that logs the "written" row must not see a write.
func TestModifyBookCreditsRespectingLocks_HeldReturnsTheRowAsRead(t *testing.T) {
	w := newCreditsLockWorld(t)
	w.lock(t, FieldKeyAuthorName)
	written, _, err := ModifyBookCreditsRespectingLocks(w.st, w.st, w.bookID, w.repointPrimary)
	require.NoError(t, err)
	require.NotNil(t, written.AuthorID)
	require.Equal(t, w.a, *written.AuthorID)
}

// A narrator credit change is held by the narrator lock, and by that alone.
func TestModifyBookCreditsRespectingLocks_NarratorLockHoldsANarratorCreditChange(t *testing.T) {
	w := newCreditsLockWorld(t)
	n, err := w.st.CreateNarrator("Oracle")
	require.NoError(t, err)
	// A narrator the book does not credit yet: the book row's own narrator
	// (Alfred Pennyworth) is already synced into its list at creation.
	addNarrator := func(b *Book, c *BookCreditsEdit) error {
		c.Narrators = []BookNarrator{{BookID: w.bookID, NarratorID: n.ID, Role: "narrator", Position: 0}}
		return nil
	}

	w.lock(t, FieldKeyAuthorName)
	_, held, err := ModifyBookCreditsRespectingLocks(w.st, w.st, w.bookID, addNarrator)
	require.NoError(t, err)
	require.Empty(t, held, "an author lock held a narrator-only change")

	other := newCreditsLockWorld(t)
	other.lock(t, FieldKeyNarrator)
	n2, err := other.st.CreateNarrator("Oracle")
	require.NoError(t, err)
	beforeN, err := other.st.GetBookNarrators(other.bookID)
	require.NoError(t, err)
	_, held, err = ModifyBookCreditsRespectingLocks(other.st, other.st, other.bookID, func(b *Book, c *BookCreditsEdit) error {
		c.Narrators = []BookNarrator{{BookID: other.bookID, NarratorID: n2.ID, Role: "narrator", Position: 0}}
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, []string{FieldKeyNarrator}, held)
	got, err := other.st.GetBookNarrators(other.bookID)
	require.NoError(t, err)
	require.Equal(t, beforeN, got, "the narrator credits were rewritten although the narrator is locked")
}

// A lock on a field the write does not touch does not hold it.
func TestModifyBookCreditsRespectingLocks_LockOnAnotherFieldDoesNotHold(t *testing.T) {
	w := newCreditsLockWorld(t)
	w.lock(t, FieldKeyPublisher)
	_, held, err := ModifyBookCreditsRespectingLocks(w.st, w.st, w.bookID, w.swapCoAuthor)
	require.NoError(t, err)
	require.Empty(t, held)
	require.Equal(t, []int{w.a, w.c}, w.authorIDs(t))
}

// An unreadable lock set fails closed: mutate never runs, nothing is written,
// and the error wraps ErrFieldLocksUnavailable.
type unreadableLocks struct{ *PebbleStore }

func (unreadableLocks) GetMetadataFieldStates(string) ([]MetadataFieldState, error) {
	return nil, errors.New("field states: store closed")
}

func TestModifyBookCreditsRespectingLocks_UnreadableLocksWriteNothing(t *testing.T) {
	w := newCreditsLockWorld(t)
	ran := false
	_, _, err := ModifyBookCreditsRespectingLocks(w.st, unreadableLocks{w.st}, w.bookID, func(*Book, *BookCreditsEdit) error {
		ran = true
		return nil
	})
	require.ErrorIs(t, err, ErrFieldLocksUnavailable)
	require.False(t, ran)
	require.Equal(t, []int{w.a, w.b}, w.authorIDs(t))
}

// mutate's own errors pass through and write nothing; ErrSkipBookWrite is a
// clean no-op that is NOT reported as a hold.
func TestModifyBookCreditsRespectingLocks_MutateErrorsPassThrough(t *testing.T) {
	w := newCreditsLockWorld(t)
	boom := errors.New("boom")
	_, _, err := ModifyBookCreditsRespectingLocks(w.st, w.st, w.bookID, func(*Book, *BookCreditsEdit) error { return boom })
	require.ErrorIs(t, err, boom)
	written, held, err := ModifyBookCreditsRespectingLocks(w.st, w.st, w.bookID, func(*Book, *BookCreditsEdit) error { return ErrSkipBookWrite })
	require.NoError(t, err)
	require.Empty(t, held)
	require.NotNil(t, written)
	require.Equal(t, []int{w.a, w.b}, w.authorIDs(t))
}

func TestModifyBookCreditsRespectingLocks_MissingBook(t *testing.T) {
	w := newCreditsLockWorld(t)
	written, held, err := ModifyBookCreditsRespectingLocks(w.st, w.st, "gone", w.swapCoAuthor)
	require.NoError(t, err)
	require.Nil(t, written)
	require.Nil(t, held)
}

// lockBeforeCallback saves the author lock after the helper has been called but
// before the store runs its callback: the lock read is only correct if it
// happens inside the callback, under the write locks.
type lockBeforeCallback struct {
	*PebbleStore
	save func()
}

func (l lockBeforeCallback) ModifyBook(id string, fn func(*Book) error) (*Book, error) {
	l.save()
	return l.PebbleStore.ModifyBook(id, fn)
}

func (l lockBeforeCallback) ModifyBookCredits(id string, fn func(*Book, *BookCreditsEdit) error) (*Book, BookCreditsEdit, error) {
	l.save()
	return l.PebbleStore.ModifyBookCredits(id, fn)
}

// Pins "the lock is read inside the callback" for both helpers: a lock saved
// after the helper started but before the store ran the callback still holds
// the write. Moving LoadFieldLocks above the store call would fail both.
func TestRespectingLocksHelpers_ReadTheLockInsideTheCallback(t *testing.T) {
	t.Run("credits", func(t *testing.T) {
		w := newCreditsLockWorld(t)
		mod := lockBeforeCallback{PebbleStore: w.st, save: func() { w.lock(t, FieldKeyAuthorName) }}
		_, held, err := ModifyBookCreditsRespectingLocks(mod, w.st, w.bookID, w.repointPrimary)
		require.NoError(t, err)
		require.Equal(t, []string{FieldKeyAuthorName}, held, "a lock saved before the callback ran did not hold the write")
		require.Equal(t, []int{w.a, w.b}, w.authorIDs(t))
	})
	t.Run("book row", func(t *testing.T) {
		w := newCreditsLockWorld(t)
		mod := lockBeforeCallback{PebbleStore: w.st, save: func() { w.lock(t, FieldKeyAuthorName) }}
		_, held, err := ModifyBookRespectingLocks(mod, w.st, w.bookID, func(b *Book) error {
			id := w.c
			b.AuthorID = &id
			return nil
		})
		require.NoError(t, err)
		require.Equal(t, []string{FieldKeyAuthorName}, held, "a lock saved before the callback ran did not hold the write")
		require.Equal(t, w.a, w.primary(t))
	})
}
