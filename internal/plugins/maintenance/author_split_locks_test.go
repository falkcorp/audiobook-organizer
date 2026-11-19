// file: internal/plugins/maintenance/author_split_locks_test.go
// version: 1.1.0
// guid: 5a7e2c19-8d04-4b63-9f1a-c60e3b2d7a85
// last-edited: 2026-11-18

package maintenance

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// splitLockStore is a real store whose lock read can be made to fail for chosen
// books. A real lock is set with UpsertMetadataFieldState.
type splitLockStore struct {
	*database.PebbleStore
	unreadable map[string]bool
	// lockAfter, when non-nil, is the number of lock reads that answer from the
	// real store; every later read of lockBook reports its author locked. It
	// models a lock the user saves after the run's first look.
	lockAfter *atomic.Int64
	lockBook  string
}

func (s splitLockStore) GetMetadataFieldStates(id string) ([]database.MetadataFieldState, error) {
	if s.unreadable[id] {
		return nil, fmt.Errorf("field states for %s: store closed", id)
	}
	if s.lockAfter != nil && id == s.lockBook && s.lockAfter.Add(-1) < 0 {
		return []database.MetadataFieldState{{BookID: id, Field: database.FieldKeyAuthorName, OverrideLocked: true}}, nil
	}
	return s.PebbleStore.GetMetadataFieldStates(id)
}

// mkSplitBook creates a book credited (junction + legacy) to the composite.
func mkSplitBook(t *testing.T, st *database.PebbleStore, title string, composite int) string {
	t.Helper()
	b, err := st.CreateBook(&database.Book{
		Title:            title,
		FilePath:         "/" + title + "/book.m4b",
		AuthorID:         new(composite),
		IsPrimaryVersion: new(true),
	})
	require.NoError(t, err)
	require.NoError(t, st.SetBookAuthors(b.ID, []database.BookAuthor{{BookID: b.ID, AuthorID: composite, Role: "author"}}))
	return b.ID
}

func authorIDOf(t *testing.T, st *database.PebbleStore, id string) int {
	t.Helper()
	b, err := st.GetBookByID(id)
	require.NoError(t, err)
	require.NotNil(t, b)
	require.NotNil(t, b.AuthorID)
	return *b.AuthorID
}

// A book whose author the user locked keeps the composite credit and primary
// author, and the composite author stays (a book still credits it). The unlocked
// book credited to the same composite is split as usual, so the hold is per book.
func TestAuthorSplitScanOp_UserLockedAuthorBookIsLeftAlone(t *testing.T) {
	st := newSplitRelinkStore(t)
	composite, err := st.CreateAuthor("Selina Kyle & Harvey Dent")
	require.NoError(t, err)
	lockedID := mkSplitBook(t, st, "The Court of Owls", composite.ID)
	openID := mkSplitBook(t, st, "Night of the Owls", composite.ID)
	require.NoError(t, st.UpsertMetadataFieldState(&database.MetadataFieldState{
		BookID: lockedID, Field: database.FieldKeyAuthorName, OverrideLocked: true,
	}))

	rep := runSplitScanOp(t, st)

	require.Equal(t, composite.ID, authorIDOf(t, st, lockedID), "a user-locked author was rewritten; logs %v", rep.logs)
	credits, err := st.GetBookAuthors(lockedID)
	require.NoError(t, err)
	require.Len(t, credits, 1)
	require.Equal(t, composite.ID, credits[0].AuthorID, "the locked book's credit list was rewritten")

	selina, err := st.GetAuthorByName("Selina Kyle")
	require.NoError(t, err)
	require.NotNil(t, selina, "control: the unlocked book was not split; logs %v", rep.logs)
	require.Equal(t, selina.ID, authorIDOf(t, st, openID), "control book not repointed")

	kept, err := st.GetAuthorByID(composite.ID)
	require.NoError(t, err)
	require.NotNil(t, kept, "the composite was deleted while a locked book still credits it")

	var summary, left string
	for _, l := range rep.logs {
		if strings.HasPrefix(l, "Split ") && strings.Contains(l, "composite authors,") {
			summary = l
		}
		if strings.Contains(l, "left in place") {
			left = l
		}
	}
	require.Contains(t, summary, "1 books left alone: author locked by the user")
	require.Contains(t, summary, "(0 errors,", "a held book is an expected state, not an error")
	require.NotEmpty(t, left, "the composite left in place was not reported")
}

// A lock set that cannot be read writes nothing for that book and counts an error.
func TestAuthorSplitScanOp_UnreadableLocksRelinkNothing(t *testing.T) {
	st := newSplitRelinkStore(t)
	composite, err := st.CreateAuthor("Selina Kyle & Harvey Dent")
	require.NoError(t, err)
	badID := mkSplitBook(t, st, "Court of Owls", composite.ID)

	rep := runSplitScanOp(t, splitLockStore{PebbleStore: st, unreadable: map[string]bool{badID: true}})

	require.Equal(t, composite.ID, authorIDOf(t, st, badID), "the book was relinked without knowing its locks; logs %v", rep.logs)
	credits, err := st.GetBookAuthors(badID)
	require.NoError(t, err)
	require.Len(t, credits, 1, "the credit list of a book whose locks could not be read was rewritten")
	require.Equal(t, composite.ID, credits[0].AuthorID, "the credit list of a book whose locks could not be read was rewritten")
	kept, err := st.GetAuthorByID(composite.ID)
	require.NoError(t, err)
	require.NotNil(t, kept, "composite deleted although its book was not relinked")
	var summary string
	for _, l := range rep.logs {
		if strings.HasPrefix(l, "Split ") && strings.Contains(l, "composite authors,") {
			summary = l
		}
	}
	require.NotContains(t, summary, "(0 errors,", "the unreadable lock read was not counted: %s", summary)
}

// The run's first lock read is the advisory pre-check; the write itself reads
// the locks again under the book's write lock. A lock saved between the two
// still holds the primary-author write: the book keeps the composite primary and
// the hold is counted, not written over.
func TestAuthorSplitScanOp_LockSavedAfterThePreCheckHoldsThePrimaryWrite(t *testing.T) {
	st := newSplitRelinkStore(t)
	composite, err := st.CreateAuthor("Selina Kyle & Harvey Dent")
	require.NoError(t, err)
	id := mkSplitBook(t, st, "The Court of Owls", composite.ID)
	var calls atomic.Int64
	calls.Store(1) // the pre-check reads unlocked; every later read sees the lock

	rep := runSplitScanOp(t, splitLockStore{PebbleStore: st, lockAfter: &calls, lockBook: id})

	require.Equal(t, composite.ID, authorIDOf(t, st, id), "a lock saved after the pre-check did not hold the write; logs %v", rep.logs)
	var held bool
	for _, l := range rep.logs {
		if strings.Contains(l, "locked by the user after its credits were relinked") {
			held = true
		}
	}
	require.True(t, held, "the late hold was not reported; logs %v", rep.logs)
}
