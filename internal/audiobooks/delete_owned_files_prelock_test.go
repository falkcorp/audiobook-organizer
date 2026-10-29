// file: internal/audiobooks/delete_owned_files_prelock_test.go
// version: 1.0.0
// guid: 229578be-5a67-41ac-86d2-aaf24b2f0b85
// last-edited: 2026-10-29

package audiobooks

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// A live book that owns file rows is refused before the merge lock is asked
// for: with the lock held by someone else (a running combine) the answer is
// still "owns files", not "merge busy" after the wait.
func TestDeleteAudiobook_HardDeleteOfBookOwningFilesIsRefusedBeforeTheMergeLock(t *testing.T) {
	_, store, _ := setupPurgeBoundary(t)
	_, err := store.CreateBook(&database.Book{ID: "owner", Title: "Hush", Format: "m4b"})
	require.NoError(t, err)
	require.NoError(t, store.CreateBookFile(&database.BookFile{BookID: "owner", FilePath: "/lib/Gotham/hush.m4b", FileSize: 1}))

	unlock, err := merge.TryLockMergeRMWWithin(merge.InteractiveMergeLockWait)
	require.NoError(t, err)
	defer unlock()

	res, err := NewAudiobookService(store).DeleteAudiobook(context.Background(), "owner", &DeleteAudiobookOptions{})

	require.Nil(t, res)
	require.ErrorIs(t, err, database.ErrBookOwnsFiles)
	require.NotErrorIs(t, err, merge.ErrMergeBusy)
}
