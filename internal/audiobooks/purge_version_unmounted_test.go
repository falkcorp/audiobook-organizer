// file: internal/audiobooks/purge_version_unmounted_test.go
// version: 1.0.0
// guid: 7d2c9e41-0b58-4a3f-9c6e-1f8a5b3d7e20
// last-edited: 2026-10-27

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

// Run 1 cannot tell whether a file in an empty mount point is gone and leaves
// the version purging. Run 2, with the mount state unchanged, must come to the
// same answer: nothing was unlinked in that folder, so nothing explains it being
// empty. It used to seed every doomed path's folder on a retry, which made the
// second run read the file as gone and mark the version purged.
func TestVersionPurgeRetry_EmptyMountPointStaysUnexplainedOnTheSecondRun(t *testing.T) {
	f := newVersionGuardFixture(t)
	libraryWitnesses(t, f)
	mnt := filepath.Join(config.AppConfig.RootDir, "Arkham")
	own := filepath.Join(mnt, "arkham-asylum.m4b")
	writeFile(t, own)
	f.ownRow(t, own, 1)
	remount := unmountNested(t, mnt)

	require.Error(t, f.purge(t), "run 1: the folder is an empty mount point, so the file cannot be called gone")
	require.Equal(t, database.BookVersionStatusPurging, f.reread(t).Status)
	require.Error(t, f.purge(t), "run 2: same mount state, same answer")
	got := f.reread(t)
	remount()

	assert.Equal(t, database.BookVersionStatusPurging, got.Status, "the version stays purging, to be retried once the dataset is up")
	assert.Empty(t, got.PurgeUnlinked, "nothing was unlinked, so nothing is recorded as unlinked")
	assert.True(t, fileExists(own), "the file is still on the dataset")
}

// The same on the first retry of a version that was left purging by something
// else (a crash after the claim): no record of any unlink, so no folder is
// exempt.
func TestVersionPurgeRetry_UnmountedNestedFolderOfAnInterruptedPurgeIsNotGone(t *testing.T) {
	f := newVersionGuardFixture(t)
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

// The positive half: a folder the earlier attempt itself emptied IS explained.
// The attempt recorded the file it unlinked (PurgeUnlinked), the folder is empty
// because of that, and the retry finishes the purge instead of refusing for ever.
func TestVersionPurgeRetry_FolderTheEarlierAttemptEmptiedReadsAsGone(t *testing.T) {
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

	decision, reason, err := f.svc.VersionPurgeGuard().NewDecider(nil).Decide(own)

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

	decision, _, err := svc.VersionPurgeGuard().NewDecider(nil).Decide(own)

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
