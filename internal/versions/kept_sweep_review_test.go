// file: internal/versions/kept_sweep_review_test.go
// version: 1.1.0
// guid: f96f6820-9489-4ec7-b233-d61df1dfbefd
// last-edited: 2026-10-29

package versions

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The review of the kept-file sweep (PR 4255): the sweep and the purge that
// retries an escalated version must agree, a file of a restorable book is never
// the sweep's to remove, no note is erased unasked, purge_failed versions stay
// swept, and a night is a calendar day.

// escalate runs the sweep three nights against s (down for the one path) and
// returns the stored, now purge_failed, version.
func escalate(t *testing.T, f *keptFixture, s KeptSweepStore) *database.BookVersion {
	t.Helper()
	for night := 0; night < PurgeUndecidedEscalateAfter; night++ {
		sweep(t, s, testGuard)
	}
	cur := f.stored(t)
	if cur.Status != database.BookVersionStatusPurgeFailed {
		t.Fatalf("status = %s after %d nights, want purge_failed", cur.Status, PurgeUndecidedEscalateAfter)
	}
	return cur
}

// ---- 1. the purge and the sweep agree on "the book serves nothing" ----

// A book that serves nothing on disk never escalates the sweep (the sweep test
// beside this one) and, now, Purge now on a version that was escalated for some
// other reason does not delete files while the book serves nothing either: the
// purge's served-file rule held only for a trashed version, so a retry of a
// purge_failed one deleted what the sweep had refused to.
func TestPurgeVersion_PurgeFailedVersionIsNotDeletedWhileTheBookServesNothing(t *testing.T) {
	f := newKeptFixture(t)
	p := f.addKept(t, "kept once")
	s := &flakyOwnerStore{PebbleStore: f.store, path: p, down: true}
	escalate(t, f, s)

	s.down = false
	if err := os.Remove(filepath.Join(f.bookDir, "year-one.m4b")); err != nil {
		t.Fatal(err)
	}
	err := PurgeVersion(s, f.stored(t), testGuard)
	if !errors.Is(err, ErrPurgeNothingServed) {
		t.Fatalf("PurgeVersion = %v, want ErrPurgeNothingServed", err)
	}
	requireOnDisk(t, p)
	if cur := f.stored(t); cur.Status != database.BookVersionStatusPurgeFailed || len(cur.PurgeFailed) != 1 {
		t.Errorf("stored = %s purge_failed=%v, want it left as it was", cur.Status, cur.PurgeFailed)
	}

	// The book's file is back: the retry goes ahead.
	writeTestFile(t, filepath.Join(f.bookDir, "year-one.m4b"), "audio")
	if err := PurgeVersion(s, f.stored(t), testGuard); err != nil {
		t.Fatalf("PurgeVersion once the book serves a file: %v", err)
	}
	requireGone(t, p)
}

// The same for a purge that was claimed and did not finish.
func TestPurgeVersion_PurgingRetryIsNotDeletedWhileTheBookServesNothing(t *testing.T) {
	f := newKeptFixture(t)
	trashedAt := time.Now().Add(-30 * day)
	ver, err := f.store.CreateBookVersion(&database.BookVersion{
		BookID: f.book.ID, Status: database.BookVersionStatusTrash, Format: "m4b", Source: "imported", TrashedAt: &trashedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(VersionSlotDir(f.bookDir, ver.ID), "part1.m4b")
	writeTestFile(t, p, "audio")
	ver.Status = database.BookVersionStatusPurging
	if err := f.store.UpdateBookVersion(ver); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(f.bookDir, "year-one.m4b")); err != nil {
		t.Fatal(err)
	}

	cur, _ := f.store.GetBookVersion(ver.ID)
	if err := PurgeVersion(f.store, cur, testGuard); !errors.Is(err, ErrPurgeNothingServed) {
		t.Fatalf("PurgeVersion = %v, want ErrPurgeNothingServed", err)
	}
	requireOnDisk(t, p)
}

// ---- 2. the sweep deletes only inside the version's own slot ----

// A noted file outside the version's own .versions/{id}/ slot is never removed,
// whatever the state of the own book and of the owner lookups: the notes cannot
// prove nothing wants the file. Each case is a way the sweep used to (or could
// have) read "no owner" for a file somebody wants: a book trashed since, a book
// hard-deleted since, a trashed book whose FilePath is the parent folder, and a
// path a re-import reused before the scan wrote its row. In both modes the file
// stays, its note stays, the report lists it as kept outside the slot, and the
// version is not escalated (holding a file is not a failure).
func TestSweepKeptFiles_NeverRemovesAFileOutsideTheSlot(t *testing.T) {
	trash := func(t *testing.T, f *keptFixture, filePath string) {
		t.Helper()
		if _, err := f.store.ModifyBook(f.book.ID, func(b *database.Book) error {
			yes := true
			b.MarkedForDeletion = &yes
			if filePath != "" {
				b.FilePath = filePath
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name  string
		setup func(t *testing.T, f *keptFixture)
	}{
		{"own book trashed", func(t *testing.T, f *keptFixture) { trash(t, f, "") }},
		{"own book hard-deleted", func(t *testing.T, f *keptFixture) {
			if err := f.store.DeleteBook(f.book.ID); err != nil {
				t.Fatal(err)
			}
		}},
		{"trashed own book whose FilePath is the parent folder", func(t *testing.T, f *keptFixture) { trash(t, f, f.bookDir) }},
		{"path reused by a re-import before the scan wrote its row", func(t *testing.T, f *keptFixture) {
			if err := f.store.DeleteBook(f.book.ID); err != nil {
				t.Fatal(err)
			}
			// A new import put different audio at the noted path; no row names it yet.
			writeTestFile(t, filepath.Join(f.bookDir, "year-one-part2.m4b"), "the re-imported book's audio")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newKeptFixture(t)
			audio := filepath.Join(f.bookDir, "year-one.m4b")
			extra := filepath.Join(f.bookDir, "year-one-part2.m4b")
			writeTestFile(t, extra, "audio")
			f.note(t, audio, KeptReason)
			f.note(t, extra, KeptReason)
			tc.setup(t, f)

			noRemovals(t)
			rep := sweepReport(t, f.store, testGuard)
			requireCounts(t, rep, 0, 0, 2, 0)
			if rep.WouldRemove != 0 {
				t.Errorf("report-only would remove %d file(s) outside the slot: %+v", rep.WouldRemove, rep.WouldRemoveFiles)
			}
			if rep.OutsideSlot != 2 || len(rep.OutsideSlotFiles) != 1 || len(rep.OutsideSlotFiles[0].Files) != 2 || rep.OutsideSlotFiles[0].Files[0].Reason != OutsideSlotNeverAutoRemovedReason {
				t.Errorf("report-only OutsideSlot = %d files = %+v, want both files listed with the outside-slot reason", rep.OutsideSlot, rep.OutsideSlotFiles)
			}
			res := sweep(t, f.store, testGuard)
			requireCounts(t, res, 0, 0, 2, 0)
			if res.OutsideSlot != 2 || len(res.OutsideSlotFiles) != 1 {
				t.Errorf("apply OutsideSlot = %d files = %+v, want both files listed", res.OutsideSlot, res.OutsideSlotFiles)
			}
			requireOnDisk(t, audio)
			requireOnDisk(t, extra)
			if got := f.kept(t); len(got) != 2 {
				t.Fatalf("PurgeKept = %v, want both notes kept", got)
			}
			for _, sf := range res.Escalated {
				t.Errorf("escalated %+v: holding a file outside the slot is not a failure", sf)
			}
			if cur := f.stored(t); cur.PurgeUndecidedNights != 0 {
				t.Errorf("nights = %d, want 0", cur.PurgeUndecidedNights)
			}
		})
	}
}

// The other half of the rule: a noted file INSIDE the slot whose owners are gone
// is still removed, whether or not the own book exists.
func TestSweepKeptFiles_StillRemovesAFileInsideTheSlotWhoseOwnerIsGone(t *testing.T) {
	for _, ownBook := range []string{"trashed", "deleted"} {
		t.Run(ownBook, func(t *testing.T) {
			f := newKeptFixture(t)
			inside := f.addKept(t, "kept once")
			switch ownBook {
			case "deleted":
				if err := f.store.DeleteBook(f.book.ID); err != nil {
					t.Fatal(err)
				}
			case "trashed":
				if _, err := f.store.ModifyBook(f.book.ID, func(b *database.Book) error {
					yes := true
					b.MarkedForDeletion = &yes
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			rep := sweepReport(t, f.store, testGuard)
			if rep.WouldRemove != 1 || rep.OutsideSlot != 0 {
				t.Fatalf("report-only: would remove %d, outside slot %d, want 1 and 0 (%+v)", rep.WouldRemove, rep.OutsideSlot, rep)
			}
			requireOnDisk(t, inside)
			res := sweep(t, f.store, testGuard)
			requireCounts(t, res, 1, 0, 0, 0)
			requireGone(t, inside)
		})
	}
}

// ---- 3a. Purge now on an escalated version asks about every noted file ----

// escalatedWithOutsideNotes makes a version whose notes name files outside its
// slot, as the sweep leaves them: gone is a file whose owner lookup failed three
// nights (so it is in PurgeFailed), owned a file another book's row names (still
// in PurgeKept). ownBook says what happens to the version's own book first:
// "deleted" hard-deletes it, "trashed" trashes it.
func escalatedWithOutsideNotes(t *testing.T, ownBook string) (f *keptFixture, s *flakyOwnerStore, gone, owned string) {
	t.Helper()
	f = newKeptFixture(t)
	gone = filepath.Join(f.bookDir, "year-one-extra1.m4b")
	owned = filepath.Join(f.bookDir, "year-one-extra2.m4b")
	writeTestFile(t, gone, "audio")
	writeTestFile(t, owned, "audio")
	f.note(t, gone, KeptReason)
	f.note(t, owned, KeptReason)
	f.rowOwner(t, owned)
	switch ownBook {
	case "deleted":
		if err := f.store.DeleteBook(f.book.ID); err != nil {
			t.Fatalf("hard-delete the own book: %v", err)
		}
	case "trashed":
		if _, err := f.store.ModifyBook(f.book.ID, func(b *database.Book) error {
			yes := true
			b.MarkedForDeletion = &yes
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	s = &flakyOwnerStore{PebbleStore: f.store, path: gone, down: true}
	esc := escalate(t, f, s)
	if len(esc.PurgeFailed) != 1 || esc.PurgeFailed[0].Path != gone {
		t.Fatalf("PurgeFailed = %+v, want %s", esc.PurgeFailed, gone)
	}
	if got := f.kept(t); !slices.Equal(got, []string{owned}) {
		t.Fatalf("PurgeKept = %v, want %s", got, owned)
	}
	s.down = false
	return f, s, gone, owned
}

// The own book is gone and its rows with it: the slot holds nothing and no row
// names the noted files, so the retry found nothing to decide and erased both
// notes (finishPurge replaced PurgeKept, markPurged cleared PurgeFailed) with the
// files still on disk. Now it decides each, and neither is dropped unasked: both
// lie outside the version's slot, so a purge never removes them
// (OutsideSlotNeverAutoRemovedReason, even though nothing names the one), and
// both stay on disk with their notes.
func TestPurgeVersion_RetryDecidesEveryNotedFileAndDropsNone(t *testing.T) {
	f, s, free, owned := escalatedWithOutsideNotes(t, "deleted")

	if err := PurgeVersion(s, f.stored(t), testGuard); err != nil {
		t.Fatalf("PurgeVersion: %v", err)
	}
	requireOnDisk(t, free)
	requireOnDisk(t, owned)
	cur := f.stored(t)
	if cur.Status != database.BookVersionStatusInactivePurged || len(cur.PurgeFailed) != 0 {
		t.Fatalf("stored = %s purge_failed=%v, want inactive_purged with the failure record cleared", cur.Status, cur.PurgeFailed)
	}
	got := f.kept(t)
	slices.Sort(got)
	want := []string{free, owned}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("PurgeKept = %v, want both files noted again, not dropped: %v", got, want)
	}
	for _, n := range cur.PurgeKept {
		if n.Path == free && n.Reason != OutsideSlotNeverAutoRemovedReason {
			t.Errorf("note for %s has reason %q, want %q", free, n.Reason, OutsideSlotNeverAutoRemovedReason)
		}
	}
}

// A retry still removes a noted file that lies inside the version's own slot and
// that nothing owns: the rule is "outside the slot is never auto-removed", not
// "noted files are never removed". The own book is hard-deleted and the slot's
// folder cannot be located, so the file is found only through its note.
func TestPurgeVersion_RetryRemovesANotedFileInsideTheSlot(t *testing.T) {
	f := newKeptFixture(t)
	inside := f.addKept(t, "kept once")
	outside := filepath.Join(f.bookDir, "year-one-extra1.m4b")
	writeTestFile(t, outside, "audio")
	f.note(t, outside, KeptReason)
	if err := f.store.DeleteBook(f.book.ID); err != nil {
		t.Fatalf("hard-delete the own book: %v", err)
	}
	s := &flakyOwnerStore{PebbleStore: f.store, path: inside, down: true}
	escalate(t, f, s)
	s.down = false

	if err := PurgeVersion(s, f.stored(t), testGuard); err != nil {
		t.Fatalf("PurgeVersion: %v", err)
	}
	requireGone(t, inside)
	requireOnDisk(t, outside)
	if got := f.kept(t); !slices.Equal(got, []string{outside}) {
		t.Errorf("PurgeKept = %v, want only the outside file %s noted", got, outside)
	}
}

// A noted file that something owns again by the time of the retry is kept and
// noted, not deleted and not forgotten.
func TestPurgeVersion_RetryKeepsAndNotesAFileOwnedAgain(t *testing.T) {
	f, s, gone, owned := escalatedWithOutsideNotes(t, "deleted")
	f.rowOwner(t, gone)

	if err := PurgeVersion(s, f.stored(t), testGuard); err != nil {
		t.Fatalf("PurgeVersion: %v", err)
	}
	requireOnDisk(t, gone)
	requireOnDisk(t, owned)
	got := f.kept(t)
	slices.Sort(got)
	want := []string{gone, owned}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("PurgeKept = %v, want both files noted: %v", got, want)
	}
}

// A noted file that cannot be settled keeps the version and its notes where they
// are (purge_failed, PurgeFailed intact) rather than finishing the purge.
func TestPurgeVersion_RetryLeavesAnUnsettledNotedFileNoted(t *testing.T) {
	f, s, gone, _ := escalatedWithOutsideNotes(t, "deleted")
	s.down = true // the lookup is still failing

	err := PurgeVersion(s, f.stored(t), testGuard)
	if err == nil {
		t.Fatal("PurgeVersion succeeded with an owner lookup that errors")
	}
	requireOnDisk(t, gone)
	cur := f.stored(t)
	if cur.Status != database.BookVersionStatusPurgeFailed || len(cur.PurgeFailed) != 1 || cur.PurgeFailed[0].Path != gone {
		t.Errorf("stored = %s purge_failed=%+v, want purge_failed with %s still named", cur.Status, cur.PurgeFailed, gone)
	}
}

// The own book is trashed, not gone: the noted file may be its audio, and it lies
// outside the slot either way, so Purge now keeps it (OutsideSlotNeverAutoRemovedReason)
// and notes it, instead of removing it.
func TestPurgeVersion_RetryKeepsANotedFileOfATrashedOwnBook(t *testing.T) {
	f, s, gone, owned := escalatedWithOutsideNotes(t, "trashed")

	if err := PurgeVersion(s, f.stored(t), testGuard); err != nil {
		t.Fatalf("PurgeVersion: %v", err)
	}
	requireOnDisk(t, gone)
	requireOnDisk(t, owned)
	cur := f.stored(t)
	var reason string
	for _, n := range cur.PurgeKept {
		if n.Path == gone {
			reason = n.Reason
		}
	}
	if reason != OutsideSlotNeverAutoRemovedReason {
		t.Errorf("note for %s has reason %q, want %q (PurgeKept = %+v)", gone, reason, OutsideSlotNeverAutoRemovedReason, cur.PurgeKept)
	}
}

// ---- 3b. a purge_failed version's kept notes stay swept ----

// An escalated version leaves ListPurgedBookVersions, and the nightly cleanup does
// not retry it, so its remaining PurgeKept notes were looked at by nothing, ever.
// They are swept every night: report-only lists what would go, apply removes it and
// drops the note, and the version is never escalated again.
func TestSweepKeptFiles_SweepsTheKeptNotesOfAPurgeFailedVersion(t *testing.T) {
	f := newKeptFixture(t)
	bad := f.addKept(t, "kept once")
	owned := f.addKept(t, "kept once")
	_, rowID := f.rowOwner(t, owned)
	s := &flakyOwnerStore{PebbleStore: f.store, path: bad, down: true}
	escalate(t, f, s)

	// Still owned: kept, and nothing to report.
	res := sweepReport(t, s, testGuard)
	requireCounts(t, res, 0, 0, 1, 0)

	if err := f.store.DeleteBookFile(rowID); err != nil {
		t.Fatal(err)
	}
	noRemovals(t)
	for night := 0; night < PurgeUndecidedEscalateAfter+1; night++ {
		rep := sweepReport(t, s, testGuard)
		if rep.WouldRemove != 1 || len(rep.WouldRemoveFiles) != 1 || rep.WouldRemoveFiles[0].VersionID != f.ver.ID || rep.WouldRemoveFiles[0].Files[0].Path != owned {
			t.Fatalf("night %d: report-only = %+v, want %s of the purge_failed version listed as would-remove", night, rep, owned)
		}
		requireOnDisk(t, owned)
		if len(rep.Escalated) != 0 {
			t.Fatalf("report-only escalated a purge_failed version again: %+v", rep.Escalated)
		}
	}
	removeFile = os.Remove // noRemovals' guard is for the report-only runs above

	res = sweep(t, s, testGuard)
	requireCounts(t, res, 1, 0, 0, 0)
	requireGone(t, owned)
	requireOnDisk(t, bad)
	cur := f.stored(t)
	if cur.Status != database.BookVersionStatusPurgeFailed || len(cur.PurgeFailed) != 1 || cur.PurgeFailed[0].Path != bad || len(cur.PurgeKept) != 0 {
		t.Errorf("stored = %s purge_failed=%+v kept=%+v, want purge_failed with only %s in PurgeFailed", cur.Status, cur.PurgeFailed, cur.PurgeKept, bad)
	}
}

// ---- nit: a night is a calendar day ----

// A note that cannot be settled counts one night however many times the sweep runs
// that day (a manual run, a retry): three runs on one day are not three nights.
func TestSweepKeptFiles_CountsAtMostOneNightPerCalendarDay(t *testing.T) {
	f := newKeptFixture(t)
	p := f.addKept(t, "kept once")
	s := &flakyOwnerStore{PebbleStore: f.store, path: p, down: true}

	nextNight(t)
	for run := 0; run < PurgeUndecidedEscalateAfter+2; run++ {
		res := sweepSameNight(t, s, testGuard, run%2 == 0)
		if len(res.Escalated) != 0 {
			t.Fatalf("run %d of one day escalated: %+v", run+1, res.Escalated)
		}
	}
	cur := f.stored(t)
	if cur.Status != database.BookVersionStatusInactivePurged || cur.PurgeUndecidedNights != 1 {
		t.Fatalf("after many runs on one day: status=%s nights=%d, want inactive_purged and 1", cur.Status, cur.PurgeUndecidedNights)
	}

	// The next two days count, and the third night escalates.
	sweep(t, s, testGuard)
	if n := f.stored(t).PurgeUndecidedNights; n != 2 {
		t.Fatalf("second day: nights = %d, want 2", n)
	}
	res := sweep(t, s, testGuard)
	if len(res.Escalated) != 1 {
		t.Fatalf("third day: Escalated = %+v, want the version", res.Escalated)
	}

	// A clean day still resets the count, and clears the day (the file is free now,
	// so that sweep removes it).
	g := newKeptFixture(t)
	q := g.addKept(t, "kept once")
	gs := &flakyOwnerStore{PebbleStore: g.store, path: q, down: true}
	sweep(t, gs, testGuard)
	gs.down = false
	sweep(t, gs, testGuard)
	if cur := g.stored(t); cur.PurgeUndecidedNights != 0 || cur.PurgeUndecidedNightOn != "" {
		t.Errorf("after a clean day: nights=%d on=%q, want 0 and empty", cur.PurgeUndecidedNights, cur.PurgeUndecidedNightOn)
	}
}
