// file: internal/versions/lifecycle_retry_seed_test.go
// version: 1.0.0
// guid: 2b6f8d13-9a47-4e50-8c1d-5e0a7f3b9d64
// last-edited: 2026-10-27

package versions

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// seedGuard is the permissive guard that remembers the seed folders each
// purge's decider was started with and how often the root was asked about.
type seedGuard struct {
	fsGuard
	seeds [][]string
}

func (g *seedGuard) NewDecider(seedDirs []string) PurgeDecider {
	g.seeds = append(g.seeds, slices.Clone(seedDirs))
	return g.fsGuard.NewDecider(seedDirs)
}

func (g *seedGuard) lastSeeds(t *testing.T) []string {
	t.Helper()
	if len(g.seeds) == 0 {
		t.Fatal("no decider was started")
	}
	return g.seeds[len(g.seeds)-1]
}

// ---- the retry seeds only what the earlier attempt emptied ----

// Every removal is recorded on the version as it happens, and a file that could
// not be removed is not recorded.
func TestPurgeVersion_RecordsEachUnlinkedPathAndNotTheOneThatFailed(t *testing.T) {
	f := newSharedPathFixture(t)
	flaky, _ := f.flakyRow(t)

	v, _ := f.store.GetBookVersion(f.trashed.ID)
	if err := PurgeVersion(f.store, v, testGuard); err == nil {
		t.Fatal("PurgeVersion succeeded although one removal failed")
	}

	got, _ := f.store.GetBookVersion(f.trashed.ID)
	want := []string{f.ownOnly, f.unrowed}
	slices.Sort(want)
	have := slices.Clone(got.PurgeUnlinked)
	slices.Sort(have)
	if !slices.Equal(have, want) {
		t.Errorf("PurgeUnlinked = %v, want %v (and not the failed %s)", got.PurgeUnlinked, want, flaky)
	}
	requireOnDisk(t, flaky)
}

// The retry seeds the folder the first attempt unlinked in, and not the folder
// of the file it could not remove: a folder where nothing was unlinked is not
// explained by this purge, and an empty one may be a mount point that is down.
func TestPurgeVersion_RetrySeedsOnlyTheFoldersItUnlinkedIn(t *testing.T) {
	f := newSharedPathFixture(t)
	flaky, clear := f.flakyRow(t)
	guard := &seedGuard{}

	v, _ := f.store.GetBookVersion(f.trashed.ID)
	if err := PurgeVersion(f.store, v, guard); err == nil {
		t.Fatal("first attempt succeeded although one removal failed")
	}
	if got := guard.lastSeeds(t); len(got) != 0 {
		t.Errorf("first attempt seeds = %v, want none", got)
	}

	clear()
	v, _ = f.store.GetBookVersion(f.trashed.ID)
	if err := PurgeVersion(f.store, v, guard); err != nil {
		t.Fatalf("retry: %v", err)
	}
	for _, dir := range guard.lastSeeds(t) {
		if dir == filepath.Dir(flaky) {
			t.Errorf("retry seeded %s, the folder of a file no attempt had unlinked", dir)
		}
		if dir != filepath.Dir(f.ownOnly) {
			t.Errorf("retry seeded %s, want only %s", dir, filepath.Dir(f.ownOnly))
		}
	}
	if len(guard.lastSeeds(t)) == 0 {
		t.Errorf("retry seeded nothing, want the slot folder the first attempt emptied")
	}
}

// A retry that has no record of any unlink (a purge interrupted right after the
// claim) seeds nothing at all.
func TestPurgeVersion_RetryWithNothingUnlinkedSeedsNothing(t *testing.T) {
	f := newSharedPathFixture(t)
	v, _ := f.store.GetBookVersion(f.trashed.ID)
	v.Status = database.BookVersionStatusPurging
	if err := f.store.UpdateBookVersion(v); err != nil {
		t.Fatal(err)
	}
	guard := &seedGuard{}

	v, _ = f.store.GetBookVersion(f.trashed.ID)
	if err := PurgeVersion(f.store, v, guard); err != nil {
		t.Fatalf("PurgeVersion: %v", err)
	}
	if got := guard.lastSeeds(t); len(got) != 0 {
		t.Errorf("seeds = %v, want none: nothing was recorded as unlinked", got)
	}
}

// The record is written after the removal and never before: while the unlink is
// running, the path is not yet on the version, so a crash at that instant cannot
// leave an unremoved path recorded as unlinked.
func TestPurgeVersion_PathIsRecordedAfterItsUnlinkNotBefore(t *testing.T) {
	f := newSharedPathFixture(t)
	orig := removeFile
	var recordedAtUnlink = map[string]bool{}
	removeFile = func(path string) error {
		cur, _ := f.store.GetBookVersion(f.trashed.ID)
		recordedAtUnlink[path] = slices.Contains(cur.PurgeUnlinked, path)
		return orig(path)
	}
	t.Cleanup(func() { removeFile = orig })

	v, _ := f.store.GetBookVersion(f.trashed.ID)
	if err := PurgeVersion(f.store, v, testGuard); err != nil {
		t.Fatalf("PurgeVersion: %v", err)
	}
	if len(recordedAtUnlink) == 0 {
		t.Fatal("nothing was removed")
	}
	for p, was := range recordedAtUnlink {
		if was {
			t.Errorf("%s was already recorded as unlinked while its unlink was running", p)
		}
	}
}

// failRecordStore refuses the write that records an unlink.
type failRecordStore struct{ *database.PebbleStore }

func (s failRecordStore) UpdateBookVersion(v *database.BookVersion) error {
	if v.Status == database.BookVersionStatusPurging && len(v.PurgeUnlinked) > 0 {
		return errors.New("injected record failure")
	}
	return s.PebbleStore.UpdateBookVersion(v)
}

// A removal whose record could not be written is a failure to retry, not a
// success: the version stays purging with the error, and the retry finds the
// file gone (and the folder, once recorded, explained).
func TestPurgeVersion_UnrecordedUnlinkLeavesTheVersionPurging(t *testing.T) {
	f := newSharedPathFixture(t)
	v, _ := f.store.GetBookVersion(f.trashed.ID)
	err := PurgeVersion(failRecordStore{f.store}, v, testGuard)
	if err == nil {
		t.Fatal("PurgeVersion succeeded although it could not record what it removed")
	}
	requirePurging(t, f)
	requireGone(t, f.ownOnly)
}

// ---- the version is read again under the lock ----

// The caller's copy is from before a lock wait that can last minutes. The stored
// version is purging with a record of what an earlier attempt unlinked; the
// purge must go by that, not by the stale copy that says "trash, no record": it
// is a retry, and it seeds the folder the earlier attempt emptied.
func TestPurgeVersion_StaleCopyOfAPurgingVersionIsStillARetry(t *testing.T) {
	f := newSharedPathFixture(t)
	stale, _ := f.store.GetBookVersion(f.trashed.ID)
	live, _ := f.store.GetBookVersion(f.trashed.ID)
	live.Status = database.BookVersionStatusPurging
	live.PurgeUnlinked = []string{f.ownOnly}
	if err := f.store.UpdateBookVersion(live); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(f.ownOnly); err != nil {
		t.Fatal(err)
	}
	guard := &seedGuard{}

	if err := PurgeVersion(f.store, stale, guard); err != nil {
		t.Fatalf("PurgeVersion: %v", err)
	}
	if got := guard.lastSeeds(t); !slices.Equal(got, []string{filepath.Dir(f.ownOnly)}) {
		t.Errorf("seeds = %v, want the folder the earlier attempt unlinked in", got)
	}
	if stale.Status != database.BookVersionStatusInactivePurged {
		t.Errorf("the caller's copy is %s, want it refreshed to inactive_purged", stale.Status)
	}
}

// A version another purge finished while this one waited for the lock is
// reported as already purged (as well as no longer in the trash), and nothing is
// touched.
func TestPurgeVersion_AlreadyPurgedWhileWaitingIsSaidSo(t *testing.T) {
	f := newSharedPathFixture(t)
	stale, _ := f.store.GetBookVersion(f.trashed.ID)
	other, _ := f.store.GetBookVersion(f.trashed.ID)
	if err := PurgeVersion(f.store, other, testGuard); err != nil {
		t.Fatal(err)
	}
	// Put the unrelated files back so a second purge would have something to do.
	if err := os.WriteFile(f.ownOnly, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := PurgeVersion(f.store, stale, testGuard)
	if !errors.Is(err, ErrVersionNotInTrash) || !errors.Is(err, ErrVersionAlreadyPurged) {
		t.Fatalf("PurgeVersion(stale, already purged) = %v, want ErrVersionNotInTrash and ErrVersionAlreadyPurged", err)
	}
	requireOnDisk(t, f.ownOnly)
}

// A restore is not "already purged".
func TestPurgeVersion_RestoredWhileWaitingIsNotCalledPurged(t *testing.T) {
	f := newSharedPathFixture(t)
	stale, _ := f.store.GetBookVersion(f.trashed.ID)
	restored, _ := f.store.GetBookVersion(f.trashed.ID)
	if err := MarkRestored(restored); err != nil {
		t.Fatal(err)
	}
	if err := f.store.UpdateBookVersion(restored); err != nil {
		t.Fatal(err)
	}

	err := PurgeVersion(f.store, stale, testGuard)
	if !errors.Is(err, ErrVersionNotInTrash) {
		t.Fatalf("PurgeVersion = %v, want ErrVersionNotInTrash", err)
	}
	if errors.Is(err, ErrVersionAlreadyPurged) {
		t.Errorf("a restored version was reported as already purged: %v", err)
	}
	requireOnDisk(t, f.ownOnly)
}

// ---- the library root is checked again once the lock is held ----

// rootFlipsGuard says the library is ready the first time it is asked (before
// the lock wait) and not ready afterwards (the lock is held; the mount went away
// in between).
type rootFlipsGuard struct {
	fsGuard
	asked int
}

func (g *rootFlipsGuard) RootReady() error {
	g.asked++
	if g.asked > 1 {
		return errors.New("the library does not look mounted")
	}
	return nil
}

func TestPurgeVersion_AsksWhetherTheLibraryIsMountedAgainOnceItHoldsTheLock(t *testing.T) {
	f := newSharedPathFixture(t)
	guard := &rootFlipsGuard{}
	v, _ := f.store.GetBookVersion(f.trashed.ID)

	err := PurgeVersion(f.store, v, guard)

	if err == nil {
		t.Fatal("PurgeVersion went ahead although the library stopped looking mounted while it waited")
	}
	if guard.asked != 2 {
		t.Errorf("RootReady asked %d times, want 2 (before the wait, and under the lock)", guard.asked)
	}
	requireTrashed(t, f)
	requireOnDisk(t, f.ownOnly)
}

// ---- a removal that says "not there" is decided again ----

// scriptedGuard answers the nth Decide of a path with decide(path, n).
type scriptedGuard struct {
	fsGuard
	decide func(path string, n int) (PurgeDecision, string, error)
	calls  map[string]int
}

func (g *scriptedGuard) NewDecider(seedDirs []string) PurgeDecider {
	g.calls = map[string]int{}
	return &scriptedDecider{g: g}
}

type scriptedDecider struct{ g *scriptedGuard }

func (d *scriptedDecider) Unlinked(string) {}

func (d *scriptedDecider) Decide(path string) (PurgeDecision, string, error) {
	d.g.calls[path]++
	return d.g.decide(path, d.g.calls[path])
}

// notThereOnUnlink makes the unlink of path answer ENOENT without touching the
// file, as when the filesystem under it went away between the decision and the
// unlink.
func notThereOnUnlink(t *testing.T, path string) {
	t.Helper()
	failRemovals(t, func(p string) error {
		if p == path {
			return syscall.ENOENT
		}
		return nil
	})
}

// The unlink said "not there" and the folder cannot vouch for that (it reads as
// an empty mount point): the path is not counted as removed. The version stays
// purging, the path is not recorded as unlinked, and the file is still there.
func TestPurgeVersion_NotThereOnUnlinkWithAFolderThatCannotVouchIsTransient(t *testing.T) {
	f := newSharedPathFixture(t)
	notThereOnUnlink(t, f.ownOnly)
	guard := &scriptedGuard{decide: func(path string, n int) (PurgeDecision, string, error) {
		if path == f.ownOnly && n > 1 {
			return PurgeKeep, "", errors.New("its folder is empty, which is also how an unmounted dataset looks")
		}
		return PurgeRemove, "", nil
	}}

	v, _ := f.store.GetBookVersion(f.trashed.ID)
	if err := PurgeVersion(f.store, v, guard); err == nil {
		t.Fatal("PurgeVersion succeeded although a path could not be shown to be gone")
	}

	requirePurging(t, f)
	requireOnDisk(t, f.ownOnly)
	got, _ := f.store.GetBookVersion(f.trashed.ID)
	if slices.Contains(got.PurgeUnlinked, f.ownOnly) {
		t.Errorf("PurgeUnlinked = %v names %s, which was never unlinked", got.PurgeUnlinked, f.ownOnly)
	}
}

// A path that is there again after "not there" is a race to retry, not gone.
func TestPurgeVersion_NotThereOnUnlinkButThereAgainIsRetried(t *testing.T) {
	f := newSharedPathFixture(t)
	notThereOnUnlink(t, f.ownOnly)
	guard := &scriptedGuard{decide: func(string, int) (PurgeDecision, string, error) { return PurgeRemove, "", nil }}

	v, _ := f.store.GetBookVersion(f.trashed.ID)
	if err := PurgeVersion(f.store, v, guard); err == nil {
		t.Fatal("PurgeVersion succeeded although a path it could not unlink is still there")
	}
	requirePurging(t, f)
	requireOnDisk(t, f.ownOnly)
}

// The control: the unlink said "not there" and the decision, asked again, says
// gone (the folder vouches for it): removed, recorded, and the purge completes.
func TestPurgeVersion_NotThereOnUnlinkWithAFolderThatVouchesCountsAsRemoved(t *testing.T) {
	f := newSharedPathFixture(t)
	notThereOnUnlink(t, f.ownOnly)
	guard := &scriptedGuard{decide: func(path string, n int) (PurgeDecision, string, error) {
		if path == f.ownOnly && n > 1 {
			return PurgeGone, "", nil
		}
		return PurgeRemove, "", nil
	}}

	v, _ := f.store.GetBookVersion(f.trashed.ID)
	if err := PurgeVersion(f.store, v, guard); err != nil {
		t.Fatalf("PurgeVersion: %v", err)
	}
	got, _ := f.store.GetBookVersion(f.trashed.ID)
	if got.Status != database.BookVersionStatusInactivePurged {
		t.Errorf("status = %s, want inactive_purged", got.Status)
	}
}

// ---- a directory that is not empty yet is a race that passes ----

// ENOTEMPTY and EEXIST (what rmdir answers for a directory something was just
// written into) are retried, not parked in purge_failed.
func TestPurgeVersion_DirectoryNotEmptyYetIsRetriedNotPurgeFailed(t *testing.T) {
	for _, errno := range []syscall.Errno{syscall.ENOTEMPTY, syscall.EEXIST} {
		t.Run(errno.Error(), func(t *testing.T) {
			f := newSharedPathFixture(t)
			failRemovals(t, func(p string) error {
				if p == f.ownOnly {
					return errno
				}
				return nil
			})
			v, _ := f.store.GetBookVersion(f.trashed.ID)
			err := PurgeVersion(f.store, v, testGuard)
			if err == nil {
				t.Fatal("PurgeVersion succeeded although a removal failed")
			}
			if errors.Is(err, ErrPurgeFailed) {
				t.Fatalf("%v marked the version purge_failed: %v", errno, err)
			}
			requirePurging(t, f)
		})
	}
}

// The permanent refusals stay permanent.
func TestRemoveErrorIsPermanent_PermissionAndReadOnlyStayPermanent(t *testing.T) {
	for _, e := range []error{syscall.EROFS, syscall.EACCES, syscall.EPERM, syscall.EISDIR, fs.ErrPermission} {
		if !removeErrorIsPermanent(e) {
			t.Errorf("removeErrorIsPermanent(%v) = false, want true", e)
		}
	}
	for _, e := range []error{syscall.EIO, syscall.ESTALE, syscall.EBUSY, syscall.ENOTEMPTY, syscall.EEXIST} {
		if removeErrorIsPermanent(e) {
			t.Errorf("removeErrorIsPermanent(%v) = true, want false", e)
		}
	}
}

// ---- the slot walk never goes through a symlink ----

func TestPruneEmptyDirs_DoesNotEnterASymlinkedSlot(t *testing.T) {
	target := t.TempDir()
	emptyInside := filepath.Join(target, "Arkham", "Asylum")
	if err := os.MkdirAll(emptyInside, 0o755); err != nil {
		t.Fatal(err)
	}
	slot := filepath.Join(t.TempDir(), ".versions", "v1")
	if err := os.MkdirAll(filepath.Dir(slot), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, slot); err != nil {
		t.Fatal(err)
	}

	pruneEmptyDirs(slot)

	requireOnDisk(t, emptyInside)
	if _, err := os.Lstat(slot); err != nil {
		t.Errorf("the symlink itself was removed: %v", err)
	}
}

// The control: a real empty slot is still pruned bottom-up.
func TestPruneEmptyDirs_StillPrunesARealEmptySlot(t *testing.T) {
	slot := filepath.Join(t.TempDir(), ".versions", "v1")
	if err := os.MkdirAll(filepath.Join(slot, "Disc 1"), 0o755); err != nil {
		t.Fatal(err)
	}
	pruneEmptyDirs(slot)
	requireGone(t, slot)
}

// ---- the stopped-for-merge streak ----

func TestCleanup_MergeStopStreakCountsConsecutiveNightsAndResets(t *testing.T) {
	f := newSharedPathFixture(t)
	prev := BackgroundMergeLockWait
	BackgroundMergeLockWait = 20 * time.Millisecond
	t.Cleanup(func() { BackgroundMergeLockWait = prev })

	merge.LockMergeRMW()
	locked := true
	defer func() {
		if locked {
			merge.UnlockMergeRMW()
		}
	}()
	for night := 1; night <= MergeStopEscalateAfter+1; night++ {
		res, err := CleanupTrashedVersions(f.store, testGuard)
		if err != nil {
			t.Fatalf("night %d: %v", night, err)
		}
		if !res.StoppedForMerge || res.MergeStopStreak != night {
			t.Fatalf("night %d: StoppedForMerge=%v streak=%d, want true and %d", night, res.StoppedForMerge, res.MergeStopStreak, night)
		}
	}

	merge.UnlockMergeRMW()
	locked = false
	res, err := CleanupTrashedVersions(f.store, testGuard)
	if err != nil || res.StoppedForMerge || res.MergeStopStreak != 0 {
		t.Fatalf("a night that got the lock: err=%v StoppedForMerge=%v streak=%d, want a clean run with streak 0", err, res.StoppedForMerge, res.MergeStopStreak)
	}
	if raw, _ := f.store.GetRaw(mergeStopStreakKey); len(raw) != 0 {
		t.Errorf("the stored streak is %q after a night that got the lock, want it cleared", raw)
	}
}

// onlyCleaner hides everything but the cleanup surface: no raw key-value space
// to keep the streak in.
type onlyCleaner struct{ TrashedVersionCleaner }

func TestCleanup_MergeStopStreakWithoutAKeyValueStoreIsThisNightAlone(t *testing.T) {
	f := newSharedPathFixture(t)
	prev := BackgroundMergeLockWait
	BackgroundMergeLockWait = 20 * time.Millisecond
	t.Cleanup(func() { BackgroundMergeLockWait = prev })
	merge.LockMergeRMW()
	defer merge.UnlockMergeRMW()

	res, err := CleanupTrashedVersions(onlyCleaner{f.store}, testGuard)
	if err != nil || !res.StoppedForMerge || res.MergeStopStreak != 1 {
		t.Fatalf("err=%v StoppedForMerge=%v streak=%d, want a stopped night counted as 1", err, res.StoppedForMerge, res.MergeStopStreak)
	}
}
