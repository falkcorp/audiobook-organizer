// file: internal/merge/sync_follow_test.go
// version: 1.0.0
// guid: e424f3c3-3b6c-4345-b703-20ca6809ec0f
// last-edited: 2026-07-30

package merge

import (
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	ulid "github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/require"
)

// seedSyncUser creates one user for the progress-merge assertions.
func seedSyncUser(t *testing.T, store database.Store) *database.User {
	t.Helper()
	u, err := store.CreateUser("reader", "reader@example.com", "argon2id", "x", []string{"user"}, "active")
	require.NoError(t, err)
	return u
}

// seedSyncBooks creates a winner (m4b) and a loser (mp3) and returns their IDs.
func seedSyncBooks(t *testing.T, store database.Store) (winnerID, loserID string) {
	t.Helper()
	winnerID = ulid.Make().String()
	loserID = ulid.Make().String()
	_, err := store.CreateBook(&database.Book{ID: winnerID, Title: "Winner", Format: "m4b", FilePath: "/tmp/win.m4b"})
	require.NoError(t, err)
	_, err = store.CreateBook(&database.Book{ID: loserID, Title: "Loser", Format: "mp3", FilePath: "/tmp/lose.mp3"})
	require.NoError(t, err)
	return winnerID, loserID
}

func TestMergeBooks_SyncIdentity_LoserRedirectsToWinner(t *testing.T) {
	store := setupTestStore(t)
	ids := database.AsSyncIdentityStore(store)
	require.NotNil(t, ids, "PebbleStore must implement SyncIdentityStore")

	winnerID, loserID := seedSyncBooks(t, store)
	winnerSync, err := ids.MintOrGetSyncID(winnerID)
	require.NoError(t, err)
	loserSync, err := ids.MintOrGetSyncID(loserID)
	require.NoError(t, err)

	ms := NewService(store)
	_, err = ms.MergeBooks([]string{winnerID, loserID}, winnerID)
	require.NoError(t, err)

	// A client still holding the loser's id must resolve to the winner's item.
	resolved, err := ids.ResolveSyncItem(loserSync)
	require.NoError(t, err)
	require.NotNil(t, resolved)
	require.Equal(t, winnerSync, resolved.SyncID, "loser syncID must resolve to the winner's item")

	loserItem, err := ids.ResolveSyncItem(winnerSync)
	require.NoError(t, err)
	require.Equal(t, []string{loserSync}, loserItem.MergedFrom, "winner records the loser exactly once")

	// The winner's own identity is unchanged.
	stillWinner, has, err := ids.GetSyncIDForBook(winnerID)
	require.NoError(t, err)
	require.True(t, has)
	require.Equal(t, winnerSync, stillWinner)
}

// TestMergeBooks_SyncIdentity_MintsWinnerWhenAbsent covers the common
// production shape once the backfill lands: the loser has a client-visible
// identity, the winner does not yet.
func TestMergeBooks_SyncIdentity_MintsWinnerWhenAbsent(t *testing.T) {
	store := setupTestStore(t)
	ids := database.AsSyncIdentityStore(store)
	winnerID, loserID := seedSyncBooks(t, store)
	loserSync, err := ids.MintOrGetSyncID(loserID)
	require.NoError(t, err)

	ms := NewService(store)
	_, err = ms.MergeBooks([]string{winnerID, loserID}, winnerID)
	require.NoError(t, err)

	winnerSync, has, err := ids.GetSyncIDForBook(winnerID)
	require.NoError(t, err)
	require.True(t, has, "merge must mint a syncID for the winner")
	resolved, err := ids.ResolveSyncItem(loserSync)
	require.NoError(t, err)
	require.Equal(t, winnerSync, resolved.SyncID)
}

func TestMergeBooks_SyncIdentity_ProgressLoserFurtherWins(t *testing.T) {
	store := setupTestStore(t)
	ids := database.AsSyncIdentityStore(store)
	user := seedSyncUser(t, store)
	winnerID, loserID := seedSyncBooks(t, store)
	_, err := ids.MintOrGetSyncID(loserID)
	require.NoError(t, err)

	require.NoError(t, store.SetUserBookState(&database.UserBookState{
		UserID: user.ID, BookID: winnerID, Status: database.UserBookStatusInProgress,
		ProgressPct: 20, LastActivityAt: time.Now().Add(-time.Hour),
	}))
	require.NoError(t, store.SetUserBookState(&database.UserBookState{
		UserID: user.ID, BookID: loserID, Status: database.UserBookStatusInProgress,
		ProgressPct: 80, LastActivityAt: time.Now(),
	}))
	require.NoError(t, store.SetUserPosition(user.ID, loserID, "seg-1", 4242))

	ms := NewService(store)
	_, err = ms.MergeBooks([]string{winnerID, loserID}, winnerID)
	require.NoError(t, err)

	got, err := store.GetUserBookState(user.ID, winnerID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, 80, got.ProgressPct, "the further-along loser state must survive on the winner")
	require.Equal(t, winnerID, got.BookID)

	positions, err := store.ListUserPositionsForBook(user.ID, winnerID)
	require.NoError(t, err)
	require.Len(t, positions, 1)
	require.Equal(t, "seg-1", positions[0].SegmentID)
	require.InDelta(t, 4242.0, positions[0].PositionSeconds, 0.001)

	// Nothing resolvable is left under the soft-deleted loser.
	assertLoserProgressDrained(t, store, user.ID, loserID)
}

func TestMergeBooks_SyncIdentity_ProgressWinnerFurtherWins(t *testing.T) {
	store := setupTestStore(t)
	ids := database.AsSyncIdentityStore(store)
	user := seedSyncUser(t, store)
	winnerID, loserID := seedSyncBooks(t, store)
	_, err := ids.MintOrGetSyncID(loserID)
	require.NoError(t, err)

	require.NoError(t, store.SetUserBookState(&database.UserBookState{
		UserID: user.ID, BookID: winnerID, Status: database.UserBookStatusInProgress,
		ProgressPct: 90, LastActivityAt: time.Now(),
	}))
	require.NoError(t, store.SetUserPosition(user.ID, winnerID, "seg-w", 900))
	require.NoError(t, store.SetUserBookState(&database.UserBookState{
		UserID: user.ID, BookID: loserID, Status: database.UserBookStatusInProgress,
		ProgressPct: 10, LastActivityAt: time.Now().Add(-time.Hour),
	}))
	require.NoError(t, store.SetUserPosition(user.ID, loserID, "seg-l", 100))

	ms := NewService(store)
	_, err = ms.MergeBooks([]string{winnerID, loserID}, winnerID)
	require.NoError(t, err)

	got, err := store.GetUserBookState(user.ID, winnerID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, 90, got.ProgressPct, "winner state must not be rewound by a behind loser")

	positions, err := store.ListUserPositionsForBook(user.ID, winnerID)
	require.NoError(t, err)
	require.Len(t, positions, 1, "the behind loser's positions must not be copied over")
	require.Equal(t, "seg-w", positions[0].SegmentID)

	assertLoserProgressDrained(t, store, user.ID, loserID)
}

func TestMergeBooks_SyncIdentity_NoProgressNoRows(t *testing.T) {
	store := setupTestStore(t)
	user := seedSyncUser(t, store)
	winnerID, loserID := seedSyncBooks(t, store)

	ms := NewService(store)
	_, err := ms.MergeBooks([]string{winnerID, loserID}, winnerID)
	require.NoError(t, err)

	got, err := store.GetUserBookState(user.ID, winnerID)
	require.NoError(t, err)
	require.Nil(t, got, "no progress anywhere must not fabricate a state row")
	got, err = store.GetUserBookState(user.ID, loserID)
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestMergeBooks_SyncIdentity_IdempotentRemerge(t *testing.T) {
	store := setupTestStore(t)
	ids := database.AsSyncIdentityStore(store)
	winnerID, loserID := seedSyncBooks(t, store)
	winnerSync, err := ids.MintOrGetSyncID(winnerID)
	require.NoError(t, err)
	loserSync, err := ids.MintOrGetSyncID(loserID)
	require.NoError(t, err)

	ms := NewService(store)
	for i := 0; i < 3; i++ {
		_, err = ms.MergeBooks([]string{winnerID, loserID}, winnerID)
		require.NoError(t, err, "re-merge %d", i)
	}

	item, err := ids.ResolveSyncItem(winnerSync)
	require.NoError(t, err)
	require.Equal(t, []string{loserSync}, item.MergedFrom, "MergedFrom must not grow on re-merge")
	resolved, err := ids.ResolveSyncItem(loserSync)
	require.NoError(t, err)
	require.Equal(t, winnerSync, resolved.SyncID)
}

// TestMergeBooks_SyncIdentity_ChainedMerges covers B -> A then A -> C: a client
// holding B's id must resolve all the way to C (ResolveSyncItem's hop
// following), not stop at the now-redirected A.
func TestMergeBooks_SyncIdentity_ChainedMerges(t *testing.T) {
	store := setupTestStore(t)
	ids := database.AsSyncIdentityStore(store)

	aID := ulid.Make().String()
	bID := ulid.Make().String()
	cID := ulid.Make().String()
	for _, b := range []*database.Book{
		{ID: aID, Title: "A", Format: "mp3", FilePath: "/tmp/a.mp3"},
		{ID: bID, Title: "B", Format: "mp3", FilePath: "/tmp/b.mp3"},
		{ID: cID, Title: "C", Format: "mp3", FilePath: "/tmp/c.mp3"},
	} {
		_, err := store.CreateBook(b)
		require.NoError(t, err)
	}
	bSync, err := ids.MintOrGetSyncID(bID)
	require.NoError(t, err)

	ms := NewService(store)
	_, err = ms.MergeBooks([]string{aID, bID}, aID) // B -> A
	require.NoError(t, err)
	_, err = ms.MergeBooks([]string{cID, aID}, cID) // A -> C
	require.NoError(t, err)

	cSync, has, err := ids.GetSyncIDForBook(cID)
	require.NoError(t, err)
	require.True(t, has)

	resolved, err := ids.ResolveSyncItem(bSync)
	require.NoError(t, err)
	require.NotNil(t, resolved)
	require.Equal(t, cSync, resolved.SyncID, "B must resolve through A to C")
}

func TestCombineBooks_SyncIdentity_ShellRedirectsToSurvivor(t *testing.T) {
	store := setupTestStore(t)
	ids := database.AsSyncIdentityStore(store)
	user := seedSyncUser(t, store)

	survivorID := ulid.Make().String()
	shellID := ulid.Make().String()
	_, err := store.CreateBook(&database.Book{ID: survivorID, Title: "Survivor", Format: "mp3", FilePath: "/tmp/s1.mp3"})
	require.NoError(t, err)
	_, err = store.CreateBook(&database.Book{ID: shellID, Title: "Shell", Format: "mp3", FilePath: "/tmp/s2.mp3"})
	require.NoError(t, err)

	survivorSync, err := ids.MintOrGetSyncID(survivorID)
	require.NoError(t, err)
	shellSync, err := ids.MintOrGetSyncID(shellID)
	require.NoError(t, err)
	require.NoError(t, store.SetUserBookState(&database.UserBookState{
		UserID: user.ID, BookID: shellID, Status: database.UserBookStatusInProgress,
		ProgressPct: 55, LastActivityAt: time.Now(),
	}))
	require.NoError(t, store.SetUserPosition(user.ID, shellID, "seg-1", 77))

	ms := NewService(store)
	_, err = ms.CombineBooks([]string{survivorID, shellID}, survivorID, nil)
	require.NoError(t, err)

	// The absorbed shell is HARD-deleted here: an unrecorded redirect would be
	// unrecoverable, there is no surviving row to repoint later.
	resolved, err := ids.ResolveSyncItem(shellSync)
	require.NoError(t, err)
	require.NotNil(t, resolved)
	require.Equal(t, survivorSync, resolved.SyncID)

	got, err := store.GetUserBookState(user.ID, survivorID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, 55, got.ProgressPct)
	assertLoserProgressDrained(t, store, user.ID, shellID)
}

// assertLoserProgressDrained asserts no resumable progress is left addressable
// under a merged-away book id. The database package has no
// DeleteUserBookState, so the row is neutralized in place (empty status, zero
// percent, no positions) rather than removed -- an empty Status also drops it
// from the ubs status index, so it can no longer surface in a
// ListUserBookStatesByStatus listing for a book that no longer exists.
func assertLoserProgressDrained(t *testing.T, store database.Store, userID, loserBookID string) {
	t.Helper()
	positions, err := store.ListUserPositionsForBook(userID, loserBookID)
	require.NoError(t, err)
	require.Empty(t, positions, "loser positions must be cleared")

	state, err := store.GetUserBookState(userID, loserBookID)
	require.NoError(t, err)
	if state == nil {
		return
	}
	require.Equal(t, 0, state.ProgressPct, "loser state must be neutralized")
	require.Equal(t, "", state.Status, "loser state must be dropped from the status index")
}
