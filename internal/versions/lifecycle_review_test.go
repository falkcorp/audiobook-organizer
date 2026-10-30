// file: internal/versions/lifecycle_review_test.go
// version: 1.0.4
// guid: 7b1e4c92-5d38-4a06-b9f7-2c6a8d0e3f15
// last-edited: 2026-10-29

package versions

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// fsGuard is a PurgeGuard that decides a path from the disk alone: gone when
// nothing is there, kept when it is a directory that holds something or when
// protected says so, removable otherwise. The real guard
// (audiobooks.AudiobookService.VersionPurgeGuard) is tested in its own package
// and through the server; these tests are about what the purge does with the
// answers.
type fsGuard struct {
	rootErr   error
	protected func(path string) bool
	// undecidable returns the error of a path whose state cannot be decided yet.
	undecidable func(path string) error
	rootAsked   int
}

func (g *fsGuard) RootReady() error { g.rootAsked++; return g.rootErr }

func (g *fsGuard) NewDecider() PurgeDecider { return &fsDecider{g: g} }

type fsDecider struct{ g *fsGuard }

// InsideRoot: this guard has no library root, every folder is inside it.
func (d *fsDecider) InsideRoot(string) error { return nil }

func (d *fsDecider) Decide(path string) (PurgeDecision, string, error) {
	if d.g.protected != nil && d.g.protected(path) {
		return PurgeKeep, "protected path", nil
	}
	if d.g.undecidable != nil {
		if err := d.g.undecidable(path); err != nil {
			return PurgeKeep, "", err
		}
	}
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return PurgeGone, "", nil
	case err != nil:
		return PurgeKeep, "", err
	case info.IsDir():
		if entries, _ := os.ReadDir(path); len(entries) > 0 {
			return PurgeKeep, "a directory that still holds files", nil
		}
	}
	return PurgeRemove, "", nil
}

// testGuard is the permissive guard most tests use.
var testGuard PurgeGuard = &fsGuard{}

// cleanupAt and cleanupNow are the nightly cleanup with the permissive guard,
// reduced to the counts most tests assert on.
func cleanupAt(store TrashedVersionCleaner, now time.Time) (purged, failed int, err error) {
	res, err := cleanupTrashedVersionsAt(store, now, testGuard, true)
	return res.Purged, res.Failed, err
}

func cleanupNow(store TrashedVersionCleaner) (purged, failed int, err error) {
	return cleanupAt(store, time.Now())
}

// failRemovals makes removeFile fail for every path errFor answers with an
// error, the way a read-only filesystem or a dying disk does, and records every
// path it was asked to remove. The real removal is used for the rest.
func failRemovals(t *testing.T, errFor func(path string) error) *[]string {
	t.Helper()
	var attempted []string
	orig := removeFile
	removeFile = func(path string) error {
		attempted = append(attempted, path)
		if err := errFor(path); err != nil {
			return &fs.PathError{Op: "remove", Path: path, Err: err}
		}
		return orig(path)
	}
	t.Cleanup(func() { removeFile = orig })
	return &attempted
}

// claimHookStore runs hook the moment the purge claim (status purging) is
// written, that is after PurgeVersion has decided which paths are doomed and
// before it removes the first of them.
type claimHookStore struct {
	*database.PebbleStore
	hook func()
	done bool
}

func (s *claimHookStore) UpdateBookVersion(v *database.BookVersion) error {
	if v.Status == database.BookVersionStatusPurging && !s.done {
		s.done = true
		s.hook()
	}
	return s.PebbleStore.UpdateBookVersion(v)
}

// ---- SF1: the owners are asked again, and the lock is held, at removal ----

// A book_file row of another book that starts naming a doomed path after the
// decision and before the removal does not lose the file: the owners are asked
// again for each path right before it is unlinked.
func TestPurgeVersion_RowAddedAfterTheDecisionKeepsTheFile(t *testing.T) {
	f := newSharedPathFixture(t)
	other, err := f.store.CreateBook(&database.Book{Title: "Batman: Year One", FilePath: filepath.Join(t.TempDir(), "x.m4b")})
	if err != nil {
		t.Fatal(err)
	}
	s := &claimHookStore{PebbleStore: f.store, hook: func() {
		if err := f.store.CreateBookFile(&database.BookFile{BookID: other.ID, FilePath: f.ownOnly, TrackNumber: 1}); err != nil {
			t.Fatalf("hook row: %v", err)
		}
	}}
	v, _ := f.store.GetBookVersion(f.trashed.ID)
	if err := PurgeVersion(s, v, testGuard); err != nil {
		t.Fatalf("PurgeVersion: %v", err)
	}
	requireOnDisk(t, f.ownOnly)
	got, _ := f.store.GetBookVersion(f.trashed.ID)
	if got.Status != database.BookVersionStatusInactivePurged || !keptPath(got, f.ownOnly) {
		t.Errorf("status=%s purge_kept=%v, want inactive_purged with %s kept", got.Status, got.PurgeKept, f.ownOnly)
	}
}

// The same for a book whose own FilePath starts naming the doomed path (a book
// can name a file with no book_file row at all).
func TestPurgeVersion_BookAddedAfterTheDecisionKeepsTheFile(t *testing.T) {
	f := newSharedPathFixture(t)
	s := &claimHookStore{PebbleStore: f.store, hook: func() {
		if _, err := f.store.CreateBook(&database.Book{Title: "Batman: Year One", FilePath: f.unrowed}); err != nil {
			t.Fatalf("hook book: %v", err)
		}
	}}
	v, _ := f.store.GetBookVersion(f.trashed.ID)
	if err := PurgeVersion(s, v, testGuard); err != nil {
		t.Fatalf("PurgeVersion: %v", err)
	}
	requireOnDisk(t, f.unrowed)
	requireGone(t, f.ownOnly)
}

// The purge holds the merge lock (the lock the repoint fixers and the retire
// path write book_file rows under) from before the claim to the end: while a
// merge or combine holds it, a purge that is not willing to wait answers
// merge.ErrMergeBusy having claimed nothing and removed nothing.
func TestPurgeVersion_WaitsForTheMergeLockAndGivesUpBusyWithNothingDone(t *testing.T) {
	f := newSharedPathFixture(t)
	merge.LockMergeRMW()
	locked := true
	defer func() {
		if locked {
			merge.UnlockMergeRMW()
		}
	}()

	v, _ := f.store.GetBookVersion(f.trashed.ID)
	err := PurgeVersionWithin(f.store, v, testGuard, 30*time.Millisecond)
	if !errors.Is(err, merge.ErrMergeBusy) {
		t.Fatalf("PurgeVersionWithin under a held merge lock = %v, want merge.ErrMergeBusy", err)
	}
	for _, p := range []string{f.ownOnly, f.slotShare, f.rowShared, f.retOnly, f.unrowed} {
		requireOnDisk(t, p)
	}
	requireTrashed(t, f)

	merge.UnlockMergeRMW()
	locked = false
	if err := PurgeVersionWithin(f.store, v, testGuard, time.Second); err != nil {
		t.Fatalf("PurgeVersionWithin once the lock is free: %v", err)
	}
	requireGone(t, f.ownOnly)
}

// The lock is released when the purge ends, however it ends: a purge that
// failed part way must not leave every later merge waiting on it.
func TestPurgeVersion_ReleasesTheMergeLockWhenItFails(t *testing.T) {
	f := newSharedPathFixture(t)
	failRemovals(t, func(string) error { return syscall.EIO })
	v, _ := f.store.GetBookVersion(f.trashed.ID)
	if err := PurgeVersion(f.store, v, testGuard); err == nil {
		t.Fatal("PurgeVersion succeeded although every removal failed")
	}
	unlock, err := merge.TryLockMergeRMWWithin(100 * time.Millisecond)
	if err != nil {
		t.Fatalf("the merge lock is still held after a failed purge: %v", err)
	}
	unlock()
}

// While a merge or combine holds the merge lock, the nightly cleanup stops at
// the first purge that cannot get it instead of waiting out
// BackgroundMergeLockWait once per due version (N versions would hold the 06:00
// job for N times the wait, every one counted as a failure). Nothing is
// deleted, no version is a failure, the run says it stopped, and the next run
// purges the versions it left.
func TestCleanupTrashedVersions_StopsAtABusyMergeLockInsteadOfWaitingForEveryVersion(t *testing.T) {
	f := newSharedPathFixture(t)

	// A second due version of the same book, with a file of its own.
	trashedAt := time.Now().Add(-41 * day)
	second, err := f.store.CreateBookVersion(&database.BookVersion{
		BookID: f.holderID, Status: database.BookVersionStatusTrash, Format: "m4b", Source: "imported", TrashedAt: &trashedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	// The fixture keeps the first version's slot at <book dir>/.versions/<id>.
	secondFile := filepath.Join(filepath.Dir(filepath.Dir(f.ownOnly)), second.ID, "gotham_second.m4b")
	if err := os.MkdirAll(filepath.Dir(secondFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondFile, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.store.CreateBookFile(&database.BookFile{BookID: f.holderID, FilePath: secondFile, VersionID: second.ID, TrackNumber: 9}); err != nil {
		t.Fatal(err)
	}

	wait := 150 * time.Millisecond
	oldWait := BackgroundMergeLockWait
	BackgroundMergeLockWait = wait
	t.Cleanup(func() { BackgroundMergeLockWait = oldWait })

	merge.LockMergeRMW()
	locked := true
	defer func() {
		if locked {
			merge.UnlockMergeRMW()
		}
	}()

	started := time.Now()
	res, err := cleanupTrashedVersionsAt(f.store, time.Now(), testGuard, true)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("cleanup under a held merge lock returned %v, want nil (a busy library is not a failure)", err)
	}
	if !res.StoppedForMerge {
		t.Error("StoppedForMerge = false, want the run to say it stopped at the merge lock")
	}
	if res.Failed != 0 || res.Purged != 0 {
		t.Errorf("purged=%d failed=%d, want 0 and 0", res.Purged, res.Failed)
	}
	if elapsed >= 2*wait {
		t.Errorf("the run took %v with two due versions and a %v wait: it waited for each version instead of stopping at the first", elapsed, wait)
	}
	for _, p := range []string{f.ownOnly, f.slotShare, f.rowShared, f.retOnly, f.unrowed, secondFile} {
		requireOnDisk(t, p)
	}
	for _, id := range []string{f.trashed.ID, second.ID} {
		got, gerr := f.store.GetBookVersion(id)
		if gerr != nil || got == nil || got.Status != database.BookVersionStatusTrash || got.PurgedDate != nil {
			t.Errorf("version %s after the stopped run = %+v (err %v), want it untouched in the trash", id, got, gerr)
		}
	}

	merge.UnlockMergeRMW()
	locked = false
	next, err := cleanupTrashedVersionsAt(f.store, time.Now(), testGuard, true)
	if err != nil || next.StoppedForMerge || next.Purged != 2 || next.Failed != 0 {
		t.Fatalf("next run: purged=%d failed=%d stopped=%v err=%v, want both versions purged", next.Purged, next.Failed, next.StoppedForMerge, err)
	}
	requireGone(t, f.ownOnly)
	requireGone(t, secondFile)
}

func keptPath(v *database.BookVersion, path string) bool {
	for _, n := range v.PurgeKept {
		if n.Path == path {
			return true
		}
	}
	return false
}

// ---- SF2: a purge that can never finish ends in purge_failed ----

// A removal that fails for a reason retrying will not change (no permission, a
// read-only filesystem) ends the version in purge_failed with the paths and the
// reasons recorded, instead of leaving it purging for the nightly job to fail on
// every night. Restore is still refused, and the files that could be removed are
// gone.
func TestPurgeVersion_AnUnremovableFileEndsInPurgeFailed(t *testing.T) {
	f := newSharedPathFixture(t)
	failRemovals(t, func(p string) error {
		if p == f.ownOnly {
			return syscall.EROFS
		}
		return nil
	})
	v, _ := f.store.GetBookVersion(f.trashed.ID)
	err := PurgeVersion(f.store, v, testGuard)
	if !errors.Is(err, ErrPurgeFailed) {
		t.Fatalf("PurgeVersion = %v, want ErrPurgeFailed", err)
	}
	got, _ := f.store.GetBookVersion(f.trashed.ID)
	if got.Status != database.BookVersionStatusPurgeFailed || got.PurgedDate != nil || got.PurgeFailedAt == nil {
		t.Fatalf("status=%s PurgedDate=%v PurgeFailedAt=%v, want purge_failed, not purged, stamped", got.Status, got.PurgedDate, got.PurgeFailedAt)
	}
	if len(got.PurgeFailed) != 1 || got.PurgeFailed[0].Path != f.ownOnly || !strings.Contains(got.PurgeFailed[0].Reason, "read-only") {
		t.Errorf("purge_failed = %+v, want %s with the read-only reason", got.PurgeFailed, f.ownOnly)
	}
	requireOnDisk(t, f.ownOnly)
	requireGone(t, f.unrowed) // what could be removed was

	if err := MarkRestored(got); !errors.Is(err, database.ErrVersionPurging) {
		t.Errorf("MarkRestored(purge_failed) = %v, want ErrVersionPurging: some of its files are gone", err)
	}
	if err := MarkTrashed(got, time.Now()); !errors.Is(err, database.ErrVersionPurging) {
		t.Errorf("MarkTrashed(purge_failed) = %v, want ErrVersionPurging", err)
	}
	stale := *got
	stale.Status = database.BookVersionStatusAlt
	if err := f.store.UpdateBookVersion(&stale); !errors.Is(err, database.ErrVersionPurging) {
		t.Errorf("a stale restore write over purge_failed = %v, want ErrVersionPurging", err)
	}
}

// The nightly cleanup does not retry a purge_failed version, does not count it
// as a failure (the op would be red every night for ever), and lists it as
// needing attention. The removal is attempted once, on the first night.
func TestCleanupTrashedVersions_APurgeFailedVersionIsReportedNotRetriedOrFailedEveryNight(t *testing.T) {
	f := newSharedPathFixture(t)
	attempts := failRemovals(t, func(p string) error {
		if p == f.ownOnly {
			return syscall.EACCES
		}
		return nil
	})

	first, err := cleanupTrashedVersionsAt(f.store, time.Now(), testGuard, true)
	if !errors.Is(err, ErrPurgeFailed) || first.Failed != 1 {
		t.Fatalf("first night: failed=%d err=%v, want the purge that gave up reported once", first.Failed, err)
	}
	attemptsAfterFirst := len(*attempts)

	for night := 2; night <= 4; night++ {
		res, err := cleanupTrashedVersionsAt(f.store, time.Now(), testGuard, true)
		if err != nil || res.Failed != 0 || res.Purged != 0 {
			t.Fatalf("night %d: purged=%d failed=%d err=%v, want a quiet run", night, res.Purged, res.Failed, err)
		}
		if len(res.NeedsAttention) != 1 || res.NeedsAttention[0].VersionID != f.trashed.ID ||
			len(res.NeedsAttention[0].Files) != 1 || res.NeedsAttention[0].Files[0].Path != f.ownOnly {
			t.Fatalf("night %d: NeedsAttention = %+v, want the version and the file that cannot be removed", night, res.NeedsAttention)
		}
	}
	if len(*attempts) != attemptsAfterFirst {
		t.Errorf("the cleanup tried to remove files again on later nights: %d attempts, %d after the first night", len(*attempts), attemptsAfterFirst)
	}
}

// Purge now is the owner's retry of a purge_failed version: with the obstacle
// gone it finishes, and the failure record is cleared.
func TestPurgeVersion_RetryOfAPurgeFailedVersionFinishesAndClearsTheFailure(t *testing.T) {
	f := newSharedPathFixture(t)
	failRemovals(t, func(p string) error {
		if p == f.ownOnly {
			return syscall.EACCES
		}
		return nil
	})
	v, _ := f.store.GetBookVersion(f.trashed.ID)
	if err := PurgeVersion(f.store, v, testGuard); !errors.Is(err, ErrPurgeFailed) {
		t.Fatalf("first PurgeVersion = %v, want ErrPurgeFailed", err)
	}

	removeFile = os.Remove // the obstacle is gone (restored by failRemovals' cleanup)
	v, _ = f.store.GetBookVersion(f.trashed.ID)
	if err := PurgeVersion(f.store, v, testGuard); err != nil {
		t.Fatalf("retry of a purge_failed version: %v", err)
	}
	got, _ := f.store.GetBookVersion(f.trashed.ID)
	if got.Status != database.BookVersionStatusInactivePurged || got.PurgedDate == nil || len(got.PurgeFailed) != 0 || got.PurgeFailedAt != nil {
		t.Errorf("status=%s PurgedDate=%v purge_failed=%v at=%v, want inactive_purged with the failure cleared",
			got.Status, got.PurgedDate, got.PurgeFailed, got.PurgeFailedAt)
	}
	requireGone(t, f.ownOnly)
}

// A removal that may pass (EIO, a stale handle) is not terminal: the version
// stays purging and the next run retries it. Only the failures retrying will not
// change end in purge_failed. When both kinds happen in one purge the transient
// one decides, because the retry will meet the permanent one again anyway.
func TestPurgeVersion_ATransientRemovalFailureStaysPurgingAndIsRetried(t *testing.T) {
	f := newSharedPathFixture(t)
	failRemovals(t, func(p string) error {
		switch p {
		case f.ownOnly:
			return syscall.EIO
		case f.unrowed:
			return syscall.EACCES
		}
		return nil
	})
	v, _ := f.store.GetBookVersion(f.trashed.ID)
	err := PurgeVersion(f.store, v, testGuard)
	if err == nil || errors.Is(err, ErrPurgeFailed) {
		t.Fatalf("PurgeVersion = %v, want a retryable failure that is not ErrPurgeFailed", err)
	}
	requirePurging(t, f)
}

// A row naming a directory that still holds files used to strand the version in
// purging for ever (the purge-now, the nightly job, restore and hard delete all
// refused or failed). The directory is now a kept file, reported on the version,
// and the version ends purged.
func TestPurgeVersion_ADirectoryRowThatHoldsFilesIsKeptNotStranded(t *testing.T) {
	f := newSharedPathFixture(t)
	dir, inner := f.stuckRow(t)

	v, _ := f.store.GetBookVersion(f.trashed.ID)
	if err := PurgeVersion(f.store, v, testGuard); err != nil {
		t.Fatalf("PurgeVersion: %v", err)
	}
	got, _ := f.store.GetBookVersion(f.trashed.ID)
	if got.Status != database.BookVersionStatusInactivePurged || !keptPath(got, dir) {
		t.Errorf("status=%s purge_kept=%v, want inactive_purged with %s kept", got.Status, got.PurgeKept, dir)
	}
	requireOnDisk(t, inner)
	requireGone(t, f.ownOnly)
}

// The same through three nightly runs: the first purges it (with the directory
// kept), the next two have nothing to do. None of them fails.
func TestCleanupTrashedVersions_ADirectoryRowThatHoldsFilesDoesNotFailEveryNight(t *testing.T) {
	f := newSharedPathFixture(t)
	f.stuckRow(t)
	for night := 1; night <= 3; night++ {
		res, err := cleanupTrashedVersionsAt(f.store, time.Now(), testGuard, true)
		if err != nil || res.Failed != 0 {
			t.Fatalf("night %d: failed=%d err=%v, want none", night, res.Failed, err)
		}
	}
}

// ---- SF3: kept files are not reported as a plain purge ----

// A version purged while a trashed book names every one of its files is purged,
// and the run says so with the files that stayed: the result carries the
// version, its book and the paths, and the version carries them too.
func TestCleanupTrashedVersions_KeptFilesAreReportedWithTheVersionAndPaths(t *testing.T) {
	f := newSharedPathFixture(t)
	yes := true
	for _, p := range []string{f.ownOnly, f.unrowed} {
		if _, err := f.store.CreateBook(&database.Book{Title: "Batman: Hush " + filepath.Base(p), FilePath: p, MarkedForDeletion: &yes}); err != nil {
			t.Fatal(err)
		}
	}
	res, err := cleanupTrashedVersionsAt(f.store, time.Now(), testGuard, true)
	if err != nil || res.Purged != 1 || res.Failed != 0 {
		t.Fatalf("purged=%d failed=%d err=%v, want 1, 0, nil", res.Purged, res.Failed, err)
	}
	if len(res.KeptFiles) != 1 || res.KeptFiles[0].VersionID != f.trashed.ID || res.KeptFiles[0].BookID != f.holderID {
		t.Fatalf("KeptFiles = %+v, want the one version with its book id", res.KeptFiles)
	}
	paths := map[string]bool{}
	for _, n := range res.KeptFiles[0].Files {
		paths[n.Path] = true
		if n.Reason == "" {
			t.Errorf("kept file %s has no reason", n.Path)
		}
	}
	for _, p := range []string{f.ownOnly, f.unrowed, f.slotShare, f.rowShared, f.retOnly} {
		if !paths[p] {
			t.Errorf("kept files %v do not name %s", paths, p)
		}
	}
	got, _ := f.store.GetBookVersion(f.trashed.ID)
	if got.Status != database.BookVersionStatusInactivePurged || len(got.PurgeKept) != len(paths) {
		t.Errorf("status=%s purge_kept=%d, want inactive_purged carrying the %d kept files", got.Status, len(got.PurgeKept), len(paths))
	}
}

// The control: a purge that kept nothing reports nothing kept, and the version
// carries no kept list.
func TestCleanupTrashedVersions_ANoKeepPurgeReportsNoKeptFiles(t *testing.T) {
	f := newTrashFixtureStamped(t, time.Now().Add(-90*day), database.BookVersionStatusTrash, ptrTime(time.Now().Add(-40*day)))
	res, err := cleanupTrashedVersionsAt(f.store, time.Now(), testGuard, true)
	if err != nil || res.Purged != 1 || len(res.KeptFiles) != 0 || len(res.NeedsAttention) != 0 {
		t.Fatalf("res=%+v err=%v, want one plain purge", res, err)
	}
	if got := f.reread(t); len(got.PurgeKept) != 0 {
		t.Errorf("purge_kept = %v, want none", got.PurgeKept)
	}
	f.requireFile(t, false)
}

func ptrTime(t time.Time) *time.Time { return &t }

// ---- SF4: the book purge's path checks apply to a version purge ----

// A path the guard keeps (an import path, the iTunes library, .failed) is never
// removed, however many rows say it is the version's: it is recorded as kept.
func TestPurgeVersion_NeverRemovesAProtectedPath(t *testing.T) {
	f := newSharedPathFixture(t)
	g := &fsGuard{protected: func(p string) bool { return p == f.ownOnly }}
	v, _ := f.store.GetBookVersion(f.trashed.ID)
	if err := PurgeVersion(f.store, v, g); err != nil {
		t.Fatalf("PurgeVersion: %v", err)
	}
	requireOnDisk(t, f.ownOnly)
	requireGone(t, f.unrowed)
	got, _ := f.store.GetBookVersion(f.trashed.ID)
	if !keptPath(got, f.ownOnly) {
		t.Errorf("purge_kept = %v, want the protected %s", got.PurgeKept, f.ownOnly)
	}
}

// A library that is not mounted: nothing is claimed, nothing is removed, the
// version stays in the trash, and the error says why.
func TestPurgeVersion_AnUnreadyLibraryRootDoesNothing(t *testing.T) {
	f := newSharedPathFixture(t)
	notMounted := errors.New("the library root is not ready: injected")
	g := &fsGuard{rootErr: notMounted}
	v, _ := f.store.GetBookVersion(f.trashed.ID)
	err := PurgeVersion(f.store, v, g)
	if !errors.Is(err, notMounted) {
		t.Fatalf("PurgeVersion = %v, want the root-not-ready error", err)
	}
	for _, p := range []string{f.ownOnly, f.slotShare, f.rowShared, f.retOnly, f.unrowed} {
		requireOnDisk(t, p)
	}
	requireTrashed(t, f)

	res, cerr := cleanupTrashedVersionsAt(f.store, time.Now(), g, true)
	if cerr == nil || res.Purged != 0 || res.Failed != 1 {
		t.Errorf("cleanup on an unready root = purged %d failed %d err %v, want 0 purged, 1 failed, an error", res.Purged, res.Failed, cerr)
	}
	requireTrashed(t, f)
}

// A path whose state cannot be decided (a "not there" under a folder that cannot
// vouch for it, as on a library that is not mounted) does not count as removed:
// the version stays purging and the next run decides it again.
func TestPurgeVersion_AnUndecidablePathIsNotCountedAsRemoved(t *testing.T) {
	f := newSharedPathFixture(t)
	g := &fsGuard{undecidable: func(p string) error {
		if p == f.ownOnly {
			return errors.New("cannot tell whether " + p + " is gone: its folder is missing too")
		}
		return nil
	}}
	v, _ := f.store.GetBookVersion(f.trashed.ID)
	err := PurgeVersion(f.store, v, g)
	if err == nil || errors.Is(err, ErrPurgeFailed) || !strings.Contains(err.Error(), "cannot tell whether") {
		t.Fatalf("PurgeVersion = %v, want the undecided path reported as a retryable failure", err)
	}
	requirePurging(t, f)
	requireOnDisk(t, f.ownOnly)

	v, _ = f.store.GetBookVersion(f.trashed.ID)
	if err := PurgeVersion(f.store, v, testGuard); err != nil {
		t.Fatalf("retry once the path can be decided: %v", err)
	}
	requireGone(t, f.ownOnly)
}

// No guard configured: nothing is deleted. A purge without the checks is not an
// option the code can fall into by leaving a wire out.
func TestPurgeVersion_WithoutAGuardDeletesNothing(t *testing.T) {
	f := newSharedPathFixture(t)
	v, _ := f.store.GetBookVersion(f.trashed.ID)
	if err := PurgeVersion(f.store, v, nil); err == nil {
		t.Fatal("PurgeVersion with no guard succeeded")
	}
	requireOnDisk(t, f.ownOnly)
	requireTrashed(t, f)
}

// ---- the slot directory ----

// A purge that removed every file of the version's slot removes the slot
// directory too (and .versions when that leaves it empty), instead of leaving
// an empty folder per purged version.
func TestPurgeVersion_RemovesTheEmptySlotDirectory(t *testing.T) {
	f := newTrashFixtureStamped(t, time.Now().Add(-90*day), database.BookVersionStatusTrash, ptrTime(time.Now().Add(-40*day)))
	slot := VersionSlotDir(f.bookDir, f.ver.ID)
	if _, err := os.Stat(slot); err != nil {
		t.Fatalf("slot before: %v", err)
	}
	if err := PurgeVersion(f.store, f.reread(t), testGuard); err != nil {
		t.Fatalf("PurgeVersion: %v", err)
	}
	requireGone(t, slot)
	requireGone(t, VersionsDir(f.bookDir))
}

// A slot that still holds a kept file stays, with the directories above it.
func TestPurgeVersion_KeepsTheSlotDirectoryThatHoldsAKeptFile(t *testing.T) {
	f := newSharedPathFixture(t)
	v, _ := f.store.GetBookVersion(f.trashed.ID)
	if err := PurgeVersion(f.store, v, testGuard); err != nil {
		t.Fatalf("PurgeVersion: %v", err)
	}
	requireOnDisk(t, f.slotShare)
	requireOnDisk(t, filepath.Dir(f.slotShare))
}

// ---- N6, N9 ----

// stalePurgingStore lists the version as purging and re-reads it as purging
// whatever is stored: the view a cleanup run has of a version that another run
// finished, or a restore moved, just after the run read it.
type stalePurgingStore struct {
	*database.PebbleStore
	id       string
	snapshot []database.BookVersion
}

func (s stalePurgingStore) ListTrashedBookVersions() ([]database.BookVersion, error) {
	return s.snapshot, nil
}

func (s stalePurgingStore) GetBookVersion(id string) (*database.BookVersion, error) {
	v, err := s.PebbleStore.GetBookVersion(id)
	if err == nil && v != nil && id == s.id {
		cp := *v
		cp.Status = database.BookVersionStatusPurging
		return &cp, nil
	}
	return v, err
}

// The purging retry loses its claim to a run that finished it (or a restore)
// between the re-read and the claim: that is the other run's win, not a failure
// of this one, exactly as it is for a version in the trash.
func TestCleanupTrashedVersions_APurgingVersionThatLeftThePurgeIsNotAFailure(t *testing.T) {
	f := newSharedPathFixture(t)
	restored, _ := f.store.GetBookVersion(f.trashed.ID)
	if err := MarkRestored(restored); err != nil {
		t.Fatal(err)
	}
	if err := f.store.UpdateBookVersion(restored); err != nil {
		t.Fatal(err)
	}
	snapshot := []database.BookVersion{*restored}
	snapshot[0].Status = database.BookVersionStatusPurging
	st := stalePurgingStore{f.store, f.trashed.ID, snapshot}

	res, err := cleanupTrashedVersionsAt(st, time.Now(), testGuard, true)
	if err != nil || res.Failed != 0 || res.Purged != 0 {
		t.Fatalf("purged=%d failed=%d err=%v, want a quiet run", res.Purged, res.Failed, err)
	}
	requireOnDisk(t, f.ownOnly)
	if got, _ := f.store.GetBookVersion(f.trashed.ID); got.Status != database.BookVersionStatusAlt {
		t.Errorf("status = %s, want alt (the restore stands)", got.Status)
	}
}

// A version whose book is gone still has its slot on disk: the book's tombstone
// names the folder, and the slot's files are removed (with the same checks)
// instead of being marked purged and left behind for ever.
func TestPurgeVersion_BookGoneStillRemovesTheSlotFiles(t *testing.T) {
	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "pebble"))
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	store.WaitForWarmup()

	bookDir := t.TempDir()
	book, err := store.CreateBook(&database.Book{Title: "Batman: Arkham Asylum", FilePath: filepath.Join(bookDir, "book.m4b")})
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(-40 * day)
	ver, err := store.CreateBookVersion(&database.BookVersion{
		BookID: book.ID, Status: database.BookVersionStatusTrash, Format: "m4b", Source: "imported", TrashedAt: &stamp,
	})
	if err != nil {
		t.Fatal(err)
	}
	slot := VersionSlotDir(bookDir, ver.ID)
	if err := os.MkdirAll(slot, 0o755); err != nil {
		t.Fatal(err)
	}
	inSlot := filepath.Join(slot, "part1.m4b")
	if err := os.WriteFile(inSlot, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The book purge writes the tombstone, then deletes the row.
	if err := store.CreateBookTombstone(book); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteBook(book.ID); err != nil {
		t.Fatalf("DeleteBook: %v", err)
	}
	if gone, _ := store.GetBookByID(book.ID); gone != nil {
		t.Fatal("the book is still there")
	}

	if err := PurgeVersion(store, ver, testGuard); err != nil {
		t.Fatalf("PurgeVersion: %v", err)
	}
	requireGone(t, inSlot)
	requireGone(t, slot)
	if got, _ := store.GetBookVersion(ver.ID); got.Status != database.BookVersionStatusInactivePurged {
		t.Errorf("status = %s, want inactive_purged", got.Status)
	}
}

// And the slot of a gone book is put to the guard like any other path.
func TestPurgeVersion_BookGoneSlotFilesAreStillGuarded(t *testing.T) {
	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "pebble"))
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	store.WaitForWarmup()

	bookDir := t.TempDir()
	book, _ := store.CreateBook(&database.Book{Title: "Batman: Arkham City", FilePath: filepath.Join(bookDir, "book.m4b")})
	stamp := time.Now().Add(-40 * day)
	ver, _ := store.CreateBookVersion(&database.BookVersion{
		BookID: book.ID, Status: database.BookVersionStatusTrash, Format: "m4b", Source: "imported", TrashedAt: &stamp,
	})
	slot := VersionSlotDir(bookDir, ver.ID)
	_ = os.MkdirAll(slot, 0o755)
	inSlot := filepath.Join(slot, "part1.m4b")
	if err := os.WriteFile(inSlot, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBookTombstone(book); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteBook(book.ID); err != nil {
		t.Fatal(err)
	}
	g := &fsGuard{protected: func(p string) bool { return p == inSlot }}
	if err := PurgeVersion(store, ver, g); err != nil {
		t.Fatalf("PurgeVersion: %v", err)
	}
	requireOnDisk(t, inSlot)
	if got, _ := store.GetBookVersion(ver.ID); !keptPath(got, inSlot) {
		t.Errorf("purge_kept = %v, want the protected slot file", got.PurgeKept)
	}
}
