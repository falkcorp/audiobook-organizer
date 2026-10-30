// file: internal/versions/kept_sweep_modes_test.go
// version: 1.0.1
// guid: 3e8a5c17-92d4-4b06-8f1a-6c0d7b2e9a54
// last-edited: 2026-10-29

package versions

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// The tests here are the two things the owner asked for on 2026-10-29: the sweep
// ships report-only (it decides everything and changes nothing until
// kept_sweep_apply_enabled is switched on), and a note it cannot settle escalates
// to purge_failed after PurgeUndecidedEscalateAfter nights instead of warning for
// ever.

func (f *keptFixture) stored(t *testing.T) *database.BookVersion {
	t.Helper()
	cur, err := f.store.GetBookVersion(f.ver.ID)
	if err != nil || cur == nil {
		t.Fatalf("reread version: %v (nil=%v)", err, cur == nil)
	}
	return cur
}

// noRemovals makes any unlink a test failure: the report-only guarantee.
func noRemovals(t *testing.T) {
	t.Helper()
	orig := removeFile
	removeFile = func(p string) error {
		t.Errorf("report-only run tried to unlink %s", p)
		return errors.New("report-only run must never unlink")
	}
	t.Cleanup(func() { removeFile = orig })
}

// ---- report-only ----

// Report-only decides everything and changes nothing: no unlink, no note dropped,
// no write to the version. It lists, per version, the files Apply would remove and
// the notes it would drop, each with the reason the file had been kept; and
// switching Apply on then does exactly that.
func TestSweepKeptFiles_ReportOnlyChangesNothingAndListsWhatApplyWouldDo(t *testing.T) {
	f := newKeptFixture(t)
	free := f.addKept(t, "it is the file path of book(s) [hush] (live or in the trash)")
	gone := f.addKept(t, "book_file x of book y still names it")
	owned := f.addKept(t, "kept once")
	f.rowOwner(t, owned)
	if err := os.Remove(gone); err != nil { // already gone, the slot still holds neighbours
		t.Fatal(err)
	}
	noRemovals(t)
	before := f.stored(t)

	res := sweepReport(t, f.store, testGuard)

	if !res.ReportOnly {
		t.Error("ReportOnly = false for a run without Apply")
	}
	if res.Removed != 0 || res.Gone != 0 {
		t.Errorf("removed=%d gone=%d, want 0 and 0 in a report-only run (%+v)", res.Removed, res.Gone, res)
	}
	if res.WouldRemove != 1 || res.WouldDrop != 1 || res.StillKept != 1 || res.KeptOnError != 0 {
		t.Fatalf("would_remove=%d would_drop=%d still_kept=%d kept_on_error=%d, want 1 1 1 0 (%+v)",
			res.WouldRemove, res.WouldDrop, res.StillKept, res.KeptOnError, res)
	}
	if len(res.WouldRemoveFiles) != 1 || res.WouldRemoveFiles[0].VersionID != f.ver.ID || res.WouldRemoveFiles[0].BookID != f.book.ID ||
		len(res.WouldRemoveFiles[0].Files) != 1 || res.WouldRemoveFiles[0].Files[0].Path != free ||
		res.WouldRemoveFiles[0].Files[0].Reason != "it is the file path of book(s) [hush] (live or in the trash)" {
		t.Errorf("WouldRemoveFiles = %+v, want %s of version %s / book %s with the reason it had been kept", res.WouldRemoveFiles, free, f.ver.ID, f.book.ID)
	}
	if len(res.WouldDropFiles) != 1 || res.WouldDropFiles[0].Files[0].Path != gone || res.WouldDropFiles[0].Files[0].Reason != "book_file x of book y still names it" {
		t.Errorf("WouldDropFiles = %+v, want %s with the reason it had been kept", res.WouldDropFiles, gone)
	}
	requireOnDisk(t, free)
	requireOnDisk(t, owned)
	after := f.stored(t)
	if after.Version != before.Version || !slices.Equal(f.kept(t), []string{free, gone, owned}) {
		t.Errorf("report-only changed the version: revision %d -> %d, PurgeKept = %v", before.Version, after.Version, f.kept(t))
	}

	// Apply does what the report said, and nothing else.
	removeFile = os.Remove // Apply is allowed to unlink (the hook restores the real one after the test)
	applied := sweep(t, f.store, testGuard)
	requireCounts(t, applied, 1, 1, 1, 0)
	requireGone(t, free)
	requireOnDisk(t, owned)
	if got := f.kept(t); !slices.Equal(got, []string{owned}) {
		t.Errorf("PurgeKept after Apply = %v, want only %s", got, owned)
	}
}

// The nightly cleanup passes the switch down: off, the file is still there after a
// whole cleanup run; on, it goes.
func TestCleanup_KeptSweepReportOnlyUnlessSwitchedOn(t *testing.T) {
	f := newKeptFixture(t)
	p := f.addKept(t, "kept once")

	noRemovals(t)
	res, err := cleanupTrashedVersionsAt(f.store, time.Now(), testGuard, false)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if !res.KeptSweep.ReportOnly || res.KeptSweep.WouldRemove != 1 || res.KeptSweep.Removed != 0 {
		t.Fatalf("KeptSweep = %+v, want a report-only run that would remove one file", res.KeptSweep)
	}
	requireOnDisk(t, p)
	if got := f.kept(t); !slices.Equal(got, []string{p}) {
		t.Fatalf("PurgeKept = %v, want the note untouched", got)
	}
}

// A report-only run takes no merge lock when it has nothing to count, so it never
// queues behind a combine: with the lock held for the whole wait it still reports.
func TestSweepKeptFiles_ReportOnlyDoesNotWaitForTheMergeLock(t *testing.T) {
	f := newKeptFixture(t)
	f.addKept(t, "kept once")
	oldWait := BackgroundMergeLockWait
	BackgroundMergeLockWait = 30 * time.Second
	t.Cleanup(func() { BackgroundMergeLockWait = oldWait })
	merge.LockMergeRMW()
	defer merge.UnlockMergeRMW()

	started := time.Now()
	res := sweepReport(t, f.store, testGuard)
	if res.WouldRemove != 1 {
		t.Fatalf("would_remove = %d, want 1 (%+v)", res.WouldRemove, res)
	}
	if time.Since(started) > 5*time.Second {
		t.Errorf("the report-only sweep waited %s for the merge lock", time.Since(started))
	}
}

// ---- escalation ----

// flakyOwnerStore fails the owner lookup of one path while down is set.
type flakyOwnerStore struct {
	*database.PebbleStore
	path string
	down bool
}

func (s *flakyOwnerStore) BookFilesAtPath(p string) ([]database.BookFile, error) {
	if s.down && p == s.path {
		return nil, errors.New("owner lookup down")
	}
	return s.PebbleStore.BookFilesAtPath(p)
}

// A note that cannot be settled three nights in a row (an owner lookup that
// errors) moves the version to purge_failed, with the note in PurgeFailed and the
// reason it could not be settled. Nights one and two only count. Against a real
// store, so the store's own refusal of the transition is exercised too. Night four
// does not see the version at all, and the cleanup lists it under NeedsAttention.
func TestSweepKeptFiles_EscalatesAfterThreeNightsOfARealError(t *testing.T) {
	f := newKeptFixture(t)
	p := f.addKept(t, "kept once")
	other := f.addKept(t, "kept once")
	f.rowOwner(t, other) // still owned: stays a kept note
	s := &flakyOwnerStore{PebbleStore: f.store, path: p, down: true}

	for night := 1; night <= PurgeUndecidedEscalateAfter-1; night++ {
		res := sweep(t, s, testGuard)
		requireCounts(t, res, 0, 0, 1, 1)
		if len(res.Escalated) != 0 {
			t.Fatalf("night %d escalated: %+v", night, res.Escalated)
		}
		cur := f.stored(t)
		if cur.Status != database.BookVersionStatusInactivePurged || cur.PurgeUndecidedNights != night {
			t.Fatalf("night %d: status=%s nights=%d, want inactive_purged and %d", night, cur.Status, cur.PurgeUndecidedNights, night)
		}
	}

	res := sweep(t, s, testGuard)
	if len(res.Escalated) != 1 || res.Escalated[0].VersionID != f.ver.ID || res.Escalated[0].Files[0].Path != p {
		t.Fatalf("Escalated = %+v, want version %s with %s", res.Escalated, f.ver.ID, p)
	}
	cur := f.stored(t)
	if cur.Status != database.BookVersionStatusPurgeFailed || cur.PurgeUndecidedNights != 0 || cur.PurgeFailedAt == nil {
		t.Fatalf("stored = status %s nights %d failed_at %v, want purge_failed, 0, set", cur.Status, cur.PurgeUndecidedNights, cur.PurgeFailedAt)
	}
	if len(cur.PurgeFailed) != 1 || cur.PurgeFailed[0].Path != p || !strings.Contains(cur.PurgeFailed[0].Reason, "owner lookup down") {
		t.Errorf("PurgeFailed = %+v, want %s with the lookup error as the reason", cur.PurgeFailed, p)
	}
	if got := f.kept(t); !slices.Equal(got, []string{other}) {
		t.Errorf("PurgeKept = %v, want only the still-owned %s", got, other)
	}
	requireOnDisk(t, p) // escalating deletes nothing
	requireOnDisk(t, other)

	// Night four: the unsettled note is the owner's now (PurgeFailed), so it is not
	// warned about again, and the version is not escalated twice. The sweep still
	// looks at the notes left in PurgeKept (the still-owned file) and finds it kept.
	night4 := sweep(t, s, testGuard)
	requireCounts(t, night4, 0, 0, 1, 0)
	if night4.Versions != 1 || len(night4.Escalated) != 0 {
		t.Errorf("night 4 looked at %d version(s) and escalated %d, want 1 and 0", night4.Versions, len(night4.Escalated))
	}
	if again := f.stored(t); again.Status != database.BookVersionStatusPurgeFailed || len(again.PurgeFailed) != 1 || again.PurgeUndecidedNights != 0 {
		t.Errorf("after night 4: status=%s purge_failed=%v nights=%d, want it unchanged", again.Status, again.PurgeFailed, again.PurgeUndecidedNights)
	}
	nextNight(t)
	clean, err := cleanupTrashedVersionsAt(s, time.Now(), testGuard, true)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if len(clean.NeedsAttention) != 1 || clean.NeedsAttention[0].VersionID != f.ver.ID || clean.KeptSweep.KeptOnError != 0 {
		t.Errorf("NeedsAttention = %+v, kept on error = %d, want the escalated version listed and no kept-on-error", clean.NeedsAttention, clean.KeptSweep.KeptOnError)
	}
}

// The count is of nights IN A ROW: a clean night resets it.
func TestSweepKeptFiles_ACleanNightResetsTheCount(t *testing.T) {
	f := newKeptFixture(t)
	p := f.addKept(t, "kept once")
	s := &flakyOwnerStore{PebbleStore: f.store, path: p, down: true}
	noRemovals(t)

	sweep(t, s, testGuard)
	sweep(t, s, testGuard)
	if n := f.stored(t).PurgeUndecidedNights; n != 2 {
		t.Fatalf("nights = %d after two bad nights, want 2", n)
	}
	// The lookup recovers; the file is owned again by then, so nothing is removed.
	s.down = false
	f.rowOwner(t, p)
	sweep(t, s, testGuard)
	if cur := f.stored(t); cur.PurgeUndecidedNights != 0 || cur.Status != database.BookVersionStatusInactivePurged {
		t.Fatalf("after a clean night: status=%s nights=%d, want inactive_purged and 0", cur.Status, cur.PurgeUndecidedNights)
	}
	// Two more bad nights are not three in a row.
	s.down = true
	sweep(t, s, testGuard)
	res := sweep(t, s, testGuard)
	if len(res.Escalated) != 0 || f.stored(t).Status != database.BookVersionStatusInactivePurged {
		t.Fatalf("escalated after 2 bad nights following a clean one: %+v", res.Escalated)
	}
}

// A night that removes something is progress and resets the count, as an attempt
// that unlinks something resets the purge's.
func TestSweepKeptFiles_ProgressResetsTheCount(t *testing.T) {
	f := newKeptFixture(t)
	bad := f.addKept(t, "kept once")
	s := &flakyOwnerStore{PebbleStore: f.store, path: bad, down: true}

	sweep(t, s, testGuard)
	sweep(t, s, testGuard)
	free := f.addKept(t, "kept once") // appears now, and goes tonight
	res := sweep(t, s, testGuard)
	requireCounts(t, res, 1, 0, 0, 1)
	requireGone(t, free)
	if cur := f.stored(t); cur.PurgeUndecidedNights != 0 || cur.Status != database.BookVersionStatusInactivePurged {
		t.Fatalf("after progress: status=%s nights=%d, want inactive_purged and 0", cur.Status, cur.PurgeUndecidedNights)
	}
}

// A real error escalates in a report-only run too, and the run still unlinks
// nothing. (The count is the one thing a report-only run writes.)
func TestSweepKeptFiles_ReportOnlyEscalatesARealError(t *testing.T) {
	f := newKeptFixture(t)
	p := f.addKept(t, "kept once")
	free := f.addKept(t, "kept once")
	s := &flakyOwnerStore{PebbleStore: f.store, path: p, down: true}
	noRemovals(t)

	var res KeptSweepResult
	for night := 0; night < PurgeUndecidedEscalateAfter; night++ {
		res = sweepReport(t, s, testGuard)
	}
	if len(res.Escalated) != 1 {
		t.Fatalf("Escalated = %+v, want the version after %d report-only nights", res.Escalated, PurgeUndecidedEscalateAfter)
	}
	cur := f.stored(t)
	if cur.Status != database.BookVersionStatusPurgeFailed || len(cur.PurgeFailed) != 1 || cur.PurgeFailed[0].Path != p {
		t.Fatalf("stored = %s %+v, want purge_failed with %s", cur.Status, cur.PurgeFailed, p)
	}
	requireOnDisk(t, p)
	requireOnDisk(t, free)
	// The note it would have removed is still on the escalated version.
	if got := f.kept(t); !slices.Equal(got, []string{free}) {
		t.Errorf("PurgeKept = %v, want %s still noted", got, free)
	}
}

// A decision that would only have removed something is not an error, however many
// nights it goes on: report-only never escalates it. That includes a would-remove
// held back because the book serves nothing on disk (nothing was being removed, so
// nothing failed).
func TestSweepKeptFiles_ReportOnlyNeverEscalatesAWouldRemove(t *testing.T) {
	t.Run("plain would-remove", func(t *testing.T) {
		f := newKeptFixture(t)
		p := f.addKept(t, "kept once")
		noRemovals(t)
		for night := 0; night < PurgeUndecidedEscalateAfter+2; night++ {
			res := sweepReport(t, f.store, testGuard)
			if res.WouldRemove != 1 || len(res.Escalated) != 0 {
				t.Fatalf("night %d: %+v", night, res)
			}
		}
		if cur := f.stored(t); cur.Status != database.BookVersionStatusInactivePurged || cur.PurgeUndecidedNights != 0 {
			t.Fatalf("stored = %s nights %d, want inactive_purged and 0", cur.Status, cur.PurgeUndecidedNights)
		}
		requireOnDisk(t, p)
	})
	t.Run("would-remove held back by a book that serves nothing", func(t *testing.T) {
		f := newKeptFixture(t)
		p := f.addKept(t, "kept once")
		if err := os.Remove(filepath.Join(f.bookDir, "year-one.m4b")); err != nil {
			t.Fatal(err)
		}
		noRemovals(t)
		for night := 0; night < PurgeUndecidedEscalateAfter+2; night++ {
			sweepReport(t, f.store, testGuard)
		}
		if cur := f.stored(t); cur.Status != database.BookVersionStatusInactivePurged || cur.PurgeUndecidedNights != 0 {
			t.Fatalf("stored = %s nights %d, want inactive_purged and 0", cur.Status, cur.PurgeUndecidedNights)
		}
		requireOnDisk(t, p)
	})
}

// A held-back removal because the book serves nothing on disk never escalates,
// in an applying run either: it is the purge's ErrPurgeNothingServed (the book's
// path is stale or lost, and only fixing the book changes that), which the purge
// does not count, and an escalated version could be retried by Purge now, which
// would have to delete what the sweep refused to. The file stays, every night,
// reported as kept on error.
func TestSweepKeptFiles_ApplyNeverEscalatesABookThatServesNothing(t *testing.T) {
	f := newKeptFixture(t)
	p := f.addKept(t, "kept once")
	if err := os.Remove(filepath.Join(f.bookDir, "year-one.m4b")); err != nil {
		t.Fatal(err)
	}
	for night := 0; night < PurgeUndecidedEscalateAfter+3; night++ {
		res := sweep(t, f.store, testGuard)
		requireCounts(t, res, 0, 0, 0, 1)
		if len(res.Escalated) != 0 {
			t.Fatalf("night %d escalated a book that serves nothing: %+v", night+1, res.Escalated)
		}
	}
	if cur := f.stored(t); cur.Status != database.BookVersionStatusInactivePurged || cur.PurgeUndecidedNights != 0 {
		t.Fatalf("stored = %s nights %d, want inactive_purged and 0", cur.Status, cur.PurgeUndecidedNights)
	}
	requireOnDisk(t, p)
}

// A failed unlink is a real "cannot settle" and counts.
func TestSweepKeptFiles_FailedUnlinkEscalates(t *testing.T) {
	f := newKeptFixture(t)
	p := f.addKept(t, "kept once")
	failRemovals(t, func(string) error { return syscall.EROFS })

	var res KeptSweepResult
	for night := 0; night < PurgeUndecidedEscalateAfter; night++ {
		res = sweep(t, f.store, testGuard)
	}
	if len(res.Escalated) != 1 {
		t.Fatalf("Escalated = %+v, want the version", res.Escalated)
	}
	cur := f.stored(t)
	if cur.Status != database.BookVersionStatusPurgeFailed || len(cur.PurgeFailed) != 1 || !strings.Contains(cur.PurgeFailed[0].Reason, "remove failed") {
		t.Fatalf("stored = %s %+v, want purge_failed with the unlink error", cur.Status, cur.PurgeFailed)
	}
	requireOnDisk(t, p)
}

// Nothing environmental counts: a library that is not ready, nor a merge lock that
// stays busy, says nothing about the note. Many nights of either leave the version
// swept and unescalated, with its count at zero.
func TestSweepKeptFiles_EnvironmentalFailuresDoNotCount(t *testing.T) {
	t.Run("library not ready", func(t *testing.T) {
		f := newKeptFixture(t)
		p := f.addKept(t, "kept once")
		p2 := f.addKept(t, "kept once")
		s := &flakyOwnerStore{PebbleStore: f.store, path: p2, down: true} // a real error beside it
		guard := &fsGuard{rootErr: errors.New("library root is not mounted")}
		for night := 0; night < PurgeUndecidedEscalateAfter+2; night++ {
			sweep(t, s, guard)
		}
		if cur := f.stored(t); cur.Status != database.BookVersionStatusInactivePurged || cur.PurgeUndecidedNights != 0 {
			t.Fatalf("stored = %s nights %d, want inactive_purged and 0", cur.Status, cur.PurgeUndecidedNights)
		}
		requireOnDisk(t, p)
	})
	t.Run("merge lock busy", func(t *testing.T) {
		f := newKeptFixture(t)
		p := f.addKept(t, "kept once")
		oldWait := BackgroundMergeLockWait
		BackgroundMergeLockWait = 20 * time.Millisecond
		t.Cleanup(func() { BackgroundMergeLockWait = oldWait })
		merge.LockMergeRMW()
		defer merge.UnlockMergeRMW()
		for night := 0; night < PurgeUndecidedEscalateAfter+2; night++ {
			res := sweep(t, f.store, testGuard)
			if res.KeptOnError != 1 {
				t.Fatalf("night %d: kept_on_error = %d, want 1", night, res.KeptOnError)
			}
		}
		if cur := f.stored(t); cur.Status != database.BookVersionStatusInactivePurged || cur.PurgeUndecidedNights != 0 {
			t.Fatalf("stored = %s nights %d, want inactive_purged and 0", cur.Status, cur.PurgeUndecidedNights)
		}
		requireOnDisk(t, p)
	})
}

// The cleanup reports a version it escalated on the night it did (NeedsAttention
// was computed before the sweep, so it could not).
func TestCleanup_ReportsAVersionTheSweepEscalated(t *testing.T) {
	f := newKeptFixture(t)
	p := f.addKept(t, "kept once")
	s := &flakyOwnerStore{PebbleStore: f.store, path: p, down: true}

	var res CleanupResult
	for night := 0; night < PurgeUndecidedEscalateAfter; night++ {
		var err error
		nextNight(t)
		if res, err = cleanupTrashedVersionsAt(s, time.Now(), testGuard, true); err != nil {
			t.Fatalf("cleanup: %v", err)
		}
	}
	if len(res.KeptSweep.Escalated) != 1 || res.KeptSweep.Escalated[0].VersionID != f.ver.ID {
		t.Fatalf("Escalated = %+v, want the version on the night it escalated", res.KeptSweep.Escalated)
	}
}

// "Purge now" on an escalated version retries it as the purge always does (the
// slot's files and the version's rows, each decided again): a file nothing owns now
// is removed and the version ends purged, and a file still owned is noted again.
func TestPurgeVersion_RetriesTheNotesOfAnEscalatedVersion(t *testing.T) {
	f := newKeptFixture(t)
	retry := f.addKept(t, "kept once")
	owned := f.addKept(t, "kept once")
	f.rowOwner(t, owned)
	s := &flakyOwnerStore{PebbleStore: f.store, path: retry, down: true}
	for night := 0; night < PurgeUndecidedEscalateAfter; night++ {
		sweep(t, s, testGuard)
	}
	esc := f.stored(t)
	if esc.Status != database.BookVersionStatusPurgeFailed {
		t.Fatalf("status = %s, want purge_failed", esc.Status)
	}

	// The owner fixes the cause and clicks Purge now (the lookup works again).
	s.down = false
	if err := PurgeVersion(s, esc, testGuard); err != nil {
		t.Fatalf("PurgeVersion on the escalated version: %v", err)
	}
	requireGone(t, retry)
	requireOnDisk(t, owned)
	cur := f.stored(t)
	if cur.Status != database.BookVersionStatusInactivePurged || len(cur.PurgeFailed) != 0 {
		t.Fatalf("stored = %s purge_failed=%v, want inactive_purged with the failure record cleared", cur.Status, cur.PurgeFailed)
	}
	if got := f.kept(t); !slices.Equal(got, []string{owned}) {
		t.Errorf("PurgeKept = %v, want the still-owned %s noted again, not dropped", got, owned)
	}
}

// And the owner can hard-delete an escalated version, as any purge_failed one.
func TestEscalatedVersionCanBeHardDeleted(t *testing.T) {
	f := newKeptFixture(t)
	p := f.addKept(t, "kept once")
	s := &flakyOwnerStore{PebbleStore: f.store, path: p, down: true}
	for night := 0; night < PurgeUndecidedEscalateAfter; night++ {
		sweep(t, s, testGuard)
	}
	if err := f.store.DeleteBookVersion(f.ver.ID); err != nil {
		t.Fatalf("hard delete of the escalated version: %v", err)
	}
	requireOnDisk(t, p) // the record goes, the file named in purge_failed stays
}
