// file: internal/metafetch/apply_history_incomplete_test.go
// version: 1.0.0
// guid: 8e4a1c73-2d6b-4f95-a3c0-6b9e2d7f4a81
// last-edited: 2026-09-13

package metafetch

import (
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// historyFailStore fails every apply history row while failFetched is set.
type historyFailStore struct {
	*database.PebbleStore
	failFetched bool
}

func (s *historyFailStore) RecordMetadataChange(r *database.MetadataChangeRecord) error {
	if s.failFetched && r.ChangeType == "fetched" {
		return errors.New("injected history failure")
	}
	return s.PebbleStore.RecordMetadataChange(r)
}

// An apply whose history rows could not be written must not let "undo last
// apply" reach past it: the newest batch on record is then an older apply,
// and undoing that one is not what the user asked for. commitApply used to
// drop the error, so the undo reverted the older apply.
func TestUndoLastApply_RefusesWhenTheNewestApplysHistoryWasLost(t *testing.T) {
	pebble, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = pebble.Close() })
	store := &historyFailStore{PebbleStore: pebble}
	svc := NewService(store)

	book, err := store.CreateBook(&database.Book{Title: "track01", FilePath: "/library/a.m4b", Format: "m4b"})
	require.NoError(t, err)

	// Apply A: history recorded.
	beforeA, err := database.SnapshotBook(book)
	require.NoError(t, err)
	workA, err := database.SnapshotBook(book)
	require.NoError(t, err)
	workA.Title = "A Title"
	_, err = svc.CommitApply(book.ID, beforeA, workA, nil, "Open Library")
	require.NoError(t, err)

	// Apply B: the write commits, its history rows fail.
	cur, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	beforeB, err := database.SnapshotBook(cur)
	require.NoError(t, err)
	workB, err := database.SnapshotBook(cur)
	require.NoError(t, err)
	pub := "B Pub"
	workB.Publisher = &pub
	store.failFetched = true
	updated, errB := svc.CommitApply(book.ID, beforeB, workB, nil, "Audible")
	store.failFetched = false
	require.NotNil(t, updated, "the book write itself committed")
	require.ErrorIs(t, errB, ErrApplyHistoryIncomplete)

	res, err := svc.UndoLastApply(book.ID)
	require.Error(t, err, "undo must refuse, result %+v", res)
	require.ErrorIs(t, err, ErrApplyHistoryIncomplete)

	got, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.Equal(t, "A Title", got.Title, "apply A must not be undone in place of apply B")
	require.NotNil(t, got.Publisher)
	require.Equal(t, "B Pub", *got.Publisher)
}
