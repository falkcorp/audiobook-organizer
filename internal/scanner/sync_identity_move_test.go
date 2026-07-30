// file: internal/scanner/sync_identity_move_test.go
// version: 1.0.0
// guid: 5867d69f-d4f1-4d1e-9850-300c607a271b
// last-edited: 2026-07-30

package scanner

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	ulid "github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/require"
)

// syncMoveFixture wires a real PebbleStore as both the scanner store and the
// global store, with RootDir cleared so the hash-duplicate branch takes the
// version-link path (mint a NEW Book ULID) rather than the in-place
// organized-path promotion (which keeps the same ULID and needs no hook).
func syncMoveFixture(t *testing.T) *database.PebbleStore {
	t.Helper()
	store, cleanup := setupPebbleStore(t)
	t.Cleanup(cleanup)

	prevStore := database.GetGlobalStore()
	database.SetGlobalStore(store)
	SetStore(store)
	prevConfig := config.AppConfig
	config.AppConfig.RootDir = ""
	t.Cleanup(func() {
		database.SetGlobalStore(prevStore)
		SetStore(nil)
		config.AppConfig = prevConfig
	})
	return store
}

// TestSaveBookToDatabase_UntaggedMove_CarriesSyncIdentity: an untagged file is
// moved on disk and re-scanned. There is no AUDIOBOOK_ORGANIZER_ID tag to
// re-link by, so the scanner matches it to its predecessor by hash and mints a
// BRAND NEW Book ULID for the new path. The predecessor's file is gone, so the
// client-visible identity (and the listening position keyed to the old ULID)
// must follow to the new ULID.
func TestSaveBookToDatabase_UntaggedMove_CarriesSyncIdentity(t *testing.T) {
	store := syncMoveFixture(t)
	ids := database.AsSyncIdentityStore(store)
	require.NotNil(t, ids)

	user, err := store.CreateUser("mover", "mover@example.com", "argon2id", "x", []string{"user"}, "active")
	require.NoError(t, err)

	const hash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	oldPath := filepath.Join(t.TempDir(), "gone", "moved.mp3") // deliberately NOT created
	oldID := ulid.Make().String()
	_, err = store.CreateBook(&database.Book{
		ID: oldID, Title: "Moved Book", Format: "mp3",
		FilePath: oldPath, FileHash: strPtr(hash), OriginalFileHash: strPtr(hash),
	})
	require.NoError(t, err)

	syncID, err := ids.MintOrGetSyncID(oldID)
	require.NoError(t, err)
	require.NoError(t, store.SetUserBookState(&database.UserBookState{
		UserID: user.ID, BookID: oldID, Status: database.UserBookStatusInProgress,
		ProgressPct: 64, LastActivityAt: time.Now(),
	}))
	require.NoError(t, store.SetUserPosition(user.ID, oldID, "seg-1", 987))

	newPath := filepath.Join(t.TempDir(), "moved.mp3")
	require.NoError(t, os.WriteFile(newPath, []byte("audio"), 0o644))
	require.NoError(t, saveBookToDatabase(context.Background(), &Book{
		FilePath: newPath, Title: "Moved Book", Format: ".mp3", FileHash: hash,
	}))

	newBook, err := store.GetBookByFilePath(newPath)
	require.NoError(t, err)
	require.NotNil(t, newBook, "the moved file must have been imported")
	require.NotEqual(t, oldID, newBook.ID, "this path mints a new ULID; if it did not, there is nothing to follow")

	// The identity followed to the new ULID.
	got, has, err := ids.GetSyncIDForBook(newBook.ID)
	require.NoError(t, err)
	require.True(t, has, "the new book must own the existing syncID")
	require.Equal(t, syncID, got)

	item, err := ids.ResolveSyncItem(syncID)
	require.NoError(t, err)
	require.NotNil(t, item)
	require.Equal(t, newBook.ID, item.CurrentBookID)
	require.Equal(t, "", item.RedirectTo, "a move is a repoint, not a merge redirect")

	// And so did the listening position.
	state, err := store.GetUserBookState(user.ID, newBook.ID)
	require.NoError(t, err)
	require.NotNil(t, state, "the position must not be stranded on the retired ULID")
	require.Equal(t, 64, state.ProgressPct)
	positions, err := store.ListUserPositionsForBook(user.ID, newBook.ID)
	require.NoError(t, err)
	require.Len(t, positions, 1)
	require.InDelta(t, 987.0, positions[0].PositionSeconds, 0.001)
}

// TestSaveBookToDatabase_SecondCopy_KeepsSyncIdentity: same hash, but the
// predecessor's file is STILL on disk. That is a genuine second copy, not a
// move — both books are real, so the identity must stay put on the original.
func TestSaveBookToDatabase_SecondCopy_KeepsSyncIdentity(t *testing.T) {
	store := syncMoveFixture(t)
	ids := database.AsSyncIdentityStore(store)

	const hash = "5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03"
	dir := t.TempDir()
	firstPath := filepath.Join(dir, "first.mp3")
	require.NoError(t, os.WriteFile(firstPath, []byte("audio"), 0o644))

	firstID := ulid.Make().String()
	_, err := store.CreateBook(&database.Book{
		ID: firstID, Title: "Two Copies", Format: "mp3",
		FilePath: firstPath, FileHash: strPtr(hash), OriginalFileHash: strPtr(hash),
	})
	require.NoError(t, err)
	syncID, err := ids.MintOrGetSyncID(firstID)
	require.NoError(t, err)

	secondPath := filepath.Join(dir, "second.mp3")
	require.NoError(t, os.WriteFile(secondPath, []byte("audio"), 0o644))
	require.NoError(t, saveBookToDatabase(context.Background(), &Book{
		FilePath: secondPath, Title: "Two Copies", Format: ".mp3", FileHash: hash,
	}))

	stillFirst, has, err := ids.GetSyncIDForBook(firstID)
	require.NoError(t, err)
	require.True(t, has, "the original book keeps its identity when its file still exists")
	require.Equal(t, syncID, stillFirst)

	second, err := store.GetBookByFilePath(secondPath)
	require.NoError(t, err)
	require.NotNil(t, second)
	_, hasSecond, err := ids.GetSyncIDForBook(second.ID)
	require.NoError(t, err)
	require.False(t, hasSecond, "a second copy must not inherit the original's identity")
}
