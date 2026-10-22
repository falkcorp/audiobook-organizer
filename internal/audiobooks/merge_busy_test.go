// file: internal/audiobooks/merge_busy_test.go
// version: 1.0.0
// guid: 0c4f7a19-3e8d-4b52-a6d1-7e92b5c08f34
// last-edited: 2026-10-22

package audiobooks

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// holdMergeLock simulates a running combine (which holds the merge lock for
// its whole run) and shortens the interactive wait so the test is quick. It
// returns the release function (also registered as cleanup).
func holdMergeLock(t *testing.T) (release func()) {
	t.Helper()
	prev := merge.InteractiveMergeLockWait
	merge.InteractiveMergeLockWait = 50 * time.Millisecond
	merge.LockMergeRMW()
	released := false
	release = func() {
		if !released {
			released = true
			merge.UnlockMergeRMW()
		}
	}
	t.Cleanup(func() {
		release()
		merge.InteractiveMergeLockWait = prev
	})
	return release
}

// Review round 3 S-7: the four interactive actions that take the merge lock
// (restore, the single hard delete of a live or a trashed book, "Purge now",
// "Discard progress and purge") hung for a combine's whole run (a combine
// holds the lock for its whole run). They now answer merge.ErrMergeBusy
// within the bound and change nothing.
func TestInteractiveActions_AnswerMergeBusyWhileACombineRuns(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	softDeleted(t, store, "trashed", "")
	softDeleted(t, store, "discard", "")
	svc.auditOverride = &fakeRecorder{}
	_, err := store.CreateBook(&database.Book{ID: "live", Title: "live"})
	require.NoError(t, err)
	release := holdMergeLock(t)

	start := time.Now()
	_, err = svc.RestoreAudiobook(context.Background(), "trashed")
	assert.ErrorIs(t, err, merge.ErrMergeBusy, "restore")

	_, err = svc.DeleteAudiobook(context.Background(), "trashed", &DeleteAudiobookOptions{})
	assert.ErrorIs(t, err, merge.ErrMergeBusy, "hard delete of a trashed book (Purge now)")

	_, err = svc.DeleteAudiobook(context.Background(), "live", &DeleteAudiobookOptions{})
	assert.ErrorIs(t, err, merge.ErrMergeBusy, "hard delete of a live book")

	_, err = svc.DiscardProgressAndPurge(context.Background(), "discard", "owner")
	assert.ErrorIs(t, err, merge.ErrMergeBusy, "discard progress and purge")
	assert.Less(t, time.Since(start), 5*time.Second, "every action must answer promptly")

	assert.False(t, bookGone(t, store, "trashed"), "a busy answer must not purge")
	assert.False(t, bookGone(t, store, "live"))
	assert.False(t, bookGone(t, store, "discard"))
	b, _ := store.GetBookByID("trashed")
	require.NotNil(t, b)
	assert.True(t, b.IsSoftDeleted(), "a busy answer must not restore")

	// Once the combine is done the same calls go through.
	release()
	_, err = svc.RestoreAudiobook(context.Background(), "trashed")
	require.NoError(t, err)
	b, _ = store.GetBookByID("trashed")
	require.NotNil(t, b)
	assert.False(t, b.IsSoftDeleted())
}
