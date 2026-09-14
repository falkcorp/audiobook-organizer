// file: internal/dedup/book_dedup_sync_follow_test.go
// version: 1.1.0
// guid: 993dc044-1180-490b-a583-a67b30067e92
// last-edited: 2026-09-14

package dedup

import (
	"context"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	ulid "github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/require"
)

// TestMergeBooks_SyncIdentityFollowsRetiredLoser: this package's MergeBooks
// (live via internal/reconcile/itunes_heal.go) retires losers, and the
// device's listening position must follow to the kept book rather than stay
// under a row that is on the purge clock. It HARD-deleted losers until A1#11
// (2026-09-14), when an unrecorded redirect would have lost the position for
// good; the follow is still what moves it.
func TestMergeBooks_SyncIdentityFollowsRetiredLoser(t *testing.T) {
	store := newConcurrentTestStore(t)
	ids := database.AsSyncIdentityStore(store)
	require.NotNil(t, ids)

	user, err := store.CreateUser("listener", "listener@example.com", "argon2id", "x", []string{"user"}, "active")
	require.NoError(t, err)

	keepID := ulid.Make().String()
	loserID := ulid.Make().String()
	_, err = store.CreateBook(&database.Book{ID: keepID, Title: "Keep", Format: "m4b", FilePath: "/tmp/keep.m4b"})
	require.NoError(t, err)
	_, err = store.CreateBook(&database.Book{ID: loserID, Title: "Loser", Format: "mp3", FilePath: "/tmp/loser.mp3"})
	require.NoError(t, err)

	keepSync, err := ids.MintOrGetSyncID(keepID)
	require.NoError(t, err)
	loserSync, err := ids.MintOrGetSyncID(loserID)
	require.NoError(t, err)

	require.NoError(t, store.SetUserBookState(&database.UserBookState{
		UserID: user.ID, BookID: loserID, Status: database.UserBookStatusInProgress,
		ProgressPct: 42, LastActivityAt: time.Now(),
	}))
	require.NoError(t, store.SetUserPosition(user.ID, loserID, "seg-1", 314))

	res, err := MergeBooks(context.Background(), store, ulid.Make().String(), keepID, []string{loserID}, nil)
	require.NoError(t, err)
	require.Equal(t, 1, res.MergedCount)
	require.Empty(t, res.Errors)

	// The loser is soft-deleted, not hard-deleted (A1#11).
	retired, err := store.GetBookByID(loserID)
	require.NoError(t, err)
	require.NotNil(t, retired)
	require.True(t, retired.IsSoftDeleted())

	resolved, err := ids.ResolveSyncItem(loserSync)
	require.NoError(t, err)
	require.NotNil(t, resolved, "the loser's sync item must resolve to the kept book")
	require.Equal(t, keepSync, resolved.SyncID)

	state, err := store.GetUserBookState(user.ID, keepID)
	require.NoError(t, err)
	require.NotNil(t, state, "the loser's listening position must have moved to the kept book")
	require.Equal(t, 42, state.ProgressPct)

	positions, err := store.ListUserPositionsForBook(user.ID, keepID)
	require.NoError(t, err)
	require.Len(t, positions, 1)
	require.InDelta(t, 314.0, positions[0].PositionSeconds, 0.001)

	orphaned, err := store.ListUserPositionsForBook(user.ID, loserID)
	require.NoError(t, err)
	require.Empty(t, orphaned, "no position may remain under the retired book id")
}

// TestMergeBooks_SyncIdentityIdempotent re-runs the same merge (the second
// call finds the loser already soft-deleted) and asserts the redirect chain is not
// corrupted and MergedFrom does not grow.
func TestMergeBooks_SyncIdentityIdempotent(t *testing.T) {
	store := newConcurrentTestStore(t)
	ids := database.AsSyncIdentityStore(store)

	keepID := ulid.Make().String()
	loserID := ulid.Make().String()
	_, err := store.CreateBook(&database.Book{ID: keepID, Title: "Keep", Format: "m4b", FilePath: "/tmp/keep2.m4b"})
	require.NoError(t, err)
	_, err = store.CreateBook(&database.Book{ID: loserID, Title: "Loser", Format: "mp3", FilePath: "/tmp/loser2.mp3"})
	require.NoError(t, err)
	keepSync, err := ids.MintOrGetSyncID(keepID)
	require.NoError(t, err)
	loserSync, err := ids.MintOrGetSyncID(loserID)
	require.NoError(t, err)

	opID := ulid.Make().String()
	_, err = MergeBooks(context.Background(), store, opID, keepID, []string{loserID}, nil)
	require.NoError(t, err)
	_, err = MergeBooks(context.Background(), store, opID, keepID, []string{loserID}, nil)
	require.NoError(t, err)

	item, err := ids.ResolveSyncItem(keepSync)
	require.NoError(t, err)
	require.Equal(t, []string{loserSync}, item.MergedFrom)
}
