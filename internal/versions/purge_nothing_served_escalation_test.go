// file: internal/versions/purge_nothing_served_escalation_test.go
// version: 1.1.0
// guid: 8a34f7fd-614f-42d4-a946-7e16987a4312
// last-edited: 2026-10-30

package versions

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// A purging version cannot be restored and cannot be hard-deleted, so a book that
// serves nothing on disk (its file is lost or its path is stale) must not leave it
// stuck for ever: each refused attempt is counted like a stuck one and the version
// becomes purge_failed after PurgeUndecidedEscalateAfter in a row. Nothing is
// deleted by any of it.

// nothingServedPurging makes a purging version of the fixture's live book, with
// one file in its slot, and takes the book's own file away so the book serves
// nothing that exists on disk.
func nothingServedPurging(t *testing.T, status string) (f *keptFixture, ver *database.BookVersion, slotFile string) {
	t.Helper()
	f = newKeptFixture(t)
	trashedAt := time.Now().Add(-30 * day)
	v, err := f.store.CreateBookVersion(&database.BookVersion{
		BookID: f.book.ID, Status: database.BookVersionStatusTrash, Format: "m4b", Source: "imported", TrashedAt: &trashedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	slotFile = filepath.Join(VersionSlotDir(f.bookDir, v.ID), "part1.m4b")
	writeTestFile(t, slotFile, "audio")
	// The store only lets a version reach purge_failed through purging.
	for _, next := range []string{database.BookVersionStatusPurging, database.BookVersionStatusPurgeFailed} {
		if status == database.BookVersionStatusTrash || (next == database.BookVersionStatusPurgeFailed && status == database.BookVersionStatusPurging) {
			break
		}
		v.Status = next
		if next == database.BookVersionStatusPurgeFailed {
			v.PurgeFailed = []database.PurgeFileNote{{Path: slotFile, Reason: "earlier failure"}}
		}
		if err := f.store.UpdateBookVersion(v); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(filepath.Join(f.bookDir, "year-one.m4b")); err != nil {
		t.Fatal(err)
	}
	return f, v, slotFile
}

func storedVersion(t *testing.T, f *keptFixture, id string) *database.BookVersion {
	t.Helper()
	cur, err := f.store.GetBookVersion(id)
	if err != nil || cur == nil {
		t.Fatalf("reread version %s: %v (nil=%v)", id, err, cur == nil)
	}
	return cur
}

func TestPurgeVersion_PurgingVersionWhoseBookServesNothingHasAWayOut(t *testing.T) {
	f, v, slotFile := nothingServedPurging(t, database.BookVersionStatusPurging)
	clock := useDay(t, time.Date(2026, 10, 1, 3, 0, 0, 0, time.Local))

	for n := 1; n < PurgeUndecidedEscalateAfter; n++ {
		clock.next()
		err := PurgeVersion(f.store, storedVersion(t, f, v.ID), testGuard)
		var ns *NothingServedError
		if !errors.Is(err, ErrPurgeNothingServed) || errors.Is(err, ErrPurgeFailed) || !errors.As(err, &ns) || ns.Attempt != n || ns.Escalated {
			t.Fatalf("attempt %d = %v, want a counted ErrPurgeNothingServed that has not escalated", n, err)
		}
		cur := storedVersion(t, f, v.ID)
		if cur.Status != database.BookVersionStatusPurging || cur.PurgeUndecidedNights != n {
			t.Fatalf("attempt %d: status=%s nights=%d, want purging and %d", n, cur.Status, cur.PurgeUndecidedNights, n)
		}
		if err := f.store.DeleteBookVersion(v.ID); !errors.Is(err, database.ErrVersionPurging) {
			t.Fatalf("attempt %d: hard delete = %v, want it refused while purging", n, err)
		}
		requireOnDisk(t, slotFile)
	}

	clock.next()
	err := PurgeVersion(f.store, storedVersion(t, f, v.ID), testGuard)
	var ns *NothingServedError
	if !errors.Is(err, ErrPurgeNothingServed) || !errors.Is(err, ErrPurgeFailed) || !errors.As(err, &ns) || !ns.Escalated {
		t.Fatalf("attempt %d = %v, want the escalating ErrPurgeNothingServed that also matches ErrPurgeFailed", PurgeUndecidedEscalateAfter, err)
	}
	cur := storedVersion(t, f, v.ID)
	if cur.Status != database.BookVersionStatusPurgeFailed || cur.PurgeFailedAt == nil || cur.PurgeUndecidedNights != 0 {
		t.Fatalf("status=%s failed_at=%v nights=%d, want purge_failed with the count reset", cur.Status, cur.PurgeFailedAt, cur.PurgeUndecidedNights)
	}
	if len(cur.PurgeFailed) != 1 || cur.PurgeFailed[0].Path != slotFile {
		t.Errorf("PurgeFailed = %+v, want the slot file the purge could not decide", cur.PurgeFailed)
	}
	requireOnDisk(t, slotFile) // escalating deletes nothing

	// The way out: purge_failed is hard-deletable, and the file named in it stays.
	if err := f.store.DeleteBookVersion(v.ID); err != nil {
		t.Fatalf("hard delete of the escalated version: %v", err)
	}
	if gone, _ := f.store.GetBookVersion(v.ID); gone != nil {
		t.Errorf("the version is still there after the hard delete: %+v", gone)
	}
	requireOnDisk(t, slotFile)
}

// The book's file comes back before the third attempt: the purge finishes, and the
// count does not linger on the purged record.
func TestPurgeVersion_PurgingVersionFinishesOnceTheBookServesAFileAgain(t *testing.T) {
	f, v, slotFile := nothingServedPurging(t, database.BookVersionStatusPurging)
	for n := 1; n < PurgeUndecidedEscalateAfter; n++ {
		if err := PurgeVersion(f.store, storedVersion(t, f, v.ID), testGuard); !errors.Is(err, ErrPurgeNothingServed) {
			t.Fatalf("attempt %d = %v, want ErrPurgeNothingServed", n, err)
		}
	}
	writeTestFile(t, filepath.Join(f.bookDir, "year-one.m4b"), "audio")
	if err := PurgeVersion(f.store, storedVersion(t, f, v.ID), testGuard); err != nil {
		t.Fatalf("PurgeVersion once the book serves a file: %v", err)
	}
	requireGone(t, slotFile)
	if cur := storedVersion(t, f, v.ID); cur.Status != database.BookVersionStatusInactivePurged || cur.PurgeUndecidedNights != 0 {
		t.Errorf("status=%s nights=%d, want inactive_purged with no count", cur.Status, cur.PurgeUndecidedNights)
	}
}

// A trashed version is never claimed and never counted: it stays trash,
// restorable, however many runs refuse it. A purge_failed one stays as it is.
func TestPurgeVersion_NothingServedDoesNotCountATrashedOrPurgeFailedVersion(t *testing.T) {
	for _, status := range []string{database.BookVersionStatusTrash, database.BookVersionStatusPurgeFailed} {
		t.Run(status, func(t *testing.T) {
			f, v, slotFile := nothingServedPurging(t, status)
			for n := 0; n < PurgeUndecidedEscalateAfter+2; n++ {
				err := PurgeVersion(f.store, storedVersion(t, f, v.ID), testGuard)
				if !errors.Is(err, ErrPurgeNothingServed) {
					t.Fatalf("attempt %d = %v, want ErrPurgeNothingServed", n+1, err)
				}
				var ns *NothingServedError
				if errors.As(err, &ns) && (ns.Attempt != 0 || ns.Escalated) {
					t.Fatalf("attempt %d counted a %s version: %+v", n+1, status, ns)
				}
			}
			cur := storedVersion(t, f, v.ID)
			if cur.Status != status || cur.PurgeUndecidedNights != 0 {
				t.Errorf("status=%s nights=%d, want %s and 0", cur.Status, cur.PurgeUndecidedNights, status)
			}
			requireOnDisk(t, slotFile)
		})
	}
}

// The nightly cleanup files the escalating night under NothingServed (a book
// problem, not a failed purge), and the next night lists the version as needing
// attention instead of retrying it.
func TestCleanup_PurgingVersionWhoseBookServesNothingEscalatesWithoutFailing(t *testing.T) {
	f, v, slotFile := nothingServedPurging(t, database.BookVersionStatusPurging)
	clock := useDay(t, time.Date(2026, 10, 1, 3, 0, 0, 0, time.Local))
	for night := 1; night <= PurgeUndecidedEscalateAfter; night++ {
		clock.next()
		res, err := cleanupTrashedVersionsAt(f.store, time.Now(), testGuard, true)
		if err != nil || res.Failed != 0 || res.Purged != 0 {
			t.Fatalf("night %d: err=%v failed=%d purged=%d, want a clean run that purged nothing", night, err, res.Failed, res.Purged)
		}
		if len(res.NothingServed) != 1 || res.NothingServed[0].VersionID != v.ID {
			t.Fatalf("night %d: NothingServed = %+v, want the version", night, res.NothingServed)
		}
	}
	if cur := storedVersion(t, f, v.ID); cur.Status != database.BookVersionStatusPurgeFailed {
		t.Fatalf("status = %s after %d nights, want purge_failed", cur.Status, PurgeUndecidedEscalateAfter)
	}
	res, err := cleanupTrashedVersionsAt(f.store, time.Now(), testGuard, true)
	if err != nil || len(res.NeedsAttention) != 1 || res.NeedsAttention[0].VersionID != v.ID || len(res.NothingServed) != 0 {
		t.Errorf("next night: err=%v NeedsAttention=%+v NothingServed=%+v, want it listed as needing attention only", err, res.NeedsAttention, res.NothingServed)
	}
	requireOnDisk(t, slotFile)
}

// A refusal counts at most one night per calendar day, as a stuck attempt does:
// a Purge now click the same day runs but does not advance the count toward
// purge_failed; the next day's refusal does.
func TestSettleNothingServed_CountsAtMostOncePerCalendarDay(t *testing.T) {
	f, v, slotFile := nothingServedPurging(t, database.BookVersionStatusPurging)
	clock := useDay(t, time.Date(2026, 10, 1, 3, 0, 0, 0, time.Local))
	attempt := func() (*database.BookVersion, *NothingServedError) {
		t.Helper()
		err := PurgeVersion(f.store, storedVersion(t, f, v.ID), testGuard)
		var ns *NothingServedError
		if !errors.As(err, &ns) {
			t.Fatalf("PurgeVersion = %v, want a *NothingServedError", err)
		}
		return storedVersion(t, f, v.ID), ns
	}

	clock.next()
	if cur, ns := attempt(); cur.PurgeUndecidedNights != 1 || ns.Attempt != 1 {
		t.Fatalf("first refusal of day 1: nights=%d attempt=%d, want 1 and 1", cur.PurgeUndecidedNights, ns.Attempt)
	}
	clock.t = clock.t.Add(5 * time.Hour) // a Purge now click later the same day
	cur, ns := attempt()
	if cur.PurgeUndecidedNights != 1 || ns.Attempt != 1 || cur.PurgeUndecidedNightOn != "2026-10-01" {
		t.Fatalf("second refusal of day 1: nights=%d attempt=%d on=%q, want 1, 1 and 2026-10-01", cur.PurgeUndecidedNights, ns.Attempt, cur.PurgeUndecidedNightOn)
	}
	clock.t = clock.t.Add(-5 * time.Hour)
	clock.next()
	if cur, ns := attempt(); cur.PurgeUndecidedNights != 2 || ns.Attempt != 2 || cur.PurgeUndecidedNightOn != "2026-10-02" {
		t.Fatalf("day 2: nights=%d attempt=%d on=%q, want 2, 2 and 2026-10-02", cur.PurgeUndecidedNights, ns.Attempt, cur.PurgeUndecidedNightOn)
	}
	if cur := storedVersion(t, f, v.ID); cur.Status != database.BookVersionStatusPurging {
		t.Errorf("status = %s, want purging", cur.Status)
	}
	requireOnDisk(t, slotFile)
}
