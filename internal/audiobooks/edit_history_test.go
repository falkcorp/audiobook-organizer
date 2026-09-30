// file: internal/audiobooks/edit_history_test.go
// version: 1.0.0
// guid: 6f1a8c34-2d9b-4e70-a5c3-0b7e4d2f9a51
// last-edited: 2026-09-30

package audiobooks_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// The single-book edit (PUT /audiobooks/:id) records a "manual" history row
// for EVERY column it changes. Until 2026-09-30 the handler recorded six
// fields and only non-empty values, so a PUT of format (or a clear) left no
// history and a queued metadata apply overwrote it silently. Fields the
// override bookkeeping already recorded (isbn13 here: "override",
// "user_edit") are not listed twice.
func TestUpdateAudiobook_ChangeOfAnUnlistedFieldRefusesAQueuedApply(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	book, err := store.CreateBook(&database.Book{Title: "T", FilePath: "/library/u.m4b", Format: "m4b"})
	require.NoError(t, err)

	mfs := metafetch.NewService(store)
	mark, err := mfs.ApplyEditMark(book.ID)
	require.NoError(t, err)

	_, err = audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID,
		map[string]any{"format": "mp3", "isbn13": "9780000000002"})
	require.NoError(t, err)

	edits, err := mfs.ApplyEditsSince(book.ID, mark, "apply-queued-own")
	require.NoError(t, err)
	require.Contains(t, edits.Others, "format (manual, manual)")
	require.Contains(t, edits.Others, "isbn13 (override, user_edit)")
	require.NotContains(t, edits.Others, "isbn13 (manual, manual)", "isbn13 was recorded twice")

	// A PUT that sets the value the book already has records nothing more.
	before, err := store.GetBookChangeHistory(book.ID, 1000)
	require.NoError(t, err)
	_, err = audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID,
		map[string]any{"format": "mp3"})
	require.NoError(t, err)
	after, err := store.GetBookChangeHistory(book.ID, 1000)
	require.NoError(t, err)
	require.Len(t, after, len(before), "a no-op PUT recorded history")
}
