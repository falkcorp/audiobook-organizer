// file: internal/merge/pending_repair_test.go
// version: 1.0.0
// guid: 0c7e4b29-5d1a-4f83-9b6e-2a8f3d7c1e45
// last-edited: 2026-09-26

package merge

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// leavePending makes a merge follow fail on the winner's state write so its
// pending record stays, then retires the loser as the merge would.
func leavePending(t *testing.T, store database.Store) (userID, winnerID, loserID string) {
	t.Helper()
	user := seedSyncUser(t, store)
	winnerID, loserID = seedSyncBooks(t, store)
	seedProgress(t, store, user.ID, loserID, 42)
	fs := &failingStore{Store: store, failStateFor: winnerID}
	require.NoError(t, FollowMerge(fs, asFollower(fs), winnerID, []string{loserID}))
	require.NoError(t, SoftDeleteBook(store, loserID))
	require.Len(t, pendingKeys(t, store), 1)
	return user.ID, winnerID, loserID
}

func TestPendingRepairLoop_CompletesRecordAndStopsCleanly(t *testing.T) {
	store := setupTestStore(t)
	userID, winnerID, loserID := leavePending(t, store)

	ctx, cancel := context.WithCancel(context.Background())
	passes := make(chan PendingSweepResult, 16)
	done := make(chan struct{})
	go func() {
		defer close(done)
		PendingRepairLoop(ctx, store, 10*time.Millisecond, 0, func(r PendingSweepResult) {
			select {
			case passes <- r:
			default:
			}
		})
	}()

	var res PendingSweepResult
	select {
	case res = <-passes:
	case <-time.After(10 * time.Second):
		t.Fatal("ticker never ran a pass")
	}
	require.Equal(t, 1, res.Completed)
	require.Zero(t, res.Remaining)
	require.Empty(t, pendingKeys(t, store))
	got, err := store.GetUserBookState(userID, winnerID)
	require.NoError(t, err)
	require.Equal(t, 42, got.ProgressPct)
	assertLoserProgressDrained(t, store, userID, loserID)

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("PendingRepairLoop did not return after cancel")
	}
	// No goroutine is left inside the loop. Checked by stack, not by count:
	// the Pebble store runs its own background goroutines.
	require.Eventually(t, func() bool {
		buf := make([]byte, 1<<20)
		return !strings.Contains(string(buf[:runtime.Stack(buf, true)]), "merge.PendingRepairLoop")
	}, 5*time.Second, 20*time.Millisecond, "goroutine leak after shutdown")
}

func TestPendingRepairLoop_SkipsYoungRecordsAndDefersLiveLoser(t *testing.T) {
	store := setupTestStore(t)
	user := seedSyncUser(t, store)
	winnerID, loserID := seedSyncBooks(t, store)
	seedProgress(t, store, user.ID, loserID, 42)
	fs := &failingStore{Store: store, failStateFor: winnerID}
	require.NoError(t, FollowMerge(fs, asFollower(fs), winnerID, []string{loserID}))

	young, err := CompletePendingUserStateRepairs(store,
		func(r PendingUserStateRepair) bool { return !r.RecordedAt.After(time.Now().Add(-time.Hour)) }, nil)
	require.NoError(t, err)
	require.Equal(t, 1, young.Skipped, "a record younger than the minimum age is left to its merge")

	live, err := CompletePendingUserStateRepairs(store, nil, nil)
	require.NoError(t, err)
	require.Equal(t, 1, live.Deferred, "the loser was never retired, so the record is deferred")
	require.Len(t, pendingKeys(t, store), 1)
}

// A later merge of the same books first completes the move an earlier merge
// still owes.
func TestFollowMerge_CompletesEarlierPendingFirst(t *testing.T) {
	store := setupTestStore(t)
	userID, winnerID, loserID := leavePending(t, store)
	_, other := seedSyncBooks(t, store)
	require.NoError(t, FollowMerge(store, asFollower(store), winnerID, []string{other}))
	require.Empty(t, pendingKeys(t, store))
	got, err := store.GetUserBookState(userID, winnerID)
	require.NoError(t, err)
	require.Equal(t, 42, got.ProgressPct)
	assertLoserProgressDrained(t, store, userID, loserID)
}

// The scanner's version-link path keeps the same durability: a failed
// progress carry is recorded and completed, although the old row stays live.
func TestFollowBookIDChange_FailedCarryIsRecordedAndCompleted(t *testing.T) {
	store := setupTestStore(t)
	user := seedSyncUser(t, store)
	newID, oldID := seedSyncBooks(t, store)
	_, err := database.AsSyncIdentityStore(store).MintOrGetSyncID(oldID)
	require.NoError(t, err)
	seedProgress(t, store, user.ID, oldID, 37)

	FollowBookIDChange(&failingStore{Store: store, failStateFor: newID}, oldID, newID)
	require.Equal(t, []string{pendingRepairKey(oldID, newID)}, pendingKeys(t, store))

	res, err := CompletePendingUserStateRepairs(store, nil, nil)
	require.NoError(t, err)
	require.Equal(t, 1, res.Completed, "a version-link record is not deferred on its live old row")
	require.Empty(t, pendingKeys(t, store))
	got, err := store.GetUserBookState(user.ID, newID)
	require.NoError(t, err)
	require.Equal(t, 37, got.ProgressPct)
}

func TestFollowBookIDChange_SuccessLeavesNoRecord(t *testing.T) {
	store := setupTestStore(t)
	user := seedSyncUser(t, store)
	newID, oldID := seedSyncBooks(t, store)
	_, err := database.AsSyncIdentityStore(store).MintOrGetSyncID(oldID)
	require.NoError(t, err)
	seedProgress(t, store, user.ID, oldID, 37)
	FollowBookIDChange(store, oldID, newID)
	require.Empty(t, pendingKeys(t, store))
}

// Undo restores positions with their original UpdatedAt (the ABS
// lastUpdate), not the undo time.
func TestCombineUndo_RestoresOriginalPositionTimestamps(t *testing.T) {
	store := setupTestStore(t)
	f := seedUndoFixture(t, store)
	w, ok := database.AsCapability[database.UserPositionTimestampWriter](store)
	require.True(t, ok)
	orig := time.Now().Add(-72 * time.Hour).UTC().Truncate(time.Millisecond)
	require.NoError(t, w.SetUserPositionAt(f.user.ID, f.absA, "seg-a", 1234, orig))
	require.NoError(t, w.SetUserPositionAt(f.user.ID, f.survivor, "seg-s", 55, orig.Add(-time.Hour)))

	ms := NewService(store)
	res, err := ms.CombineBooks([]string{f.survivor, f.absA, f.absB}, f.survivor, nil)
	require.NoError(t, err)
	_, err = ms.UndoCombine(res.JournalID)
	require.NoError(t, err)

	pos, err := store.ListUserPositionsForBook(f.user.ID, f.absA)
	require.NoError(t, err)
	require.Len(t, pos, 1)
	require.True(t, pos[0].UpdatedAt.Equal(orig), "restored %v, want the original %v", pos[0].UpdatedAt, orig)
	spos, err := store.ListUserPositionsForBook(f.user.ID, f.survivor)
	require.NoError(t, err)
	require.Len(t, spos, 1)
	require.True(t, spos[0].UpdatedAt.Equal(orig.Add(-time.Hour)))
}
