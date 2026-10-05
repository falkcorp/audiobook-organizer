// file: internal/audiobooks/purge_user_state_test.go
// version: 1.1.0
// guid: 7b9a844a-321a-4f77-9487-2f6311409ef2
// last-edited: 2026-10-05

package audiobooks

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

func purgeSeedProgress(t *testing.T, store *database.PebbleStore, bookID string) *database.User {
	t.Helper()
	u, err := store.CreateUser("reader", "reader@example.com", "argon2id", "x", []string{"user"}, "active")
	require.NoError(t, err)
	require.NoError(t, store.SetUserBookState(&database.UserBookState{
		UserID: u.ID, BookID: bookID, Status: database.UserBookStatusInProgress,
		ProgressPct: 40, LastActivityAt: time.Now(),
	}))
	require.NoError(t, store.SetUserPosition(u.ID, bookID, "seg", 40))
	return u
}

// A soft-deleted book a user still has listening state on, with no live
// version of it to carry the state to, is never purged: the hard delete would
// drop the state for good. It is kept and reported as kept_has_progress, not
// counted as an error.
func TestPurge_RefusesBookHoldingUserState(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	softDeleted(t, store, "held", "")
	purgeSeedProgress(t, store, "held")

	res, err := svc.PurgeSoftDeletedBooks(context.Background(), false, nil)
	require.NoError(t, err)
	require.Equal(t, 0, res.Purged)
	require.Equal(t, 1, res.KeptHasProgress)
	require.Equal(t, []string{"held"}, res.KeptHasProgressIDs)
	require.Zero(t, res.CarriedToSibling)
	require.Zero(t, res.CarryFailed)
	require.Empty(t, res.Errors)
	b, err := store.GetBookByID("held")
	require.NoError(t, err)
	require.NotNil(t, b, "the book holding the state is still there")
}

// A merge loser whose state the merge moved keeps only a drained state row;
// that is not state to protect, and the loser still purges.
func TestPurge_MergedLoserWithMovedStateStillPurges(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	keep, err := store.CreateBook(&database.Book{Title: "Keep", Format: "m4b"})
	require.NoError(t, err)
	loser, err := store.CreateBook(&database.Book{Title: "Loser", Format: "mp3"})
	require.NoError(t, err)
	u := purgeSeedProgress(t, store, loser.ID)
	_, err = merge.NewService(store).MergeBooks([]string{keep.ID, loser.ID}, keep.ID)
	require.NoError(t, err)
	st, err := store.GetUserBookState(u.ID, loser.ID)
	require.NoError(t, err)
	require.NotNil(t, st, "fixture: the loser keeps a drained state row")

	res, err := svc.PurgeSoftDeletedBooks(context.Background(), false, nil)
	require.NoError(t, err)
	require.Equal(t, 1, res.Purged, "result %+v", res)
	require.Zero(t, res.KeptHasProgress)
	require.Zero(t, res.CarriedToSibling, "a drained row is not state to carry")
	moved, err := store.GetUserBookState(u.ID, keep.ID)
	require.NoError(t, err)
	require.Equal(t, 40, moved.ProgressPct)
}
