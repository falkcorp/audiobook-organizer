// file: internal/metafetch/commit_apply_test.go
// version: 1.1.0
// guid: 7c1e4a92-3b5d-4f60-8e27-9d0a6b3c1f48
// last-edited: 2026-09-13

package metafetch

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// An apply that follows slow provider work writes only the fields it changed
// onto the row as it stands, so a write that landed meanwhile survives; and
// its history is recorded from the committed row. The auto-fetch paths used
// to UpdateBook the whole row read before the provider search, reverting that
// write, and recorded history from the in-memory book.
func TestCommitApply_KeepsAConcurrentWriteAndRecordsTheStoredRow(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	svc := NewService(store)

	book, err := store.CreateBook(&database.Book{Title: "track01", FilePath: "/library/a.m4b", Format: "m4b"})
	require.NoError(t, err)
	before, err := database.SnapshotBook(book)
	require.NoError(t, err)
	working, err := database.SnapshotBook(book)
	require.NoError(t, err)
	working.Title = "Applied Title"

	// Another writer commits while the provider search runs.
	other, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	userPub := "User Pub"
	other.Publisher = &userPub
	_, err = store.UpdateBook(book.ID, other)
	require.NoError(t, err)

	_, err = svc.CommitApply(book.ID, before, working, nil, "Open Library")
	require.NoError(t, err)

	stored, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.Equal(t, "Applied Title", stored.Title)
	require.NotNil(t, stored.Publisher, "the concurrent write must survive the apply")
	require.Equal(t, "User Pub", *stored.Publisher)

	history, err := store.GetBookChangeHistory(book.ID, 100)
	require.NoError(t, err)
	require.Len(t, history, 1, "only the field the apply changed is recorded: %+v", history)
	require.Equal(t, "title", history[0].Field)
	require.NotNil(t, history[0].NewValue)
	require.Equal(t, `"Applied Title"`, *history[0].NewValue)
	require.NotEmpty(t, history[0].BatchID)
}
