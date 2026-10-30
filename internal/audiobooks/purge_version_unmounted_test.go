// file: internal/audiobooks/purge_version_unmounted_test.go
// version: 1.2.1
// guid: 7d2c9e41-0b58-4a3f-9c6e-1f8a5b3d7e20
// last-edited: 2026-10-30

package audiobooks

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/versions"
)

// These tests are about a version purge on a library whose storage is not (all)
// there: an empty mount point where a dataset should be, a root that goes away
// while the purge waits for the merge lock, an import-path list that cannot be
// read. In every one of them the file is still on disk, and the purge must not
// record the version as purged with the file reported gone or kept for good.

// libraryWitnesses gives the library two live books with files in it, so the
// root looks populated (libraryRootReady samples live files).
func libraryWitnesses(t *testing.T, f *versionGuardFixture) {
	t.Helper()
	root := config.AppConfig.RootDir
	liveBookWithFile(t, f.store, "long-halloween", filepath.Join(root, "Gotham", "long-halloween.m4b"))
	liveBookWithFile(t, f.store, "dark-victory", filepath.Join(root, "Gotham", "dark-victory.m4b"))
}

// unmountNested turns the folder dir (a nested dataset's mount point) into what
// an unmounted dataset leaves behind: an empty directory. The returned func
// mounts it again.
func unmountNested(t *testing.T, dir string) (remount func()) {
	t.Helper()
	away := dir + ".away"
	require.NoError(t, os.Rename(dir, away))
	require.NoError(t, os.MkdirAll(dir, 0o755))
	return func() {
		require.NoError(t, os.RemoveAll(dir))
		require.NoError(t, os.Rename(away, dir))
	}
}

// Run 1 cannot tell whether a file in an empty mount point is gone. Nothing is
// removable, so the version is not even claimed: it stays restorable trash. Run 2,
// with the mount state unchanged, comes to the same answer, and once the dataset
// is up the purge removes the file.
func TestVersionPurgeRetry_EmptyMountPointStaysUnexplainedOnTheSecondRun(t *testing.T) {
	f := newVersionGuardFixtureBookTrashed(t)
	libraryWitnesses(t, f)
	mnt := filepath.Join(config.AppConfig.RootDir, "Arkham")
	own := filepath.Join(mnt, "arkham-asylum.m4b")
	writeFile(t, own)
	f.ownRow(t, own, 1)
	remount := unmountNested(t, mnt)

	err1 := f.purge(t)
	require.ErrorIs(t, err1, versions.ErrPurgeCannotTell, "run 1: the folder is an empty mount point, so the file cannot be called gone")
	require.Equal(t, database.BookVersionStatusTrash, f.reread(t).Status, "nothing is removable, so the version is not claimed")
	err2 := f.purge(t)
	require.ErrorIs(t, err2, versions.ErrPurgeCannotTell, "run 2: same mount state, same answer")
	got := f.reread(t)
	remount()

	assert.Equal(t, database.BookVersionStatusTrash, got.Status, "the version stays in the trash, restorable")
	assert.Empty(t, got.PurgeUnlinked, "nothing was unlinked, so nothing is recorded as unlinked")
	assert.True(t, fileExists(own), "the file is still on the dataset")

	require.NoError(t, f.purge(t), "run 3: the dataset is up again")
	assert.False(t, fileExists(own))
	assert.Equal(t, database.BookVersionStatusInactivePurged, f.reread(t).Status)
}

// The same on the first retry of a version that was left purging by something
// else (a crash after the claim): no record of any unlink, so no folder is
// exempt.
func TestVersionPurgeRetry_UnmountedNestedFolderOfAnInterruptedPurgeIsNotGone(t *testing.T) {
	f := newVersionGuardFixtureBookTrashed(t)
	libraryWitnesses(t, f)
	nested := filepath.Join(config.AppConfig.RootDir, "Arkham")
	own := filepath.Join(nested, "Asylum", "arkham.m4b")
	writeFile(t, own)
	f.ownRow(t, own, 1)
	v := f.reread(t)
	v.Status = database.BookVersionStatusPurging
	require.NoError(t, f.store.UpdateBookVersion(v))
	remount := unmountNested(t, nested)
	require.NoError(t, os.MkdirAll(filepath.Join(nested, "Asylum"), 0o755))

	require.Error(t, f.purge(t))
	got := f.reread(t)
	remount()

	assert.Equal(t, database.BookVersionStatusPurging, got.Status)
	assert.True(t, fileExists(own))
}

// The positive half, per path: a path the earlier attempt unlinked and recorded
// (PurgeUnlinked) reads "not there" and that is enough, even though its folder is
// empty. The retry finishes the purge instead of refusing for ever.
func TestVersionPurgeRetry_RecordedPathReadsAsGoneEvenInAnEmptyFolder(t *testing.T) {
	f := newVersionGuardFixture(t)
	libraryWitnesses(t, f)
	folder := filepath.Join(config.AppConfig.RootDir, "Arkham", "Asylum")
	own := filepath.Join(folder, "arkham.m4b")
	writeFile(t, own)
	f.ownRow(t, own, 1)
	// Attempt 1 unlinked the file, recorded it, and was interrupted.
	require.NoError(t, os.Remove(own))
	v := f.reread(t)
	v.Status = database.BookVersionStatusPurging
	v.PurgeUnlinked = []string{own}
	require.NoError(t, f.store.UpdateBookVersion(v))

	require.NoError(t, f.purge(t))
	got := f.reread(t)
	assert.Equal(t, database.BookVersionStatusInactivePurged, got.Status)
	assert.Empty(t, got.PurgeUnlinked, "the record is cleared once the purge is done")
	assert.Empty(t, got.PurgeKept)
}

// R1. The record is per path: attempt 1 unlinked and recorded joker.m4b in the
// nested mount point Arkham and stopped before harley.m4b. Before the retry the
// Arkham dataset goes down: Arkham is an empty mount point and harley.m4b is still
// on the dataset. The recorded joker is gone; harley, which this purge never
// unlinked, must not be called gone because of it.
func TestR4210b_RetryRecordedSiblingDoesNotVouchForAnUnrecordedOneInAnUnmountedFolder(t *testing.T) {
	f := newVersionGuardFixtureBookTrashed(t)
	libraryWitnesses(t, f)
	mnt := filepath.Join(config.AppConfig.RootDir, "Arkham")
	joker := filepath.Join(mnt, "joker.m4b")
	harley := filepath.Join(mnt, "harley.m4b")
	writeFile(t, joker)
	writeFile(t, harley)
	f.ownRow(t, joker, 1)
	f.ownRow(t, harley, 2)

	// State left by attempt 1: joker unlinked and recorded, harley not reached.
	require.NoError(t, os.Remove(joker))
	v := f.reread(t)
	v.Status = database.BookVersionStatusPurging
	v.PurgeUnlinked = []string{joker}
	require.NoError(t, f.store.UpdateBookVersion(v))

	remount := unmountNested(t, mnt)
	perr := f.purge(t)
	got := f.reread(t)
	remount()

	assert.ErrorIs(t, perr, versions.ErrPurgeCannotTell)
	assert.True(t, fileExists(harley), "harley.m4b is still on the dataset")
	assert.Equal(t, database.BookVersionStatusPurging, got.Status,
		"the version must stay purging: harley.m4b was never unlinked and its folder is an empty mount point")
	assert.Equal(t, []string{joker}, got.PurgeUnlinked)
}

// flakyAtPath fails BookFilesAtPath(path) once, the first time it is asked after
// the version was claimed (status purging): the per-path owner re-check right
// before the removal, a transient lookup failure, the EIO-class error a dataset
// that is going bad produces. The calls before the claim are answered, whatever
// their number (the served-file guard and the owner check each ask).
type flakyAtPath struct {
	*database.PebbleStore
	path    string
	version string
	failed  bool
}

func (s *flakyAtPath) BookFilesAtPath(p string) ([]database.BookFile, error) {
	if p == s.path && !s.failed {
		if v, err := s.PebbleStore.GetBookVersion(s.version); err == nil && v != nil && v.Status == database.BookVersionStatusPurging {
			s.failed = true
			return nil, errors.New("injected transient lookup failure")
		}
	}
	return s.PebbleStore.BookFilesAtPath(p)
}

// R1b. R1 reached through the real code, no hand-built row: run 1 removes
// joker.m4b and records it, harley.m4b fails transiently; the Arkham dataset then
// drops (empty mount point); run 2 must not mark the version purged with
// harley.m4b still on the dataset.
func TestR4210b_RetrySeedExemptsSibling_RealSequence(t *testing.T) {
	f := newVersionGuardFixtureBookTrashed(t)
	libraryWitnesses(t, f)
	mnt := filepath.Join(config.AppConfig.RootDir, "Arkham")
	joker := filepath.Join(mnt, "joker.m4b")
	harley := filepath.Join(mnt, "harley.m4b")
	writeFile(t, joker)
	writeFile(t, harley)
	f.ownRow(t, joker, 1)
	f.ownRow(t, harley, 2)

	flaky := &flakyAtPath{PebbleStore: f.store, path: harley, version: f.ver.ID}
	v := f.reread(t)
	err1 := versions.PurgeVersion(flaky, v, f.svc.VersionPurgeGuard())
	after1 := f.reread(t)
	require.Error(t, err1)
	require.Equal(t, database.BookVersionStatusPurging, after1.Status)
	require.Equal(t, []string{joker}, after1.PurgeUnlinked)
	require.True(t, fileExists(harley))

	remount := unmountNested(t, mnt)
	err2 := f.purge(t)
	got := f.reread(t)
	remount()

	assert.ErrorIs(t, err2, versions.ErrPurgeCannotTell)
	assert.True(t, fileExists(harley), "harley.m4b is still on the dataset")
	assert.Equal(t, database.BookVersionStatusPurging, got.Status,
		"harley.m4b was never unlinked and its folder is an empty mount point; the version must stay purging")
	assert.Equal(t, []string{joker}, got.PurgeUnlinked)

	// And with the dataset back up, the next attempt finishes the job.
	require.NoError(t, f.purge(t))
	assert.False(t, fileExists(harley))
	assert.Equal(t, database.BookVersionStatusInactivePurged, f.reread(t).Status)
}

// R2. Crash between the unlink and its record (or the record write failing): the
// only file of the slot is gone, the slot folder is empty, and nothing is
// recorded. The purge cannot tell that from a dataset that is down, and the
// version used to sit in purging for ever (restore and hard delete both refuse
// it). The attempts are counted and the version becomes purge_failed, which can
// be hard-deleted.
func TestR4210b_UnrecordedUnlinkEscalatesToPurgeFailedSoItCanBeHardDeleted(t *testing.T) {
	f := newVersionGuardFixture(t)
	libraryWitnesses(t, f)
	slot := versions.VersionSlotDir(f.bookDir, f.ver.ID)
	own := filepath.Join(slot, "part1.m4b")
	writeFile(t, own)
	f.ownRow(t, own, 1)

	// Attempt 1: claimed, unlinked, crashed before the record.
	v := f.reread(t)
	v.Status = database.BookVersionStatusPurging
	require.NoError(t, f.store.UpdateBookVersion(v))
	require.NoError(t, os.Remove(own))

	// A stuck attempt counts at most once per calendar day, so each retry below
	// stands for a later night: forget the day the previous one was counted on.
	nextNight := func() {
		cur := f.reread(t)
		cur.PurgeUndecidedNightOn = ""
		require.NoError(t, f.store.UpdateBookVersion(cur))
	}
	for i := 1; i < versions.PurgeUndecidedEscalateAfter; i++ {
		nextNight()
		err := f.purge(t)
		require.ErrorIs(t, err, versions.ErrPurgeCannotTell, "retry %d", i)
		require.Equal(t, database.BookVersionStatusPurging, f.reread(t).Status, "retry %d", i)
	}
	assert.ErrorIs(t, f.store.DeleteBookVersion(f.ver.ID), database.ErrVersionPurging, "hard delete is refused while purging")
	assert.Error(t, versions.MarkRestored(f.reread(t)), "restore is refused: some files are gone")

	nextNight()
	err := f.purge(t)
	require.ErrorIs(t, err, versions.ErrPurgeFailed)
	got := f.reread(t)
	require.Equal(t, database.BookVersionStatusPurgeFailed, got.Status)
	require.Len(t, got.PurgeFailed, 1)
	assert.Equal(t, own, got.PurgeFailed[0].Path)
	assert.Contains(t, got.PurgeFailed[0].Reason, "cannot tell whether")

	require.NoError(t, f.store.DeleteBookVersion(f.ver.ID), "purge_failed can be hard-deleted: that is the way out")
	gone, err := f.store.GetBookVersion(f.ver.ID)
	require.NoError(t, err)
	assert.Nil(t, gone)
}

// R3. A trashed version whose only file is already gone from a folder that is gone
// too (the owner tidied up by hand), or whose dataset is down at 06:00. The first
// purge used to CLAIM it (purging) before deciding, and a version that was
// restorable trash became an unrestorable, undeletable row. Nothing is removable
// and nothing was ever unlinked, so it stays trash.
func TestR4210b_AlreadyMissingFolderLeavesRestorableTrashInTheTrash(t *testing.T) {
	f := newVersionGuardFixtureBookTrashed(t)
	libraryWitnesses(t, f)
	own := filepath.Join(config.AppConfig.RootDir, "Gotham", "No Mans Land", "nml.m4b")
	f.ownRow(t, own, 1) // never on disk / folder deleted by hand

	require.Equal(t, database.BookVersionStatusTrash, f.reread(t).Status)
	err := f.purge(t)
	var ct *versions.CannotTellError
	require.ErrorAs(t, err, &ct)
	require.Len(t, ct.Files, 1)
	assert.Equal(t, own, ct.Files[0].Path)
	got := f.reread(t)
	assert.Equal(t, database.BookVersionStatusTrash, got.Status,
		"nothing was removed, so the version should still be restorable trash, not purging")
	assert.Empty(t, got.PurgeUnlinked)
	assert.Zero(t, got.PurgeUndecidedNights)
	require.NoError(t, versions.MarkRestored(got))
	require.NoError(t, f.store.UpdateBookVersion(got), "the restore goes through")
}

// signalingGuard tells the test when the purge has asked the real guard whether
// the library is mounted for the first time, which is before it waits for the
// merge lock.
type signalingGuard struct {
	versions.PurgeGuard
	once  sync.Once
	asked chan struct{}
}

func (g *signalingGuard) RootReady() error {
	err := g.PurgeGuard.RootReady()
	g.once.Do(func() { close(g.asked) })
	return err
}

// The library goes away after the first root check passed and while the purge
// waits for the merge lock. Every path then fails to resolve inside a root that
// is not there; that is the library being down, not a verdict on the files. The
// purge asks again once it holds the lock, refuses with ErrLibraryRootNotReady,
// and the version is still in the trash with its file on disk.
func TestVersionPurge_LibraryRootVanishingDuringTheMergeLockWaitIsNotAVerdict(t *testing.T) {
	f := newVersionGuardFixture(t)
	slotFile := filepath.Join(versions.VersionSlotDir(f.bookDir, f.ver.ID), "part1.m4b")
	writeFile(t, slotFile)
	f.ownRow(t, slotFile, 1)
	libraryWitnesses(t, f)

	merge.LockMergeRMW()
	locked := true
	defer func() {
		if locked {
			merge.UnlockMergeRMW()
		}
	}()
	guard := &signalingGuard{PurgeGuard: f.svc.VersionPurgeGuard(), asked: make(chan struct{})}
	v := f.reread(t)
	done := make(chan error, 1)
	go func() { done <- versions.PurgeVersion(f.store, v, guard) }()
	select {
	case <-guard.asked:
	case <-time.After(10 * time.Second):
		t.Fatal("the purge never asked whether the library is mounted")
	}
	root := config.AppConfig.RootDir
	away := root + ".away"
	require.NoError(t, os.Rename(root, away)) // the mount goes away: no mount point left at root
	merge.UnlockMergeRMW()
	locked = false
	perr := <-done
	require.NoError(t, os.Rename(away, root)) // and comes back

	require.ErrorIs(t, perr, ErrLibraryRootNotReady)
	got := f.reread(t)
	assert.Equal(t, database.BookVersionStatusTrash, got.Status, "nothing was claimed or decided")
	assert.Empty(t, got.PurgeKept, "no file is reported as kept for good")
	assert.True(t, fileExists(slotFile))
}

// Even without the second root check, a path that cannot be resolved because
// the root itself is not there is a question the decider could not answer, not
// a permanent keep: "kept" ends the version for good and nothing looks at its
// files again.
func TestVersionPurgeGuard_UnresolvableRootIsTransientNotAPermanentKeep(t *testing.T) {
	f := newVersionGuardFixture(t)
	libraryWitnesses(t, f)
	own := filepath.Join(config.AppConfig.RootDir, "Gotham", "Year One", "year-one.m4b")
	root := config.AppConfig.RootDir
	away := root + ".away"
	require.NoError(t, os.Rename(root, away))
	defer func() { require.NoError(t, os.Rename(away, root)) }()

	decision, reason, err := f.svc.VersionPurgeGuard().NewDecider().Decide(own)

	require.Error(t, err, "decision=%v reason=%q: a missing root is not a verdict", decision, reason)
	assert.ErrorIs(t, err, fs.ErrNotExist)
	assert.Equal(t, versions.PurgeKeep, decision)
	assert.Empty(t, reason, "no reason means it is not recorded as kept")
}

// failingImportPaths is a store whose import-path list cannot be read.
type failingImportPaths struct{ *database.PebbleStore }

func (failingImportPaths) GetAllImportPaths() ([]database.ImportPath, error) {
	return nil, errors.New("injected import-path read failure")
}

func resetImportPathCache() {
	importPathCacheForHelperMu.Lock()
	defer importPathCacheForHelperMu.Unlock()
	importPathCacheForHelper = nil
	importPathCacheForHelperAt = time.Time{}
}

// A deletion path that cannot read the import-path list does not know whether
// the file is under one. It used to read that as "not under one" and go on to
// remove the file; it must leave the file alone and say it could not tell.
func TestVersionPurgeGuard_UnreadableImportPathListLeavesTheFileAlone(t *testing.T) {
	f := newVersionGuardFixture(t)
	libraryWitnesses(t, f)
	own := filepath.Join(config.AppConfig.RootDir, "Gotham", "Year One", "hush.m4b")
	writeFile(t, own)
	f.ownRow(t, own, 1)
	svc := NewAudiobookService(failingImportPaths{f.store})
	resetImportPathCache()
	t.Cleanup(resetImportPathCache)

	decision, _, err := svc.VersionPurgeGuard().NewDecider().Decide(own)

	require.Error(t, err)
	assert.ErrorIs(t, err, errPathLookupFailed)
	assert.NotEqual(t, versions.PurgeRemove, decision)
	assert.True(t, fileExists(own))
}

// The book purge shares the decision: an unreadable import-path list keeps the
// file there too, as a failed lookup (the book stays in the trash to be retried).
func TestPlanPurgePath_UnreadableImportPathListIsAFailedLookup(t *testing.T) {
	f := newVersionGuardFixture(t)
	libraryWitnesses(t, f)
	own := filepath.Join(config.AppConfig.RootDir, "Gotham", "Year One", "hush.m4b")
	writeFile(t, own)
	svc := NewAudiobookService(failingImportPaths{f.store})
	resetImportPathCache()
	t.Cleanup(resetImportPathCache)

	v := svc.planPurgePathIn(own, nil, nil)

	assert.Empty(t, v.remove)
	assert.False(t, v.protected)
	require.Error(t, v.keptErr)
	assert.ErrorIs(t, v.keptErr, errPathLookupFailed)
}

// A path that IS protected stays protected when the list cannot be read: the
// iTunes and .failed rules do not depend on it.
func TestPlanPurgePath_ProtectedByOtherRulesStillWinsWhenTheListIsUnreadable(t *testing.T) {
	f := newVersionGuardFixture(t)
	libraryWitnesses(t, f)
	failed := filepath.Join(config.AppConfig.RootDir, "Gotham", ".failed", "knightfall.m4b")
	writeFile(t, failed)
	svc := NewAudiobookService(failingImportPaths{f.store})
	resetImportPathCache()
	t.Cleanup(resetImportPathCache)

	v := svc.planPurgePathIn(failed, nil, nil)

	assert.True(t, v.protected)
	assert.NoError(t, v.keptErr)
}
