// file: core/internal/restoreswap/resetmoved_test.go
// version: 1.0.1
// guid: b1d38611-602f-4c12-beb2-452f0b65bc7c
// last-edited: 2026-11-08

package restoreswap

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// TestResetJournalsEachMoveOnce (R1-21 review n1): a reset's set-aside
// journals one moved record per item it moved. A target that never existed
// gets none, and a target an earlier boot journaled gets none again when
// the set-aside resumes after a kill that fell past its record.
func TestResetJournalsEachMoveOnce(t *testing.T) {
	f := resetFixture(t, restorejournal.ResetStore, "outside")
	var present []string
	for _, tg := range f.in.Targets {
		if ok, err := exists(f.mem, tg.From); err != nil {
			t.Fatal(err)
		} else if ok {
			present = append(present, tg.From)
		}
	}
	if len(present) == len(f.in.Targets) {
		t.Fatal("fixture: every target exists; the test needs one that does not")
	}
	movedFroms := func(recs []restorejournal.Record) []string {
		var out []string
		for _, r := range recs {
			if r.Step == StepMoved {
				out = append(out, r.From)
			}
		}
		return out
	}
	boot := func() []restorejournal.Record {
		t.Helper()
		s := newSwapper(t, f.mem, testBuild, testNow)
		if out, err := s.RunP0a(context.Background()); err != nil || out.Boot != BootReset || out.Last != StepSetAside {
			t.Fatalf("P0a = %+v, %v; want a reset boot at set_aside", out, err)
		}
		recs, err := s.Journal(f.in.ID)
		if err != nil {
			t.Fatal(err)
		}
		return recs
	}

	recs := boot()
	if got := movedFroms(recs); !slices.Equal(got, present) {
		t.Fatalf("moved records for %v, want one for each target that existed: %v", got, present)
	}

	// A kill right after the first moved record: the later items are set
	// aside, their records lost, and the in-flight file stays.
	first := slices.IndexFunc(recs, func(r restorejournal.Record) bool { return r.Step == StepMoved })
	writeJournal(t, f.mem, f.in.ID, recs[:first+1])
	if got := movedFroms(boot()); !slices.Equal(got, present) {
		t.Fatalf("after the resume, moved records for %v, want each item once: %v", got, present)
	}
}

// TestResetResumeMovesAJournaledTargetAgain (R1-21 review M6, found by the
// matrix's nested kills): a moved record says the item was set aside once,
// not where it is now. A failed set-aside's move-back renames it back
// before rejected is journaled, so a kill inside the move-back leaves the
// record beside an item at its source (or at both names), and the resume
// must set it aside again, never boot the original store as the reset.
func TestResetResumeMovesAJournaledTargetAgain(t *testing.T) {
	f := resetFixture(t, restorejournal.ResetStore, "outside")
	db := f.in.Targets[0]
	recs := func() []restorejournal.Record {
		s := newSwapper(t, f.mem, testBuild, testNow)
		if out, err := s.RunP0a(context.Background()); err != nil || out.Boot != BootReset {
			t.Fatalf("P0a = %+v, %v; want a reset boot", out, err)
		}
		recs, err := s.Journal(f.in.ID)
		if err != nil {
			t.Fatal(err)
		}
		return recs
	}()
	first := slices.IndexFunc(recs, func(r restorejournal.Record) bool { return r.Step == StepMoved })
	if first < 0 || recs[first].From != db.From {
		t.Fatalf("journal %v: want the store's moved record first", steps(recs))
	}
	// The move-back of the store, then the kill: the journal ends at the
	// store's moved record and the in-flight file stays.
	writeJournal(t, f.mem, f.in.ID, recs[:first+1])
	if err := f.mem.Rename(db.To, db.From); err != nil {
		t.Fatal(err)
	}
	out, err := newSwapper(t, f.mem, testBuild, testNow).RunP0a(context.Background())
	if err != nil || out.Boot != BootReset || out.Last != StepSetAside {
		t.Fatalf("P0a = %+v, %v; want the reset resumed to set_aside", out, err)
	}
	if ok, err := exists(f.mem, db.From); err != nil || ok {
		t.Fatalf("the original store is still at %s (%v): the resume trusted its moved record", db.From, err)
	}
	if ok, err := exists(f.mem, db.To); err != nil || !ok {
		t.Fatalf("the original store is not set aside at %s (%v)", db.To, err)
	}
	if got := treeHash(t, f.mem, db.To); got != f.live {
		t.Fatal("the set-aside store is not the original")
	}
}

// writeJournal replaces restore/<id>/journal.json with recs.
func writeJournal(t *testing.T, fs vfs.FS, id string, recs []restorejournal.Record) {
	t.Helper()
	var b []byte
	for _, r := range recs {
		line, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		b = append(append(b, line...), '\n')
	}
	f, err := fs.Create(journalPath(stateDir, id), vfs.WriteCategoryUnspecified)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}
