// file: internal/versions/lifecycle_retry_record_test.go
// version: 2.0.0
// guid: 2b6f8d13-9a47-4e50-8c1d-5e0a7f3b9d64
// last-edited: 2026-10-28

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

// askGuard is the permissive guard that remembers every path its deciders were
// asked about, so a test can tell a path the purge trusted from the record (never
// asked) from one it decided from the disk.
type askGuard struct {
	fsGuard
	asked []string
}

func (g *askGuard) NewDecider() PurgeDecider {
	return &askDecider{g: g, inner: g.fsGuard.NewDecider()}
}

type askDecider struct {
	g     *askGuard
	inner PurgeDecider
}

func (d *askDecider) InsideRoot(dir string) error { return d.inner.InsideRoot(dir) }

func (d *askDecider) Decide(path string) (PurgeDecision, string, error) {
	d.g.asked = append(d.g.asked, path)
	return d.inner.Decide(path)
}

// ---- the retry trusts the paths the earlier attempt unlinked, one by one ----

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

// The retry trusts the paths the first attempt unlinked and recorded, path by
// path, and decides every other path from the disk: the recorded ones are not put
// to the guard at all (they read "not there" and that is enough), the one the
// first attempt could not remove is.
func TestPurgeVersion_RetryTrustsOnlyTheRecordedPathsAndDecidesTheRest(t *testing.T) {
	f := newSharedPathFixture(t)
	flaky, clear := f.flakyRow(t)
	guard := &askGuard{}

	v, _ := f.store.GetBookVersion(f.trashed.ID)
	if err := PurgeVersion(f.store, v, guard); err == nil {
		t.Fatal("first attempt succeeded although one removal failed")
	}
	got, _ := f.store.GetBookVersion(f.trashed.ID)
	if len(got.PurgeUnlinked) != 2 {
		t.Fatalf("PurgeUnlinked = %v, want the two paths the first attempt unlinked", got.PurgeUnlinked)
	}

	clear()
	guard.asked = nil
	v, _ = f.store.GetBookVersion(f.trashed.ID)
	if err := PurgeVersion(f.store, v, guard); err != nil {
		t.Fatalf("retry: %v", err)
	}
	for _, p := range []string{f.ownOnly, f.unrowed} {
		if slices.Contains(guard.asked, p) {
			t.Errorf("retry put %s to the guard although the first attempt had unlinked and recorded it", p)
		}
	}
	if !slices.Contains(guard.asked, flaky) {
		t.Errorf("retry never decided %s, the path the first attempt could not remove (asked %v)", flaky, guard.asked)
	}
	requireGone(t, flaky)
}

// A recorded path is gone because THIS purge unlinked it; that says nothing about
// a neighbour it never unlinked, in the same folder, which reads "not there" only
// because the folder is empty (a dataset that is down). The retry must not call
// the neighbour gone, and must say so: cannot tell.
func TestPurgeVersion_RecordedPathDoesNotVouchForAnUnrecordedNeighbourInTheSameFolder(t *testing.T) {
	f := newSharedPathFixture(t)
	v, _ := f.store.GetBookVersion(f.trashed.ID)
	v.Status = database.BookVersionStatusPurging
	v.PurgeUnlinked = []string{f.ownOnly}
	if err := f.store.UpdateBookVersion(v); err != nil {
		t.Fatal(err)
	}
	// The neighbour has a row of the version's own (it is a file of the version
	// that this purge never reached).
	if err := f.store.CreateBookFile(&database.BookFile{BookID: f.holderID, FilePath: f.unrowed, VersionID: f.trashed.ID, TrackNumber: 9}); err != nil {
		t.Fatal(err)
	}
	// Attempt 1 unlinked ownOnly and recorded it; the dataset then went down: the
	// neighbour is "not there" too, and its folder (the slot) now looks empty.
	for _, p := range []string{f.ownOnly, f.unrowed} {
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	}
	guard := &fsGuard{undecidable: func(p string) error {
		if p == f.unrowed {
			return errors.New("its folder is empty, which is also how an unmounted dataset looks")
		}
		return nil
	}}

	v, _ = f.store.GetBookVersion(f.trashed.ID)
	err := PurgeVersion(f.store, v, guard)

	var ct *CannotTellError
	if !errors.Is(err, ErrPurgeCannotTell) || !errors.As(err, &ct) {
		t.Fatalf("PurgeVersion = %v, want a *CannotTellError", err)
	}
	if len(ct.Files) != 1 || ct.Files[0].Path != f.unrowed {
		t.Errorf("CannotTellError.Files = %+v, want only %s", ct.Files, f.unrowed)
	}
	requirePurging(t, f)
	got, _ := f.store.GetBookVersion(f.trashed.ID)
	if !slices.Equal(got.PurgeUnlinked, []string{f.ownOnly}) {
		t.Errorf("PurgeUnlinked = %v, want only the path unlinked by this purge", got.PurgeUnlinked)
	}
}

// A recorded path that reads "not there" is gone however its folder looks: the
// purge unlinked it, and the guard is not even asked.
func TestPurgeVersion_RecordedPathReadingNotThereIsGoneWhateverItsFolderLooksLike(t *testing.T) {
	f := newSharedPathFixture(t)
	v, _ := f.store.GetBookVersion(f.trashed.ID)
	v.Status = database.BookVersionStatusPurging
	v.PurgeUnlinked = []string{f.ownOnly, f.unrowed}
	if err := f.store.UpdateBookVersion(v); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{f.ownOnly, f.unrowed} {
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	}
	guard := &fsGuard{undecidable: func(string) error { return errors.New("every folder looks like an unmounted dataset") }}

	v, _ = f.store.GetBookVersion(f.trashed.ID)
	if err := PurgeVersion(f.store, v, guard); err != nil {
		t.Fatalf("PurgeVersion: %v", err)
	}
	got, _ := f.store.GetBookVersion(f.trashed.ID)
	if got.Status != database.BookVersionStatusInactivePurged || len(got.PurgeUnlinked) != 0 {
		t.Errorf("status=%s PurgeUnlinked=%v, want inactive_purged with the record cleared", got.Status, got.PurgeUnlinked)
	}
}

// A retry with no record of any unlink (a purge interrupted right after the
// claim) trusts no path: every one is decided from the disk.
func TestPurgeVersion_RetryWithNothingRecordedTrustsNoPath(t *testing.T) {
	f := newSharedPathFixture(t)
	v, _ := f.store.GetBookVersion(f.trashed.ID)
	v.Status = database.BookVersionStatusPurging
	if err := f.store.UpdateBookVersion(v); err != nil {
		t.Fatal(err)
	}
	guard := &askGuard{}

	v, _ = f.store.GetBookVersion(f.trashed.ID)
	if err := PurgeVersion(f.store, v, guard); err != nil {
		t.Fatalf("PurgeVersion: %v", err)
	}
	for _, p := range []string{f.ownOnly, f.unrowed} {
		if !slices.Contains(guard.asked, p) {
			t.Errorf("%s was never decided although nothing recorded it as unlinked (asked %v)", p, guard.asked)
		}
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
// success: the version stays purging with the error, and the purge STOPS there.
// The path after it is left untouched on disk, because a removal that cannot be
// recorded is one the retry cannot trust, and the store is the thing that is not
// answering.
func TestPurgeVersion_FailedRecordWriteStopsTheUnlinks(t *testing.T) {
	f := newSharedPathFixture(t)
	attempted := failRemovals(t, func(string) error { return nil })
	v, _ := f.store.GetBookVersion(f.trashed.ID)
	err := PurgeVersion(failRecordStore{f.store}, v, testGuard)
	if err == nil {
		t.Fatal("PurgeVersion succeeded although it could not record what it removed")
	}
	requirePurging(t, f)
	if len(*attempted) != 1 {
		t.Fatalf("%d removals were attempted (%v), want exactly 1: the record write failed after the first", len(*attempted), *attempted)
	}
	removed := (*attempted)[0]
	for _, p := range []string{f.ownOnly, f.unrowed} {
		if p == removed {
			requireGone(t, p)
		} else {
			requireOnDisk(t, p)
		}
	}
}

// ---- the version is read again under the lock ----

// The caller's copy is from before a lock wait that can last minutes. The stored
// version is purging with a record of what an earlier attempt unlinked; the
// purge must go by that, not by the stale copy that says "trash, no record": it
// is a retry, and it trusts the path the earlier attempt unlinked.
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
	guard := &askGuard{}

	if err := PurgeVersion(f.store, stale, guard); err != nil {
		t.Fatalf("PurgeVersion: %v", err)
	}
	if slices.Contains(guard.asked, f.ownOnly) {
		t.Errorf("%s was put to the guard although the stored record says an earlier attempt unlinked it", f.ownOnly)
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

func (g *scriptedGuard) NewDecider() PurgeDecider {
	g.calls = map[string]int{}
	return &scriptedDecider{g: g}
}

type scriptedDecider struct{ g *scriptedGuard }

func (d *scriptedDecider) InsideRoot(string) error { return nil }

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

// The unlink said "not there" for a path the decision said was there. This purge
// did not unlink it, so it is not counted as removed and not recorded as
// unlinked: it is "cannot tell", the version stays purging, and the file (the
// fake removal never touched it) is still there.
func TestPurgeVersion_NotThereOnUnlinkIsCannotTellAndNeverRecorded(t *testing.T) {
	f := newSharedPathFixture(t)
	notThereOnUnlink(t, f.ownOnly)
	guard := &scriptedGuard{decide: func(string, int) (PurgeDecision, string, error) { return PurgeRemove, "", nil }}

	v, _ := f.store.GetBookVersion(f.trashed.ID)
	err := PurgeVersion(f.store, v, guard)

	var ct *CannotTellError
	if !errors.As(err, &ct) {
		t.Fatalf("PurgeVersion = %v, want a *CannotTellError", err)
	}
	if len(ct.Files) != 1 || ct.Files[0].Path != f.ownOnly {
		t.Errorf("CannotTellError.Files = %+v, want %s", ct.Files, f.ownOnly)
	}
	requirePurging(t, f)
	requireOnDisk(t, f.ownOnly)
	got, _ := f.store.GetBookVersion(f.trashed.ID)
	if slices.Contains(got.PurgeUnlinked, f.ownOnly) {
		t.Errorf("PurgeUnlinked = %v names %s, which this purge never unlinked", got.PurgeUnlinked, f.ownOnly)
	}
}

// The control: the path that was "not there" at the unlink is decided again by
// the next attempt, from the disk, and when it is gone and its folder vouches for
// that the purge completes.
func TestPurgeVersion_NotThereOnUnlinkIsSettledByTheNextAttempt(t *testing.T) {
	f := newSharedPathFixture(t)
	// Removed by someone else between the decision and the unlink.
	failRemovals(t, func(p string) error {
		if p == f.ownOnly {
			_ = os.Remove(p)
			return syscall.ENOENT
		}
		return nil
	})

	v, _ := f.store.GetBookVersion(f.trashed.ID)
	if err := PurgeVersion(f.store, v, testGuard); !errors.Is(err, ErrPurgeCannotTell) {
		t.Fatalf("first attempt = %v, want ErrPurgeCannotTell", err)
	}
	requirePurging(t, f)

	removeFile = os.Remove
	v, _ = f.store.GetBookVersion(f.trashed.ID)
	if err := PurgeVersion(f.store, v, testGuard); err != nil {
		t.Fatalf("second attempt: %v", err)
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

// R4. A .versions folder that is a symlink to somewhere outside the library is
// not followed: the empty directories under the target's <versionID> folder stay.
func TestR4210b_PruneFollowsSymlinkedVersionsDir(t *testing.T) {
	base := t.TempDir()
	bookDir := filepath.Join(base, "lib", "Gotham", "Hush")
	outside := filepath.Join(base, "elsewhere")
	vid := "01TESTVERSIONID"
	victim := filepath.Join(outside, vid, "empty-mountpoint")
	if err := os.MkdirAll(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(bookDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(bookDir, ".versions")); err != nil {
		t.Fatal(err)
	}

	pruneEmptyDirs(VersionSlotDir(bookDir, vid))
	if err := PruneEmptyVersionsDir(bookDir); err != nil {
		t.Fatal(err)
	}

	requireOnDisk(t, victim)
	requireOnDisk(t, filepath.Join(outside, vid))
	if _, err := os.Lstat(filepath.Join(bookDir, ".versions")); err != nil {
		t.Errorf("the .versions symlink itself was removed: %v", err)
	}
}

// refuseRootDecider says no folder is inside the library root.
type refuseRootDecider struct{ PurgeDecider }

func (refuseRootDecider) InsideRoot(string) error { return errors.New("outside the library root") }

// A slot that is a real folder but resolves outside the library root is not
// pruned by a finished purge.
func TestPruneVersionDirs_LeavesASlotOutsideTheLibraryRoot(t *testing.T) {
	bookDir := t.TempDir()
	slot := VersionSlotDir(bookDir, "v1")
	if err := os.MkdirAll(filepath.Join(slot, "Disc 1"), 0o755); err != nil {
		t.Fatal(err)
	}
	dec := testGuard.NewDecider()

	pruneVersionDirs(refuseRootDecider{dec}, bookDir, "v1")
	requireOnDisk(t, slot)

	pruneVersionDirs(dec, bookDir, "v1")
	requireGone(t, slot)
	requireGone(t, VersionsDir(bookDir))
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
