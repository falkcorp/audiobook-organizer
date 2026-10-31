// file: internal/merge/sync_follow_handover_test.go
// version: 1.0.0
// guid: 0c022c5f-dfcc-4f2b-a607-45bbb5837b7a
// last-edited: 2026-10-30

package merge

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The organizer's copy has NEW book_file ids, so a sync_file ino must follow
// its track onto the row that replaces it, not land on the copy under an id
// the copy does not have. FollowBookIDChange (stable ids) cannot do that.
func TestFollowVersionHandover_RemapsInoOntoTheReplacementFileRow(t *testing.T) {
	store := setupTestStore(t)
	ids := database.AsSyncIdentityStore(store)
	sf := database.AsSyncFileStore(store)
	user := seedSyncUser(t, store)
	copyID, srcID := seedSyncBooks(t, store)

	syncID, err := ids.MintOrGetSyncID(srcID)
	require.NoError(t, err)
	ino, err := sf.MintOrGetSyncFileID(srcID, "src-file-1")
	require.NoError(t, err)
	seedProgress(t, store, user.ID, srcID, 40)

	FollowVersionHandover(store, srcID, copyID, map[string]string{"src-file-1": "copy-file-1"})

	got, has, err := ids.GetSyncIDForBook(copyID)
	require.NoError(t, err)
	require.True(t, has)
	require.Equal(t, syncID, got, "the client-visible id moves to the copy")
	_, stillOnSource, err := ids.GetSyncIDForBook(srcID)
	require.NoError(t, err)
	require.False(t, stillOnSource)

	gotIno, has, err := sf.GetSyncFileID(copyID, "copy-file-1")
	require.NoError(t, err)
	require.True(t, has, "the ino resolves under the copy's own file id")
	require.Equal(t, ino, gotIno)
	_, has, err = sf.GetSyncFileID(copyID, "src-file-1")
	require.NoError(t, err)
	require.False(t, has, "nothing is left under an id the copy has no row for")
	left, err := sf.ListSyncFilesForBook(srcID)
	require.NoError(t, err)
	require.Empty(t, left)

	state, err := store.GetUserBookState(user.ID, copyID)
	require.NoError(t, err)
	require.NotNil(t, state)
	require.Equal(t, 40, state.ProgressPct)
	assertLoserProgressDrained(t, store, user.ID, srcID)
	require.Empty(t, pendingKeys(t, store))
}

// backfill-itunes-positions writes positions and mints no sync id. The
// scanner's follow leaves those where they are; a handover hides the source,
// so it must move them.
func TestFollowVersionHandover_MovesProgressOfASourceWithNoSyncID(t *testing.T) {
	store := setupTestStore(t)
	user := seedSyncUser(t, store)
	copyID, srcID := seedSyncBooks(t, store)
	seedProgress(t, store, user.ID, srcID, 55)

	FollowBookIDChange(store, srcID, copyID)
	stranded, err := store.GetUserBookState(user.ID, srcID)
	require.NoError(t, err)
	require.NotNil(t, stranded)
	require.Equal(t, 55, stranded.ProgressPct, "the scanner's follow keeps progress on a book with no sync id")

	FollowVersionHandover(store, srcID, copyID, nil)

	state, err := store.GetUserBookState(user.ID, copyID)
	require.NoError(t, err)
	require.NotNil(t, state)
	require.Equal(t, 55, state.ProgressPct)
	assertLoserProgressDrained(t, store, user.ID, srcID)
	_, has, err := database.AsSyncIdentityStore(store).GetSyncIDForBook(copyID)
	require.NoError(t, err)
	require.False(t, has, "no sync id is invented for a book that never had one")
}

// A source with nothing to carry changes nothing.
func TestFollowVersionHandover_NothingToCarryIsANoOp(t *testing.T) {
	store := setupTestStore(t)
	user := seedSyncUser(t, store)
	copyID, srcID := seedSyncBooks(t, store)

	FollowVersionHandover(store, srcID, copyID, map[string]string{"a": "b"})

	state, err := store.GetUserBookState(user.ID, copyID)
	require.NoError(t, err)
	require.Nil(t, state)
	require.Empty(t, pendingKeys(t, store))
}

// A destination that already minted an ino for the replacement row keeps it.
func TestFollowVersionHandover_DestinationInoWinsOverTheCarriedOne(t *testing.T) {
	store := setupTestStore(t)
	ids := database.AsSyncIdentityStore(store)
	sf := database.AsSyncFileStore(store)
	copyID, srcID := seedSyncBooks(t, store)
	_, err := ids.MintOrGetSyncID(srcID)
	require.NoError(t, err)
	carried, err := sf.MintOrGetSyncFileID(srcID, "src-file-1")
	require.NoError(t, err)
	own, err := sf.MintOrGetSyncFileID(copyID, "copy-file-1")
	require.NoError(t, err)
	require.NotEqual(t, carried, own)

	FollowVersionHandover(store, srcID, copyID, map[string]string{"src-file-1": "copy-file-1"})

	got, has, err := sf.GetSyncFileID(copyID, "copy-file-1")
	require.NoError(t, err)
	require.True(t, has)
	require.Equal(t, own, got)
}
