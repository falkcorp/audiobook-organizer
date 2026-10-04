// file: internal/metafetch/series_object_drop_history_test.go
// version: 1.0.0
// guid: 6de38339-35ee-4dd6-953c-e64f58267f8b
// last-edited: 2026-10-04

package metafetch

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// seedStaleSeriesObject leaves the row an older build wrote: SeriesID nil
// with an embedded Series object.
func seedStaleSeriesObject(t *testing.T, store *database.PebbleStore, id string) {
	t.Helper()
	_, err := store.SeedLegacyBookRowForTest(id, func(b *database.Book) error {
		b.SeriesID, b.Series = nil, &database.Series{ID: 4242, Name: "Stale Series"}
		return nil
	})
	require.NoError(t, err)
}

// A scanner-style row write that drops a stale series object records a
// series_object row, and that row is the store's bookkeeping, not a later
// edit: the queued apply still runs.
func TestApplyEditsSince_SeriesObjectDropIsNotAnEdit(t *testing.T) {
	store, svc, book := queuedApplyFixture(t)
	seedStaleSeriesObject(t, store, book.ID)
	mark, err := svc.ApplyEditMark(book.ID)
	require.NoError(t, err)

	_, err = store.ModifyBook(book.ID, func(b *database.Book) error { b.Title = "Tag Title"; return nil })
	require.NoError(t, err)
	drops, err := store.GetMetadataChangeHistory(book.ID, database.HistoryFieldSeriesObject, 10)
	require.NoError(t, err)
	require.Len(t, drops, 1, "the drop was recorded")

	edits, err := svc.ApplyEditsSince(book.ID, mark, "apply-queued-own")
	require.NoError(t, err)
	require.Empty(t, edits.Others, "a recorded series-object drop must not refuse the queued apply")
	newMark, err := svc.ApplyEditMark(book.ID)
	require.NoError(t, err)
	require.Equal(t, mark, newMark, "the drop does not move the edit mark")
}

// Undo of a series_object row is refused (not undoable) and leaves the book
// as it is: the object cannot be put back without the series id, and
// restoring it is the relink fixer's job.
func TestUndoFieldChange_SeriesObjectDropIsNotUndoable(t *testing.T) {
	store, svc, book := queuedApplyFixture(t)
	seedStaleSeriesObject(t, store, book.ID)
	_, err := store.ModifyBook(book.ID, func(b *database.Book) error { b.Title = "Retitled"; return nil })
	require.NoError(t, err)
	before, err := store.GetBookByID(book.ID)
	require.NoError(t, err)

	_, err = svc.UndoFieldChange(book.ID, database.HistoryFieldSeriesObject)
	require.True(t, errors.Is(err, ErrFieldNotUndoable), "got %v", err)

	after, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.Equal(t, before.Title, after.Title)
	require.Nil(t, after.SeriesID)
	require.Nil(t, after.Series)
	require.Equal(t, before.UpdatedAt, after.UpdatedAt, "no write")
}
