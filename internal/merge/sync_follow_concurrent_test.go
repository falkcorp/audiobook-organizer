// file: internal/merge/sync_follow_concurrent_test.go
// version: 1.0.0
// guid: df75ad6b-54e4-4a58-b5dd-4e07ec76fb2f
// last-edited: 2026-07-30

package merge

import (
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	ulid "github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/require"
)

// TestMergeBooks_SyncIdentity_ConcurrentSamePair_ExactlyOnce is the
// load-bearing concurrency test for the identity hook. 16 goroutines run the
// SAME merge through ONE Service against a real PebbleStore. The hook lives
// inside MergeBooks' existing mergeSerializeMu critical section, so it is
// exactly-once by construction; this test proves the post-conditions rather
// than merely "no race detected":
//
//  1. -race reports nothing.
//  2. the loser resolves to the winner in exactly one hop (not a chain, not
//     empty).
//  3. the winner's MergedFrom has exactly ONE entry -- 16 appends would still
//     be race-free under the mutex, so only this assertion catches a missing
//     idempotency guard.
//  4. the single seeded progress record survives on the winner with its
//     original value (not zeroed by a lost update, not double-applied).
//
// The Service is built with NewService(store) and the follower is injected
// explicitly via SetSyncFollower so the test cannot silently pass with a
// nil (no-op) follower.
func TestMergeBooks_SyncIdentity_ConcurrentSamePair_ExactlyOnce(t *testing.T) {
	store := setupTestStore(t)
	ids := database.AsSyncIdentityStore(store)
	require.NotNil(t, ids)

	user := seedSyncUser(t, store)

	// B is m4b so BookIsBetter deterministically picks it as the winner no
	// matter how the goroutines interleave.
	aID := ulid.Make().String()
	bID := ulid.Make().String()
	_, err := store.CreateBook(&database.Book{ID: aID, Title: "Dup A", Format: "mp3", FilePath: "/tmp/ca.mp3"})
	require.NoError(t, err)
	_, err = store.CreateBook(&database.Book{ID: bID, Title: "Dup B", Format: "m4b", FilePath: "/tmp/cb.m4b"})
	require.NoError(t, err)

	aSync, err := ids.MintOrGetSyncID(aID)
	require.NoError(t, err)
	bSync, err := ids.MintOrGetSyncID(bID)
	require.NoError(t, err)

	require.NoError(t, store.SetUserBookState(&database.UserBookState{
		UserID: user.ID, BookID: aID, Status: database.UserBookStatusInProgress,
		ProgressPct: 50, LastActivityAt: time.Now(),
	}))
	require.NoError(t, store.SetUserPosition(user.ID, aID, "seg-1", 1234))

	ms := NewService(store)
	ms.SetSyncFollower(database.AsSyncIdentityStore(store))

	const goroutines = 16
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			_, _ = ms.MergeBooks([]string{aID, bID}, bID)
		}()
	}
	wg.Wait()

	resolved, err := ids.ResolveSyncItem(aSync)
	require.NoError(t, err)
	require.NotNil(t, resolved)
	require.Equal(t, bSync, resolved.SyncID, "loser must resolve to the winner")

	winnerItem, err := ids.ResolveSyncItem(bSync)
	require.NoError(t, err)
	require.NotNil(t, winnerItem)
	require.Len(t, winnerItem.MergedFrom, 1, "MergedFrom must have exactly one entry after 16 identical merges")
	require.Equal(t, aSync, winnerItem.MergedFrom[0])
	require.Equal(t, "", winnerItem.RedirectTo, "the winner must stay live, never redirected")

	state, err := store.GetUserBookState(user.ID, bID)
	require.NoError(t, err)
	require.NotNil(t, state, "the lone progress record must have survived onto the winner")
	require.Equal(t, 50, state.ProgressPct)

	positions, err := store.ListUserPositionsForBook(user.ID, bID)
	require.NoError(t, err)
	require.Len(t, positions, 1)
	require.InDelta(t, 1234.0, positions[0].PositionSeconds, 0.001)

	assertLoserProgressDrained(t, store, user.ID, aID)
}
