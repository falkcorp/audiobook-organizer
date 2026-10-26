// file: internal/audiobooks/block_hash_guard_test.go
// version: 1.0.0
// guid: 0b7d3f68-4c19-4e85-a2d6-9f1c5e8a3b74
// last-edited: 2026-10-26

package audiobooks

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

const sharedTestHash = "aa11bb22cc33dd44"

func hashedBook(t *testing.T, store *database.PebbleStore, id, hash string, trashed bool) {
	t.Helper()
	b := &database.Book{ID: id, Title: id, FileHash: &hash}
	if trashed {
		marked := true
		at := time.Now().Add(-time.Hour)
		b.MarkedForDeletion, b.MarkedForDeletionAt = &marked, &at
	}
	_, err := store.CreateBook(b)
	require.NoError(t, err)
}

func hashBlocked(t *testing.T, store *database.PebbleStore, hash string) bool {
	t.Helper()
	blocked, err := store.IsHashBlocked(hash)
	require.NoError(t, err)
	return blocked
}

func firstWarning(t *testing.T, res map[string]any) string {
	t.Helper()
	w, ok := res["warnings"].([]string)
	require.True(t, ok, "expected a warnings list in %v", res)
	require.NotEmpty(t, w)
	return w[0]
}

// A delete that asks to block a hash a live book still holds proceeds, does
// not block, and says which live book holds it. The holder here is found
// through the book hash index, which lists EVERY holder: the book being
// deleted sorts first, so a lookup that answered with one holder would answer
// with the book itself and miss "keeper".
func TestDeleteAudiobook_BlockHashSkippedWhileLiveBookHoldsIt(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	hashedBook(t, store, "gone", sharedTestHash, false)
	hashedBook(t, store, "keeper", sharedTestHash, false)

	res, err := svc.DeleteAudiobook(context.Background(), "gone", &DeleteAudiobookOptions{SoftDelete: true, BlockHash: true})
	require.NoError(t, err)
	assert.Equal(t, false, res["blocked"])
	assert.Contains(t, firstWarning(t, res), "keeper")
	assert.False(t, hashBlocked(t, store, sharedTestHash), "a shared hash must not be blocked")
	gone, err := store.GetBookByID("gone")
	require.NoError(t, err)
	assert.True(t, gone.IsSoftDeleted(), "the delete itself still happened")
	keeper, err := store.GetBookByID("keeper")
	require.NoError(t, err)
	assert.False(t, keeper.IsSoftDeleted())
}

// The second holder is found through the multi-valued book_file hash index
// even when the book hash index lists only the deleted book.
func TestDeleteAudiobook_BlockHashSkippedWhenHolderIsOnlyInBookFileIndex(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	hashedBook(t, store, "keeper", sharedTestHash, false)
	require.NoError(t, store.CreateBookFile(&database.BookFile{BookID: "keeper", FilePath: "/x/keeper.m4b", Format: "m4b", FileHash: sharedTestHash}))
	hashedBook(t, store, "gone", sharedTestHash, false)

	res, err := svc.DeleteAudiobook(context.Background(), "gone", &DeleteAudiobookOptions{SoftDelete: true, BlockHash: true})
	require.NoError(t, err)
	assert.Equal(t, false, res["blocked"])
	assert.Contains(t, firstWarning(t, res), "keeper")
	assert.False(t, hashBlocked(t, store, sharedTestHash))
}

// A hash nobody else holds is blocked as before, and a trashed twin is not a
// live holder.
func TestDeleteAudiobook_BlockHashStillBlocksWhenNoLiveHolder(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	hashedBook(t, store, "twin", sharedTestHash, true)
	hashedBook(t, store, "gone", sharedTestHash, false)

	res, err := svc.DeleteAudiobook(context.Background(), "gone", &DeleteAudiobookOptions{SoftDelete: true, BlockHash: true})
	require.NoError(t, err)
	assert.Equal(t, true, res["blocked"])
	assert.Nil(t, res["warnings"])
	assert.True(t, hashBlocked(t, store, sharedTestHash))
}

// Without block_hash nothing is looked up, blocked, or warned about.
func TestDeleteAudiobook_NoBlockHashRequestedMeansNoWarning(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	hashedBook(t, store, "gone", sharedTestHash, false)
	hashedBook(t, store, "keeper", sharedTestHash, false)

	res, err := svc.DeleteAudiobook(context.Background(), "gone", &DeleteAudiobookOptions{SoftDelete: true})
	require.NoError(t, err)
	assert.Equal(t, false, res["blocked"])
	assert.Nil(t, res["warnings"])
	assert.False(t, hashBlocked(t, store, sharedTestHash))
}

// The hard delete of a live book takes the same path.
func TestDeleteAudiobook_HardDeleteBlockHashSkippedWhileLiveBookHoldsIt(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	hashedBook(t, store, "gone", sharedTestHash, false)
	hashedBook(t, store, "keeper", sharedTestHash, false)

	res, err := svc.DeleteAudiobook(context.Background(), "gone", &DeleteAudiobookOptions{BlockHash: true})
	require.NoError(t, err)
	assert.Equal(t, false, res["blocked"])
	assert.Contains(t, firstWarning(t, res), "keeper")
	assert.False(t, hashBlocked(t, store, sharedTestHash))
	assert.True(t, bookGone(t, store, "gone"))
}

// "Purge now" on a trashed book blocks only when nobody live holds the hash.
func TestDeleteAudiobook_PurgeNowBlockHashSkippedWhileLiveBookHoldsIt(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	hashedBook(t, store, "gone", sharedTestHash, true)
	hashedBook(t, store, "keeper", sharedTestHash, false)

	res, err := svc.DeleteAudiobook(context.Background(), "gone", &DeleteAudiobookOptions{BlockHash: true})
	require.NoError(t, err)
	assert.NotEqual(t, true, res["blocked"])
	assert.Contains(t, firstWarning(t, res), "keeper")
	assert.False(t, hashBlocked(t, store, sharedTestHash))
	assert.True(t, bookGone(t, store, "gone"))
}
