// file: internal/audiobooks/purge_retired_holder_test.go
// version: 1.0.0
// guid: 2b8d4f60-7c13-4a95-b0e8-5d1f93a7c246
// last-edited: 2026-10-28

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

// trashedHolder creates a live book in a version group, retires one row into it
// and then trashes it (retireRowInto, trashBook): the holder of retired versions
// is itself in the trash, with no live file row of its own.
func trashedHolder(t *testing.T, store *database.PebbleStore, id string) {
	t.Helper()
	groupBook(t, store, id, "g-"+id, false, false)
	retireRowInto(t, store, id)
	trashBook(t, store, id)
}

// A trashed book that holds retired versions and no live file is not purged. It
// is reported under its own reason, with no purge side effect (no tombstone, no
// carried state), and the book row stays. The control in the same run is an
// ordinary trashed book with no rows, which is purged: the run really purges.
func TestRunPurge_HolderOfRetiredVersionsIsKeptUnderItsOwnReason(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	trashedHolder(t, store, "holder")
	softDeleted(t, store, "empty", "")

	res, err := svc.RunPurge(context.Background(), PurgeOptions{Unattended: true})
	require.NoError(t, err)
	require.Equal(t, 2, res.Attempted, "result %+v", res)
	require.Equal(t, 1, res.Purged, "the book with no rows is purged: result %+v", res)
	require.True(t, bookGone(t, store, "empty"))

	require.Equal(t, 1, res.SkippedHoldsRetiredVersions, "result %+v", res)
	require.Equal(t, []string{"holder"}, res.SkippedHoldsRetiredVersionsIDs)
	require.Zero(t, res.SkippedOwnsFiles, "a holder of retired versions only is not reported as owning files")
	require.Empty(t, res.Errors, "a refusal is not an error")
	require.Equal(t, 2, res.Decided(), "the new reason is a decision: %+v", res)
	require.Contains(t, res.Summary(), "1: hold retired versions")
	require.False(t, bookGone(t, store, "holder"))
	tomb, err := store.GetBookTombstone("holder")
	require.NoError(t, err)
	require.Nil(t, tomb, "no purge side effect ran for a book that was kept")
	retired, err := store.GetRetiredBookFiles("holder")
	require.NoError(t, err)
	require.Len(t, retired, 1, "the retired version is untouched")
}

// The preview classifies the holder exactly as the pass that follows it does
// (both go through purgeGate), the breakdown the trash listing shows counts it
// as blocked, and the preview itself changes nothing.
func TestRunPurge_PreviewAndPassAgreeOnAHolderOfRetiredVersions(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	trashedHolder(t, store, "holder")
	softDeleted(t, store, "empty", "")

	prev, err := svc.RunPurge(context.Background(), PurgeOptions{DryRun: true})
	require.NoError(t, err)
	require.Equal(t, 1, prev.SkippedHoldsRetiredVersions, "preview %+v", prev)
	require.Equal(t, []string{"holder"}, prev.SkippedHoldsRetiredVersionsIDs)
	require.Equal(t, 1, prev.Purgeable, "preview %+v", prev)
	require.False(t, bookGone(t, store, "empty"), "a preview purges nothing")
	require.Equal(t, 1, breakdownOf(prev, time.Now()).OwnsFiles, "the listing has no separate figure: it is blocked on rows")

	run, err := svc.RunPurge(context.Background(), PurgeOptions{TrashedBefore: prev.TrashedBefore})
	require.NoError(t, err)
	require.Equal(t, prev.SkippedHoldsRetiredVersions, run.SkippedHoldsRetiredVersions, "run %+v", run)
	require.Equal(t, prev.Purgeable, run.Purged, "run %+v", run)
}

// Capped cuts the new id list like the others, so the stored operation result
// stays small however many holders there are; the count stays exact.
func TestPurgeResult_CappedCutsTheHoldsRetiredVersionsIDs(t *testing.T) {
	r := &PurgeResult{SkippedHoldsRetiredVersions: 4, SkippedHoldsRetiredVersionsIDs: []string{"a", "b", "c", "d"}}
	c := r.Capped(2)
	require.Len(t, c.SkippedHoldsRetiredVersionsIDs, 2)
	require.True(t, c.Truncated)
	require.Equal(t, 4, c.SkippedHoldsRetiredVersions)
	require.Len(t, r.SkippedHoldsRetiredVersionsIDs, 4, "the original is not modified")
}

// A trashed book that owns a live file AND holds a retired version is reported
// as owning files: the live row is the more basic reason.
func TestRunPurge_LiveRowWinsOverRetiredHolding(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	trashedHolder(t, store, "mixed")
	require.NoError(t, store.CreateBookFile(&database.BookFile{BookID: "mixed", FilePath: filepath.Join(config.AppConfig.RootDir, "mixed", "01.mp3")}))

	res, err := svc.RunPurge(context.Background(), PurgeOptions{Unattended: true})
	require.NoError(t, err)
	require.Equal(t, 1, res.SkippedOwnsFiles, "result %+v", res)
	require.Equal(t, []string{"mixed"}, res.SkippedOwnsFilesIDs)
	require.Zero(t, res.SkippedHoldsRetiredVersions)
	require.False(t, bookGone(t, store, "mixed"))
}

// "Purge now" of a trashed holder fails with an error that is both
// database.ErrBookHoldsRetiredVersions and ErrBookOwnsFiles (the 409 mapping
// callers already have for the latter stays right), and deletes nothing. The
// control is the same call on a trashed book with no rows, which purges.
func TestDeleteAudiobook_TrashedHolderOfRetiredVersionsIsRefused(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	trashedHolder(t, store, "holder")
	softDeleted(t, store, "empty", "")

	_, err := svc.DeleteAudiobook(context.Background(), "holder", &DeleteAudiobookOptions{})
	require.ErrorIs(t, err, database.ErrBookHoldsRetiredVersions)
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
	groupBook(t, store, "holder", "g-holder", false, false)
	_, err := store.ModifyBook("holder", func(b *database.Book) error { b.FileHash = &heldHash; return nil })
	require.NoError(t, err)
	retireRowInto(t, store, "holder")
	groupBook(t, store, "free", "g-free", false, false)
	_, err = store.ModifyBook("free", func(b *database.Book) error { b.FileHash = &freeHash; return nil })
	require.NoError(t, err)

	_, err = svc.DeleteAudiobook(context.Background(), "holder", &DeleteAudiobookOptions{BlockHash: true})
	require.ErrorIs(t, err, database.ErrBookHoldsRetiredVersions)
	require.ErrorIs(t, err, database.ErrBookOwnsFiles)
	require.False(t, bookGone(t, store, "holder"))
	blocked, err := store.IsHashBlocked(heldHash)
	require.NoError(t, err)
	require.False(t, blocked, "a hash block was left behind by a delete that was refused")

	_, err = svc.DeleteAudiobook(context.Background(), "free", &DeleteAudiobookOptions{BlockHash: true})
	require.NoError(t, err, "control: a live book with no rows is deleted")
	require.True(t, bookGone(t, store, "free"))
	blocked, err = store.IsHashBlocked(freeHash)
	require.NoError(t, err)
	require.True(t, blocked, "control: the hash of the deleted book is blocked")
}

// DiscardProgressAndPurge refuses a trashed holder of retired versions before
// any listening state is cleared, wrapping both errors; the control (a trashed
// book with progress and no rows) is cleared and purged.
func TestDiscardProgressAndPurge_RefusesHolderOfRetiredVersions(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	svc.auditOverride = &fakeRecorder{}
	trashedHolder(t, store, "holder")
	u := purgeSeedProgress(t, store, "holder")

	_, err := svc.DiscardProgressAndPurge(context.Background(), "holder", "owner")
	require.ErrorIs(t, err, database.ErrBookHoldsRetiredVersions)
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
