// file: internal/audiobooks/purge_retired_holder_test.go
// version: 1.0.0
// guid: 2b8d4f60-7c13-4a95-b0e8-5d1f93a7c246
// last-edited: 2026-10-23

package audiobooks

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// retireRowInto creates a shell book with one book_file row and retires that row
// into holderID, the way the retire-versions repair does: the row ends up keyed
// under the holder, flagged Retired, tied to a retired version of the holder.
func retireRowInto(t *testing.T, store *database.PebbleStore, holderID, name string) {
	t.Helper()
	shell, err := store.CreateBook(&database.Book{Title: "Shell " + name, FilePath: filepath.Join(libRoot(), "shell-"+name)})
	require.NoError(t, err)
	retiredAt := time.Now()
	ver, err := store.CreateBookVersion(&database.BookVersion{
		BookID: holderID, Status: database.BookVersionStatusRetired, Format: "mp3", Source: "retired_book",
		RetiredFromBookID: shell.ID, RetireOpID: "op-" + name, RetiredAt: &retiredAt,
	})
	require.NoError(t, err)
	row := &database.BookFile{BookID: shell.ID, FilePath: filepath.Join(libRoot(), "shell-"+name, "01.mp3")}
	require.NoError(t, store.CreateBookFile(row))
	require.NoError(t, store.RetireBookFiles([]database.BookFileMove{{FileIDs: []string{row.ID}, SourceBookID: shell.ID}}, holderID, ver.ID))
	live, err := store.GetBookFiles(holderID)
	require.NoError(t, err)
	require.Empty(t, live, "fixture: the retired row must be hidden from GetBookFiles")
}

// A trashed book that holds retired versions and no live file is not purged. It
// is reported under its own reason, with no purge side effect (no tombstone, no
// carried state), and the book row stays. The control in the same run is an
// ordinary trashed book with no rows, which is purged: the run really purges.
func TestPurgeSoftDeletedBooks_HolderOfRetiredVersionsIsKept(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	trashedHolder(t, store, "holder", "a")
	softDeleted(t, store, "empty", "")

	res, err := svc.PurgeSoftDeletedBooks(context.Background(), false, nil)
	require.NoError(t, err)
	require.Equal(t, 2, res.Attempted, "result %+v", res)
	require.Equal(t, 1, res.Purged, "the book with no rows is purged: result %+v", res)
	require.True(t, bookGone(t, store, "empty"))

	require.Equal(t, 1, res.SkippedHoldsRetiredVersions, "result %+v", res)
	require.Equal(t, []string{"holder"}, res.SkippedHoldsRetiredVersionsIDs)
	require.Zero(t, res.SkippedOwnsFiles, "a holder of retired versions only is not reported as owning files")
	require.Empty(t, res.Errors, "a refusal is not an error")
	require.False(t, bookGone(t, store, "holder"))
	tomb, err := store.GetBookTombstone("holder")
	require.NoError(t, err)
	require.Nil(t, tomb, "no purge side effect ran for a book that was kept")
	retired, err := store.GetRetiredBookFiles("holder")
	require.NoError(t, err)
	require.Len(t, retired, 1, "the retired version is untouched")
}

// A trashed book that owns a live file AND holds a retired version is reported
// as owning files: the live row is the more basic reason.
func TestPurgeSoftDeletedBooks_LiveRowWinsOverRetiredHolding(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	trashedHolder(t, store, "mixed", "b")
	require.NoError(t, store.CreateBookFile(&database.BookFile{BookID: "mixed", FilePath: filepath.Join(libRoot(), "mixed", "01.mp3")}))

	res, err := svc.PurgeSoftDeletedBooks(context.Background(), false, nil)
	require.NoError(t, err)
	require.Equal(t, 1, res.SkippedOwnsFiles, "result %+v", res)
	require.Equal(t, []string{"mixed"}, res.SkippedOwnsFilesIDs)
	require.Zero(t, res.SkippedHoldsRetiredVersions)
	require.False(t, bookGone(t, store, "mixed"))
}

// "Purge now" and the hard delete of a trashed holder fail with an error that is
// both ErrHoldsRetiredVersions and ErrBookOwnsFiles (the 409 mapping callers
// already have for the latter stays right), and delete nothing. The control is
// the same call on a trashed book with no rows, which purges.
func TestDeleteAudiobook_TrashedHolderOfRetiredVersionsIsRefused(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	trashedHolder(t, store, "holder", "c")
	softDeleted(t, store, "empty", "")

	_, err := svc.DeleteAudiobook(context.Background(), "holder", &DeleteAudiobookOptions{})
	require.ErrorIs(t, err, ErrHoldsRetiredVersions)
	require.ErrorIs(t, err, database.ErrBookOwnsFiles)
	require.False(t, bookGone(t, store, "holder"))
	tomb, terr := store.GetBookTombstone("holder")
	require.NoError(t, terr)
	require.Nil(t, tomb)

	res, err := svc.DeleteAudiobook(context.Background(), "empty", &DeleteAudiobookOptions{})
	require.NoError(t, err, "control: a trashed book with no rows purges")
	require.Equal(t, "audiobook purged", res["message"])
	require.True(t, bookGone(t, store, "empty"))
}

// The hard delete of a LIVE book that holds only retired versions is refused
// before the hash block is written: GetBookFiles hides the retired row, so
// without the include-retired pre-check the block would be recorded and
// DeleteBook would then refuse. The control is a live book with no rows: it is
// deleted and its hash blocked, so the block path is reachable.
func TestDeleteAudiobook_LiveHolderOfRetiredVersionsWritesNoHashBlock(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	heldHash, freeHash := "hash-of-the-holder", "hash-of-the-free-book"
	_, err := store.CreateBook(&database.Book{ID: "holder", Title: "holder", FilePath: filepath.Join(libRoot(), "holder"), FileHash: &heldHash})
	require.NoError(t, err)
	retireRowInto(t, store, "holder", "d")
	_, err = store.CreateBook(&database.Book{ID: "free", Title: "free", FilePath: filepath.Join(libRoot(), "free"), FileHash: &freeHash})
	require.NoError(t, err)

	_, err = svc.DeleteAudiobook(context.Background(), "holder", &DeleteAudiobookOptions{BlockHash: true})
	require.ErrorIs(t, err, ErrHoldsRetiredVersions)
	require.ErrorIs(t, err, database.ErrBookOwnsFiles)
	require.False(t, bookGone(t, store, "holder"))
	blocked, err := store.GetBlockedHashByHash(heldHash)
	require.NoError(t, err)
	require.Nil(t, blocked, "a hash block was left behind by a delete that was refused")

	_, err = svc.DeleteAudiobook(context.Background(), "free", &DeleteAudiobookOptions{BlockHash: true})
	require.NoError(t, err, "control: a live book with no rows is deleted")
	require.True(t, bookGone(t, store, "free"))
	blocked, err = store.GetBlockedHashByHash(freeHash)
	require.NoError(t, err)
	require.NotNil(t, blocked, "control: the hash of the deleted book is blocked")
}

// DiscardProgressAndPurge refuses a trashed holder of retired versions before
// any listening state is cleared, wrapping both errors; the control (a trashed
// book with progress and no rows) is cleared and purged.
func TestDiscardProgressAndPurge_RefusesHolderOfRetiredVersions(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	svc.auditOverride = &fakeRecorder{}
	trashedHolder(t, store, "holder", "e")
	u := purgeSeedProgress(t, store, "holder")

	_, err := svc.DiscardProgressAndPurge(context.Background(), "holder", "owner")
	require.ErrorIs(t, err, ErrHoldsRetiredVersions)
	require.ErrorIs(t, err, database.ErrBookOwnsFiles)
	require.False(t, bookGone(t, store, "holder"))
	require.Equal(t, 40, userPct(t, store, u.ID, "holder"), "no state was cleared")

	softDeleted(t, store, "free", "")
	require.NoError(t, store.SetUserBookState(&database.UserBookState{
		UserID: u.ID, BookID: "free", Status: database.UserBookStatusInProgress,
		ProgressPct: 10, LastActivityAt: time.Now(),
	}))
	_, err = svc.DiscardProgressAndPurge(context.Background(), "free", "owner")
	require.NoError(t, err, "control: a trashed book with progress and no rows is discarded and purged")
	require.True(t, bookGone(t, store, "free"))
}

// trashedHolder creates a live book, retires one row into it and then trashes it:
// the chain case, a holder that is itself trashed after the retire (a retire
// refuses a soft-deleted holder, so the order is the only way to build it).
func trashedHolder(t *testing.T, store *database.PebbleStore, id, name string) {
	t.Helper()
	_, err := store.CreateBook(&database.Book{ID: id, Title: id, FilePath: filepath.Join(libRoot(), id)})
	require.NoError(t, err)
	retireRowInto(t, store, id, name)
	yes := true
	old := time.Now().Add(-time.Hour)
	_, err = store.ModifyBook(id, func(b *database.Book) error {
		b.MarkedForDeletion = &yes
		b.MarkedForDeletionAt = &old
		return nil
	})
	require.NoError(t, err)
}

// libRoot is the library root setupPurgeBoundary pointed the config at.
func libRoot() string { return config.AppConfig.RootDir }
