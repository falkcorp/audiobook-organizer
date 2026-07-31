// file: internal/merge/sync_follow_syncfile_test.go
// version: 1.0.0
// guid: d74cf944-f953-4a7e-83eb-994193dbc7d1
// last-edited: 2026-07-30

package merge

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	ulid "github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/require"
)

// This file covers the PR #2074 follow-up gap: sync_file entries (the
// per-file `ino` backing /api/items/{itemId}/file/{ino}) are keyed
// (bookID, fileID) and did not follow CombineBooks or an untagged move, only
// item-level identity and progress did. FollowFileMove and the file-carry
// added to FollowBookIDChange close that gap.

// --- FollowFileMove (direct unit tests) ---

func TestFollowFileMove_RepointsEachFile(t *testing.T) {
	store := setupTestStore(t)
	sf := database.AsSyncFileStore(store)
	require.NotNil(t, sf, "PebbleStore must implement SyncFileStore")

	oldBookID := ulid.Make().String()
	newBookID := ulid.Make().String()

	idA, err := sf.MintOrGetSyncFileID(oldBookID, "file-a")
	require.NoError(t, err)
	idB, err := sf.MintOrGetSyncFileID(oldBookID, "file-b")
	require.NoError(t, err)

	FollowFileMove(store, oldBookID, newBookID, []string{"file-a", "file-b"})

	gotA, ok, err := sf.GetSyncFileID(newBookID, "file-a")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, idA, gotA)

	gotB, ok, err := sf.GetSyncFileID(newBookID, "file-b")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, idB, gotB)

	_, ok, err = sf.GetSyncFileID(oldBookID, "file-a")
	require.NoError(t, err)
	require.False(t, ok, "old book must no longer resolve file-a")
	_, ok, err = sf.GetSyncFileID(oldBookID, "file-b")
	require.NoError(t, err)
	require.False(t, ok, "old book must no longer resolve file-b")
}

// TestFollowFileMove_GuardsNilAndEmptyArgs exercises every early-return guard
// -- none of these should panic, error, or mutate anything.
func TestFollowFileMove_GuardsNilAndEmptyArgs(t *testing.T) {
	store := setupTestStore(t)
	sf := database.AsSyncFileStore(store)
	require.NotNil(t, sf)

	bookID := ulid.Make().String()
	otherBookID := ulid.Make().String()
	originalID, err := sf.MintOrGetSyncFileID(bookID, "file-1")
	require.NoError(t, err)

	// nil store.
	require.NotPanics(t, func() { FollowFileMove(nil, bookID, otherBookID, []string{"file-1"}) })
	// empty old/new book ids.
	FollowFileMove(store, "", otherBookID, []string{"file-1"})
	FollowFileMove(store, bookID, "", []string{"file-1"})
	// same book id -- nothing to move.
	FollowFileMove(store, bookID, bookID, []string{"file-1"})
	// empty file id list.
	FollowFileMove(store, bookID, otherBookID, nil)

	got, ok, err := sf.GetSyncFileID(bookID, "file-1")
	require.NoError(t, err)
	require.True(t, ok, "no guard-triggering call may have moved the entry")
	require.Equal(t, originalID, got)
}

// TestFollowFileMove_SkipsEmptyFileIDsInSlice covers a defensive case: an
// empty string sneaking into the fileIDs slice must be skipped, not passed
// through to the store primitive (which would error on an empty fileID).
func TestFollowFileMove_SkipsEmptyFileIDsInSlice(t *testing.T) {
	store := setupTestStore(t)
	sf := database.AsSyncFileStore(store)
	require.NotNil(t, sf)

	oldBookID := ulid.Make().String()
	newBookID := ulid.Make().String()
	id, err := sf.MintOrGetSyncFileID(oldBookID, "file-1")
	require.NoError(t, err)

	require.NotPanics(t, func() {
		FollowFileMove(store, oldBookID, newBookID, []string{"", "file-1", ""})
	})

	got, ok, err := sf.GetSyncFileID(newBookID, "file-1")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, id, got)
}

// --- CombineBooks integration: files move to the surviving book ---

// TestCombineBooks_SyncIdentity_FileInoCarriesToSurvivor covers the main
// CombineBooks shape: real BookFile rows moved via MoveBookFilesToBook.
// Every file's sync_file `ino` must resolve under the survivor afterward,
// preserving the SAME syncFileID an offline client may have cached.
func TestCombineBooks_SyncIdentity_FileInoCarriesToSurvivor(t *testing.T) {
	store := setupTestStore(t)
	sf := database.AsSyncFileStore(store)
	require.NotNil(t, sf)

	survivor := &database.Book{ID: ulid.Make().String(), Title: "Survivor", Format: "mp3", FilePath: "/tmp/sf-survivor.mp3"}
	shell := &database.Book{ID: ulid.Make().String(), Title: "Shell", Format: "mp3", FilePath: "/tmp/sf-shell.mp3"}
	_, err := store.CreateBook(survivor)
	require.NoError(t, err)
	_, err = store.CreateBook(shell)
	require.NoError(t, err)

	shellFile := &database.BookFile{ID: ulid.Make().String(), BookID: shell.ID, FilePath: shell.FilePath, Format: "mp3"}
	require.NoError(t, store.CreateBookFile(shellFile))

	shellSyncFileID, err := sf.MintOrGetSyncFileID(shell.ID, shellFile.ID)
	require.NoError(t, err)

	ms := NewService(store)
	_, err = ms.CombineBooks([]string{survivor.ID, shell.ID}, survivor.ID, nil)
	require.NoError(t, err)

	gotID, ok, err := sf.GetSyncFileID(survivor.ID, shellFile.ID)
	require.NoError(t, err)
	require.True(t, ok, "the file's sync_file entry must resolve under the survivor")
	require.Equal(t, shellSyncFileID, gotID, "the ino must be preserved across the combine, not re-minted")

	_, ok, err = sf.GetSyncFileID(shell.ID, shellFile.ID)
	require.NoError(t, err)
	require.False(t, ok, "the absorbed shell's book id must no longer resolve the file")
}

// TestCombineBooks_SyncIdentity_NoSyncFileEntry_IsHarmlessNoOp covers the
// common case where the file was never synced to a client: CombineBooks must
// succeed exactly as before, and no sync_file entry is fabricated.
func TestCombineBooks_SyncIdentity_NoSyncFileEntry_IsHarmlessNoOp(t *testing.T) {
	store := setupTestStore(t)
	sf := database.AsSyncFileStore(store)
	require.NotNil(t, sf)

	survivor := &database.Book{ID: ulid.Make().String(), Title: "Survivor", Format: "mp3", FilePath: "/tmp/sf-survivor2.mp3"}
	shell := &database.Book{ID: ulid.Make().String(), Title: "Shell", Format: "mp3", FilePath: "/tmp/sf-shell2.mp3"}
	_, err := store.CreateBook(survivor)
	require.NoError(t, err)
	_, err = store.CreateBook(shell)
	require.NoError(t, err)

	shellFile := &database.BookFile{ID: ulid.Make().String(), BookID: shell.ID, FilePath: shell.FilePath, Format: "mp3"}
	require.NoError(t, store.CreateBookFile(shellFile))

	ms := NewService(store)
	_, err = ms.CombineBooks([]string{survivor.ID, shell.ID}, survivor.ID, nil)
	require.NoError(t, err)

	_, ok, err := sf.GetSyncFileID(survivor.ID, shellFile.ID)
	require.NoError(t, err)
	require.False(t, ok, "no sync_file entry should be fabricated for a file that was never synced")
}

// TestB3_AttachVirtualFile_ReattachExistingOwnedByOtherBook_CarriesFileIno
// extends TestB3_AttachVirtualFile_ReattachExistingOwnedByOtherBook
// (service_b3_realstore_test.go): attachVirtualFile's cross-book reattach
// branch is also a file moving onto a different book id and must carry the
// file's ino the same way the main CombineBooks loop does.
func TestB3_AttachVirtualFile_ReattachExistingOwnedByOtherBook_CarriesFileIno(t *testing.T) {
	store := setupTestStore(t)
	sf := database.AsSyncFileStore(store)
	require.NotNil(t, sf)
	ms := NewService(store)

	strayOwner := &database.Book{ID: ulid.Make().String(), Title: "Stray owner", FilePath: "/tmp/sf-stray-owner.mp3"}
	target := &database.Book{ID: ulid.Make().String(), Title: "Target", FilePath: "/tmp/sf-stray-shared.mp3"}
	_, err := store.CreateBook(strayOwner)
	require.NoError(t, err)
	_, err = store.CreateBook(target)
	require.NoError(t, err)

	strayFile := &database.BookFile{ID: ulid.Make().String(), BookID: strayOwner.ID, FilePath: target.FilePath, Format: "mp3"}
	require.NoError(t, store.CreateBookFile(strayFile))

	strayID, err := sf.MintOrGetSyncFileID(strayOwner.ID, strayFile.ID)
	require.NoError(t, err)

	n := ms.attachVirtualFile(target, target.ID)
	require.Equal(t, 1, n)

	gotID, ok, err := sf.GetSyncFileID(target.ID, strayFile.ID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, strayID, gotID, "the ino must be preserved when the stray row is reattached")
}

// --- FollowBookIDChange (untagged move) ---

// TestFollowBookIDChange_SyncFile_CarriesInoToNewBook covers the scanner's
// hash-duplicate version-link path (internal/scanner's
// followSyncIdentityOnVersionLink), which calls FollowBookIDChange directly.
// Every sync_file registered on the superseded book must resolve under the
// new book id afterward, alongside the identity/progress carry already
// covered elsewhere.
func TestFollowBookIDChange_SyncFile_CarriesInoToNewBook(t *testing.T) {
	store := setupTestStore(t)
	ids := database.AsSyncIdentityStore(store)
	sf := database.AsSyncFileStore(store)
	require.NotNil(t, ids)
	require.NotNil(t, sf)

	oldBook := &database.Book{ID: ulid.Make().String(), Title: "Old", Format: "mp3", FilePath: "/tmp/sf-old.mp3"}
	newBook := &database.Book{ID: ulid.Make().String(), Title: "New", Format: "mp3", FilePath: "/tmp/sf-new.mp3"}
	_, err := store.CreateBook(oldBook)
	require.NoError(t, err)
	_, err = store.CreateBook(newBook)
	require.NoError(t, err)

	// The old book must have a client-visible identity, or there is nothing
	// a client could have cached a file URL against in the first place.
	_, err = ids.MintOrGetSyncID(oldBook.ID)
	require.NoError(t, err)

	fileA, err := sf.MintOrGetSyncFileID(oldBook.ID, "file-a")
	require.NoError(t, err)
	fileB, err := sf.MintOrGetSyncFileID(oldBook.ID, "file-b")
	require.NoError(t, err)

	FollowBookIDChange(store, oldBook.ID, newBook.ID)

	gotA, ok, err := sf.GetSyncFileID(newBook.ID, "file-a")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, fileA, gotA)

	gotB, ok, err := sf.GetSyncFileID(newBook.ID, "file-b")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, fileB, gotB)

	list, err := sf.ListSyncFilesForBook(oldBook.ID)
	require.NoError(t, err)
	require.Empty(t, list, "old book must own no sync_file entries after the follow")
}

// TestFollowBookIDChange_SyncFile_NoIdentity_FilesNotTouched documents the
// gating decision: a book that never had a syncID minted could not have had
// a client-cached download URL either (itemId in the contentUrl IS the
// syncID), so FollowBookIDChange's existing early-return on "no syncID" is
// correct for sync_file too -- there is deliberately no separate gate here.
func TestFollowBookIDChange_SyncFile_NoIdentity_FilesNotTouched(t *testing.T) {
	store := setupTestStore(t)
	sf := database.AsSyncFileStore(store)
	require.NotNil(t, sf)

	oldBook := &database.Book{ID: ulid.Make().String(), Title: "Old", Format: "mp3", FilePath: "/tmp/sf-old2.mp3"}
	newBook := &database.Book{ID: ulid.Make().String(), Title: "New", Format: "mp3", FilePath: "/tmp/sf-new2.mp3"}
	_, err := store.CreateBook(oldBook)
	require.NoError(t, err)
	_, err = store.CreateBook(newBook)
	require.NoError(t, err)

	FollowBookIDChange(store, oldBook.ID, newBook.ID)

	list, err := sf.ListSyncFilesForBook(newBook.ID)
	require.NoError(t, err)
	require.Empty(t, list)
}

// TestFollowBookIDChange_SyncFile_IdempotentReRun mirrors the package's
// existing idempotency guarantee for identity/progress: a second call finds
// no syncID on oldBookID (already repointed) and is a complete no-op,
// including for sync_file entries.
func TestFollowBookIDChange_SyncFile_IdempotentReRun(t *testing.T) {
	store := setupTestStore(t)
	ids := database.AsSyncIdentityStore(store)
	sf := database.AsSyncFileStore(store)
	require.NotNil(t, ids)
	require.NotNil(t, sf)

	oldBook := &database.Book{ID: ulid.Make().String(), Title: "Old", Format: "mp3", FilePath: "/tmp/sf-old3.mp3"}
	newBook := &database.Book{ID: ulid.Make().String(), Title: "New", Format: "mp3", FilePath: "/tmp/sf-new3.mp3"}
	_, err := store.CreateBook(oldBook)
	require.NoError(t, err)
	_, err = store.CreateBook(newBook)
	require.NoError(t, err)
	_, err = ids.MintOrGetSyncID(oldBook.ID)
	require.NoError(t, err)
	fileID, err := sf.MintOrGetSyncFileID(oldBook.ID, "file-1")
	require.NoError(t, err)

	FollowBookIDChange(store, oldBook.ID, newBook.ID)
	FollowBookIDChange(store, oldBook.ID, newBook.ID) // must be a pure no-op

	got, ok, err := sf.GetSyncFileID(newBook.ID, "file-1")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, fileID, got)

	list, err := sf.ListSyncFilesForBook(newBook.ID)
	require.NoError(t, err)
	require.Len(t, list, 1, "re-running must not duplicate the entry")
}
