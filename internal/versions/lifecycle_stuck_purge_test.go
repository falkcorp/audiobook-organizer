// file: internal/versions/lifecycle_stuck_purge_test.go
// version: 1.2.0
// guid: 4e8b1d27-6c93-4f05-a7d2-9b3e5f0c8a61
// last-edited: 2026-10-30

package versions

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// undecidableOwn is a guard that cannot tell, for the two paths the trashed
// version alone owns, whether they are gone: the shape of a dataset that is down.
// The paths other rows name are kept by the owners question before the guard is
// asked.
func undecidableOwn(f *sharedPathFixture) *fsGuard {
	return &fsGuard{undecidable: func(p string) error {
		if p == f.ownOnly || p == f.unrowed {
			return errors.New("its folder is empty, which is also how an unmounted dataset looks")
		}
		return nil
	}}
}

func setStatus(t *testing.T, f *sharedPathFixture, status string) {
	t.Helper()
	v, _ := f.store.GetBookVersion(f.trashed.ID)
	v.Status = status
	if err := f.store.UpdateBookVersion(v); err != nil {
		t.Fatal(err)
	}
}

// A trashed version with nothing removable and paths that cannot be told apart
// from a dataset that is down is NOT claimed: it stays restorable trash, nothing
// is deleted, and the error names the paths and why.
func TestPurgeVersion_NothingRemovableAndCannotTellLeavesTheTrashedVersionInTheTrash(t *testing.T) {
	f := newSharedPathFixture(t)
	v, _ := f.store.GetBookVersion(f.trashed.ID)

	err := PurgeVersion(f.store, v, undecidableOwn(f))

	var ct *CannotTellError
	if !errors.As(err, &ct) || !errors.Is(err, ErrPurgeCannotTell) {
		t.Fatalf("PurgeVersion = %v, want a *CannotTellError", err)
	}
	if len(ct.Files) != 2 {
		t.Errorf("CannotTellError.Files = %+v, want the two paths the version alone owns", ct.Files)
	}
	requireTrashed(t, f)
	requireOnDisk(t, f.ownOnly)
	requireOnDisk(t, f.unrowed)
	// Still restorable.
	got, _ := f.store.GetBookVersion(f.trashed.ID)
	if err := MarkRestored(got); err != nil {
		t.Fatalf("MarkRestored: %v", err)
	}
	if err := f.store.UpdateBookVersion(got); err != nil {
		t.Fatalf("the store refused the restore of a version that was never claimed: %v", err)
	}
}

// The nightly cleanup files such a version under CannotTell: not purged, not a
// failure, still trash.
func TestCleanup_VersionThatCannotBeToldApartFromAnUnmountedDatasetIsListedNotFailed(t *testing.T) {
	f := newSharedPathFixture(t)

	res, err := cleanupTrashedVersionsAt(f.store, time.Now(), undecidableOwn(f), true)

	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if res.Purged != 0 || res.Failed != 0 || len(res.CannotTell) != 1 || res.CannotTell[0].VersionID != f.trashed.ID {
		t.Fatalf("Purged=%d Failed=%d CannotTell=%+v, want 0, 0 and the trashed version", res.Purged, res.Failed, res.CannotTell)
	}
	if len(res.CannotTell[0].Files) != 2 {
		t.Errorf("CannotTell files = %+v, want the two paths", res.CannotTell[0].Files)
	}
	requireTrashed(t, f)
}

// The way out. A purging version whose paths can never be settled (the file was
// unlinked but the record never written, or the dataset is down for days) is
// counted each attempt and, at PurgeUndecidedEscalateAfter, becomes purge_failed
// with the paths and the reason, which makes hard delete available. Before that
// hard delete is refused, as it must be while a purge may still be running.
func TestPurgeVersion_PurgingVersionThatNeverSettlesEscalatesToPurgeFailedAndCanBeHardDeleted(t *testing.T) {
	f := newSharedPathFixture(t)
	setStatus(t, f, database.BookVersionStatusPurging)
	guard := undecidableOwn(f)
	day := useDay(t, time.Date(2026, 10, 1, 3, 0, 0, 0, time.Local))

	for n := 1; n < PurgeUndecidedEscalateAfter; n++ {
		day.next()
		v, _ := f.store.GetBookVersion(f.trashed.ID)
		if err := PurgeVersion(f.store, v, guard); !errors.Is(err, ErrPurgeCannotTell) {
			t.Fatalf("attempt %d = %v, want ErrPurgeCannotTell", n, err)
		}
		requirePurging(t, f)
		got, _ := f.store.GetBookVersion(f.trashed.ID)
		if got.PurgeUndecidedNights != n {
			t.Fatalf("attempt %d: PurgeUndecidedNights = %d, want %d", n, got.PurgeUndecidedNights, n)
		}
		if err := f.store.DeleteBookVersion(f.trashed.ID); !errors.Is(err, database.ErrVersionPurging) {
			t.Fatalf("attempt %d: hard delete = %v, want it refused while purging", n, err)
		}
	}

	day.next()
	v, _ := f.store.GetBookVersion(f.trashed.ID)
	err := PurgeVersion(f.store, v, guard)
	if !errors.Is(err, ErrPurgeFailed) {
		t.Fatalf("attempt %d = %v, want ErrPurgeFailed", PurgeUndecidedEscalateAfter, err)
	}
	got, _ := f.store.GetBookVersion(f.trashed.ID)
	if got.Status != database.BookVersionStatusPurgeFailed || got.PurgeFailedAt == nil {
		t.Fatalf("status = %s, want purge_failed", got.Status)
	}
	var paths []string
	for _, n := range got.PurgeFailed {
		paths = append(paths, n.Path)
		if !strings.Contains(n.Reason, "cannot tell whether") {
			t.Errorf("reason for %s = %q, want it to say it cannot tell whether the file is gone", n.Path, n.Reason)
		}
	}
	slices.Sort(paths)
	want := []string{f.ownOnly, f.unrowed}
	slices.Sort(want)
	if !slices.Equal(paths, want) {
		t.Errorf("PurgeFailed paths = %v, want %v", paths, want)
	}
	if got.PurgeUndecidedNights != 0 {
		t.Errorf("PurgeUndecidedNights = %d after escalating, want it reset", got.PurgeUndecidedNights)
	}
	// purge_failed is deletable, and is listed by the cleanup rather than retried.
	res, cerr := cleanupTrashedVersionsAt(f.store, time.Now(), guard, true)
	if cerr != nil || len(res.NeedsAttention) != 1 {
		t.Errorf("cleanup: err=%v NeedsAttention=%v, want the version listed and no failure", cerr, res.NeedsAttention)
	}
	if err := f.store.DeleteBookVersion(f.trashed.ID); err != nil {
		t.Fatalf("hard delete of the escalated version: %v", err)
	}
	if gone, _ := f.store.GetBookVersion(f.trashed.ID); gone != nil {
		t.Errorf("the version is still there after the hard delete: %+v", gone)
	}
}

// The count is of attempts that settled nothing. An attempt that unlinks something
// is progress and resets it, so a purge that is slowly getting through is not
// escalated.
func TestPurgeVersion_AnAttemptThatUnlinksSomethingResetsTheUndecidedCount(t *testing.T) {
	f := newSharedPathFixture(t)
	v, _ := f.store.GetBookVersion(f.trashed.ID)
	v.Status = database.BookVersionStatusPurging
	v.PurgeUndecidedNights = PurgeUndecidedEscalateAfter - 1
	if err := f.store.UpdateBookVersion(v); err != nil {
		t.Fatal(err)
	}
	// ownOnly cannot be decided; unrowed can, and is removed.
	guard := &fsGuard{undecidable: func(p string) error {
		if p == f.ownOnly {
			return errors.New("cannot vouch")
		}
		return nil
	}}

	v, _ = f.store.GetBookVersion(f.trashed.ID)
	if err := PurgeVersion(f.store, v, guard); !errors.Is(err, ErrPurgeCannotTell) {
		t.Fatalf("PurgeVersion = %v, want ErrPurgeCannotTell (and not purge_failed)", err)
	}
	requirePurging(t, f)
	requireGone(t, f.unrowed)
	got, _ := f.store.GetBookVersion(f.trashed.ID)
	if got.PurgeUndecidedNights != 0 {
		t.Errorf("PurgeUndecidedNights = %d, want 0 after an attempt that unlinked %s", got.PurgeUndecidedNights, f.unrowed)
	}
}

// A purge that gets through clears the count with the rest of the record.
func TestPurgeVersion_FinishedPurgeClearsTheUndecidedCount(t *testing.T) {
	f := newSharedPathFixture(t)
	v, _ := f.store.GetBookVersion(f.trashed.ID)
	v.Status = database.BookVersionStatusPurging
	v.PurgeUndecidedNights = PurgeUndecidedEscalateAfter - 1
	if err := f.store.UpdateBookVersion(v); err != nil {
		t.Fatal(err)
	}
	v, _ = f.store.GetBookVersion(f.trashed.ID)
	if err := PurgeVersion(f.store, v, testGuard); err != nil {
		t.Fatalf("PurgeVersion: %v", err)
	}
	got, _ := f.store.GetBookVersion(f.trashed.ID)
	if got.Status != database.BookVersionStatusInactivePurged || got.PurgeUndecidedNights != 0 {
		t.Errorf("status=%s PurgeUndecidedNights=%d, want inactive_purged and 0", got.Status, got.PurgeUndecidedNights)
	}
}

// A version that was claimed, had its only file unlinked and never recorded that
// (the crash window) is exactly this: nothing recorded, nothing on disk, the
// folder cannot vouch. It used to sit in purging for ever.
func TestPurgeVersion_UnlinkedButNeverRecordedFileDoesNotWedgeTheVersion(t *testing.T) {
	f := newSharedPathFixture(t)
	setStatus(t, f, database.BookVersionStatusPurging)
	for _, p := range []string{f.ownOnly, f.unrowed} {
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	}
	guard := undecidableOwn(f)

	day := useDay(t, time.Date(2026, 10, 1, 3, 0, 0, 0, time.Local))
	var last error
	for n := 1; n <= PurgeUndecidedEscalateAfter; n++ {
		day.next()
		v, _ := f.store.GetBookVersion(f.trashed.ID)
		last = PurgeVersion(f.store, v, guard)
	}
	if !errors.Is(last, ErrPurgeFailed) {
		t.Fatalf("attempt %d = %v, want ErrPurgeFailed", PurgeUndecidedEscalateAfter, last)
	}
	if err := f.store.DeleteBookVersion(f.trashed.ID); err != nil {
		t.Fatalf("hard delete: %v", err)
	}
}

// ---- a version that was deleted is not a version that was restored ----

func TestPurgeVersion_HardDeletedWhileWaitingIsSaidSoNotRestored(t *testing.T) {
	f := newSharedPathFixture(t)
	stale, _ := f.store.GetBookVersion(f.trashed.ID)
	if err := f.store.DeleteBookVersion(f.trashed.ID); err != nil {
		t.Fatal(err)
	}

	err := PurgeVersion(f.store, stale, testGuard)

	if !errors.Is(err, ErrVersionNotInTrash) || !errors.Is(err, ErrVersionDeleted) || errors.Is(err, ErrVersionAlreadyPurged) {
		t.Fatalf("PurgeVersion = %v, want ErrVersionNotInTrash and ErrVersionDeleted", err)
	}
	requireOnDisk(t, f.ownOnly)
}

// deleteAtClaimStore hard-deletes the version the moment the claim is written.
type deleteAtClaimStore struct {
	*database.PebbleStore
	id   string
	done bool
}

func (s *deleteAtClaimStore) UpdateBookVersion(v *database.BookVersion) error {
	if v.Status == database.BookVersionStatusPurging && !s.done {
		s.done = true
		if err := s.PebbleStore.DeleteBookVersion(s.id); err != nil {
			return err
		}
	}
	return s.PebbleStore.UpdateBookVersion(v)
}

func TestPurgeVersion_HardDeletedBetweenTheDecisionAndTheClaimIsSaidSo(t *testing.T) {
	f := newSharedPathFixture(t)
	v, _ := f.store.GetBookVersion(f.trashed.ID)

	err := PurgeVersion(&deleteAtClaimStore{PebbleStore: f.store, id: f.trashed.ID}, v, testGuard)

	if !errors.Is(err, ErrVersionNotInTrash) || !errors.Is(err, ErrVersionDeleted) {
		t.Fatalf("PurgeVersion = %v, want ErrVersionNotInTrash and ErrVersionDeleted", err)
	}
	requireOnDisk(t, f.ownOnly)
}

// ---- a purged version is not put back in the trash ----

func TestMarkTrashed_RefusesAPurgedVersionAndSoDoesTheStore(t *testing.T) {
	f := newSharedPathFixture(t)
	v, _ := f.store.GetBookVersion(f.trashed.ID)
	if err := PurgeVersion(f.store, v, testGuard); err != nil {
		t.Fatalf("PurgeVersion: %v", err)
	}
	purged, _ := f.store.GetBookVersion(f.trashed.ID)
	if purged.Status != database.BookVersionStatusInactivePurged {
		t.Fatalf("status = %s, want inactive_purged", purged.Status)
	}

	if err := MarkTrashed(purged, time.Now()); !errors.Is(err, database.ErrVersionPurged) {
		t.Errorf("MarkTrashed = %v, want database.ErrVersionPurged", err)
	}
	if purged.Status != database.BookVersionStatusInactivePurged || purged.TrashedAt != nil {
		t.Errorf("MarkTrashed changed the caller's copy: status=%s TrashedAt=%v", purged.Status, purged.TrashedAt)
	}

	// A caller that skips MarkTrashed and writes the status itself is refused by
	// the store, under the version's stripe.
	purged.Status = database.BookVersionStatusTrash
	if err := f.store.UpdateBookVersion(purged); !errors.Is(err, database.ErrVersionPurged) {
		t.Errorf("UpdateBookVersion(trash) = %v, want database.ErrVersionPurged", err)
	}
	stored, _ := f.store.GetBookVersion(f.trashed.ID)
	if stored.Status != database.BookVersionStatusInactivePurged {
		t.Errorf("stored status = %s, want it left inactive_purged", stored.Status)
	}
}

// ---- the served-file guard is part of the read-only pass before the claim ----

// A live book that serves one of the files ends the purge before it claims the
// version, and before the question of paths that cannot be told: the version
// stays trash (restorable, not purging, nothing recorded as unlinked), nothing is
// unlinked, and the error is the BookServesFilesError, not a CannotTellError,
// even though two other paths could not be decided either.
func TestPurgeVersion_AServedFileEndsThePurgeBeforeTheClaimAndBeforeCannotTell(t *testing.T) {
	f := newSharedPathFixture(t)
	if _, err := f.store.CreateBook(&database.Book{Title: "Batman: Year One", FilePath: f.slotShare}); err != nil {
		t.Fatal(err)
	}
	guard := undecidableOwn(f)
	removed := failRemovals(t, func(string) error { return nil })
	v, _ := f.store.GetBookVersion(f.trashed.ID)

	err := PurgeVersion(f.store, v, guard)

	var served *BookServesFilesError
	if !errors.As(err, &served) || errors.Is(err, ErrPurgeCannotTell) {
		t.Fatalf("PurgeVersion = %v, want a *BookServesFilesError and not a CannotTellError", err)
	}
	if len(*removed) != 0 {
		t.Errorf("removals attempted: %v, want none", *removed)
	}
	requireTrashed(t, f)
	got, _ := f.store.GetBookVersion(f.trashed.ID)
	if len(got.PurgeUnlinked) != 0 || got.PurgeUndecidedNights != 0 {
		t.Errorf("PurgeUnlinked=%v PurgeUndecidedNights=%d, want the version untouched", got.PurgeUnlinked, got.PurgeUndecidedNights)
	}
	for _, p := range []string{f.ownOnly, f.slotShare, f.rowShared, f.retOnly, f.unrowed} {
		requireOnDisk(t, p)
	}
}

// A purge that is already under way (purging) cannot go back to the trash: a
// path a live book has come to serve since is left on disk and recorded as kept,
// and the purge finishes with the rest instead of wedging on a
// BookServesFilesError it can never get out of.
func TestPurgeVersion_APathServedSinceThePurgeBeganIsKeptAndThePurgeFinishes(t *testing.T) {
	f := newSharedPathFixture(t)
	flaky, clear := f.flakyRow(t)
	v, _ := f.store.GetBookVersion(f.trashed.ID)
	if err := PurgeVersion(f.store, v, testGuard); err == nil {
		t.Fatal("first attempt succeeded although one removal failed")
	}
	requirePurging(t, f)
	clear()
	if _, err := f.store.CreateBook(&database.Book{Title: "Batman: Hush", FilePath: flaky}); err != nil {
		t.Fatal(err)
	}

	v, _ = f.store.GetBookVersion(f.trashed.ID)
	if err := PurgeVersion(f.store, v, testGuard); err != nil {
		t.Fatalf("retry: %v", err)
	}

	got, _ := f.store.GetBookVersion(f.trashed.ID)
	if got.Status != database.BookVersionStatusInactivePurged || !keptPath(got, flaky) {
		t.Errorf("status=%s purge_kept=%v, want inactive_purged with %s kept", got.Status, got.PurgeKept, flaky)
	}
	requireOnDisk(t, flaky)
}

// The nightly cleanup files a served version under Kept (with the reason on
// each path), not under Failed, CannotTell or Purged.
func TestCleanup_AServedVersionIsListedUnderKeptAndNothingElse(t *testing.T) {
	f := newSharedPathFixture(t)
	if _, err := f.store.CreateBook(&database.Book{Title: "Batman: Year One", FilePath: f.unrowed}); err != nil {
		t.Fatal(err)
	}

	res, err := cleanupTrashedVersionsAt(f.store, time.Now(), testGuard, true)

	if err != nil || res.Purged != 0 || res.Failed != 0 || len(res.CannotTell) != 0 || len(res.KeptFiles) != 0 {
		t.Fatalf("res=%+v err=%v, want only Kept filled", res, err)
	}
	if len(res.Kept) != 1 || res.Kept[0].VersionID != f.trashed.ID || len(res.Kept[0].Files) != 1 ||
		res.Kept[0].Files[0].Path != f.unrowed || res.Kept[0].Files[0].Reason != KeptReason {
		t.Fatalf("Kept = %+v, want %s with the served path and the reason", res.Kept, f.trashed.ID)
	}
	requireTrashed(t, f)
}

// dayClock moves purgeClock one calendar day at a time.
type dayClock struct{ t time.Time }

func (d *dayClock) next() { d.t = d.t.AddDate(0, 0, 1) }

// useDay installs a purgeClock that reads d, starting one day before t so the
// first next() lands on t. Tests using it must not run in parallel.
func useDay(t *testing.T, start time.Time) *dayClock {
	t.Helper()
	d := &dayClock{t: start.AddDate(0, 0, -1)}
	old := purgeClock
	purgeClock = func() time.Time { return d.t }
	t.Cleanup(func() { purgeClock = old })
	return d
}

// A Purge now click is a purge attempt, but not another night: two stuck attempts
// on one calendar day count once, and the next day's counts again.
func TestSettleStuck_CountsAtMostOncePerCalendarDay(t *testing.T) {
	f := newSharedPathFixture(t)
	setStatus(t, f, database.BookVersionStatusPurging)
	guard := undecidableOwn(f)
	day := useDay(t, time.Date(2026, 10, 1, 3, 0, 0, 0, time.Local))
	attempt := func() *database.BookVersion {
		t.Helper()
		v, _ := f.store.GetBookVersion(f.trashed.ID)
		if err := PurgeVersion(f.store, v, guard); !errors.Is(err, ErrPurgeCannotTell) {
			t.Fatalf("PurgeVersion = %v, want ErrPurgeCannotTell", err)
		}
		got, _ := f.store.GetBookVersion(f.trashed.ID)
		return got
	}

	day.next()
	if got := attempt(); got.PurgeUndecidedNights != 1 {
		t.Fatalf("first attempt of day 1: nights = %d, want 1", got.PurgeUndecidedNights)
	}
	day.t = day.t.Add(5 * time.Hour) // a Purge now click later the same day
	got := attempt()
	if got.PurgeUndecidedNights != 1 || got.PurgeUndecidedNightOn != "2026-10-01" {
		t.Fatalf("second attempt of day 1: nights=%d on=%q, want 1 and 2026-10-01", got.PurgeUndecidedNights, got.PurgeUndecidedNightOn)
	}
	day.t = day.t.Add(-5 * time.Hour)
	day.next()
	if got := attempt(); got.PurgeUndecidedNights != 2 || got.PurgeUndecidedNightOn != "2026-10-02" {
		t.Fatalf("day 2: nights=%d on=%q, want 2 and 2026-10-02", got.PurgeUndecidedNights, got.PurgeUndecidedNightOn)
	}
	requirePurging(t, f)
}
