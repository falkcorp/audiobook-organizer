// file: internal/audiobooks/purge_user_state_test.go
// version: 1.2.0
// guid: 7b9a844a-321a-4f77-9487-2f6311409ef2
// last-edited: 2026-10-06

package audiobooks

import (
	"context"
	"errors"
	"fmt"
	"strings"
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

// purgeSyncGraphFault returns a chosen error from ListSyncAliases or
// ResolveSyncItem (the alias cap is unexported in package database).
type purgeSyncGraphFault struct {
	*database.PebbleStore
	resolveErr error
	aliasErr   error
}

func (s *purgeSyncGraphFault) ResolveSyncItem(syncID string) (*database.SyncItem, error) {
	if s.resolveErr != nil {
		return nil, s.resolveErr
	}
	return s.PebbleStore.ResolveSyncItem(syncID)
}

func (s *purgeSyncGraphFault) ListSyncAliases(syncID string) ([]string, error) {
	if s.aliasErr != nil {
		return nil, s.aliasErr
	}
	return s.PebbleStore.ListSyncAliases(syncID)
}

// Item 4 (#3777 review): a book whose bookmark check fails for a PERMANENT
// sync-graph reason (a broken redirect chain, an alias graph over the cap)
// is refused on every run; the purge says which, instead of the generic
// "cannot read users' listening state", which reads like a transient error.
func TestPurge_PermanentBookmarkCheckFailuresHaveDistinctReasons(t *testing.T) {
	const (
		brokenReason  = "redirect chain is broken"
		aliasReason   = "more merged aliases than the alias cap"
		genericReason = "cannot read users' listening state on it"
	)
	purgeErrFor := func(t *testing.T, res *PurgeResult, id string) string {
		t.Helper()
		require.Zero(t, res.Purged, "%+v", res)
		for _, e := range res.Errors {
			if strings.HasPrefix(e, id+": not purged: ") {
				return e
			}
		}
		t.Fatalf("no refusal reported for %s: %q", id, res.Errors)
		return ""
	}

	t.Run("broken redirect chain (real dangling redirect)", func(t *testing.T) {
		svc, store, _ := setupPurgeBoundary(t)
		softDeleted(t, store, "loser", "")
		ids := database.AsSyncIdentityStore(store)
		_, err := ids.MintOrGetSyncID("loser")
		require.NoError(t, err)
		require.NoError(t, store.RecordSyncMerge("loser", "winner"))
		winnerSync, _, err := store.GetSyncIDForBook("winner")
		require.NoError(t, err)
		require.NoError(t, store.DeleteRaw("sync_item:"+winnerSync))

		res, err := svc.PurgeSoftDeletedBooks(context.Background(), false, nil)
		require.NoError(t, err)
		msg := purgeErrFor(t, res, "loser")
		require.Contains(t, msg, brokenReason)
		require.NotContains(t, msg, genericReason)
		b, err := store.GetBookByID("loser")
		require.NoError(t, err)
		require.NotNil(t, b, "fail closed: the book is kept")
	})
	t.Run("alias graph over the cap", func(t *testing.T) {
		_, store, _ := setupPurgeBoundary(t)
		softDeleted(t, store, "aliased", "")
		_, err := database.AsSyncIdentityStore(store).MintOrGetSyncID("aliased")
		require.NoError(t, err)
		fs := &purgeSyncGraphFault{PebbleStore: store, aliasErr: fmt.Errorf("%w: starting at x", database.ErrSyncAliasLimit)}

		res, err := NewAudiobookService(fs).PurgeSoftDeletedBooks(context.Background(), false, nil)
		require.NoError(t, err)
		msg := purgeErrFor(t, res, "aliased")
		require.Contains(t, msg, aliasReason)
		require.NotContains(t, msg, genericReason)
		require.NotContains(t, msg, brokenReason)
	})
	t.Run("ordinary read error keeps the generic reason", func(t *testing.T) {
		_, store, _ := setupPurgeBoundary(t)
		softDeleted(t, store, "io", "")
		_, err := database.AsSyncIdentityStore(store).MintOrGetSyncID("io")
		require.NoError(t, err)
		fs := &purgeSyncGraphFault{PebbleStore: store, resolveErr: errors.New("injected read error")}

		res, err := NewAudiobookService(fs).PurgeSoftDeletedBooks(context.Background(), false, nil)
		require.NoError(t, err)
		msg := purgeErrFor(t, res, "io")
		require.Contains(t, msg, genericReason)
		require.NotContains(t, msg, brokenReason)
		require.NotContains(t, msg, aliasReason)
	})
}
