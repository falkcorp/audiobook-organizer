// file: core/internal/restoreswap/rewrite_resume_test.go
// version: 1.0.0
// guid: 2383dc1a-c332-41dd-b704-ef8219c12403
// last-edited: 2026-11-11

package restoreswap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// TestResumeRewriteByJournalStep (05-storage §5S.3.13 phase 3, P0a over an
// in-flight KindRewrite journal): a journal at swapped or opened boots the
// copy and leaves M8's phase 4 to finish (BootRewrite, the in-flight file
// kept); a terminal journal (applied, swapped_back, rejected) only loses its
// in-flight file and boots plain; a step this build never writes there is
// ErrJournalCorrupt, never a guess.
func TestResumeRewriteByJournalStep(t *testing.T) {
	for _, c := range []struct {
		name     string
		extra    string // appended after swapped
		boot     BootKind
		inflight bool // the in-flight file stays
		corrupt  bool
	}{
		{name: "swapped", boot: BootRewrite, inflight: true},
		{name: "opened", extra: StepOpened, boot: BootRewrite, inflight: true},
		{name: "applied", extra: StepApplied},
		{name: "swapped back", extra: StepSwappedBack},
		{name: "rejected", extra: StepRejected},
		{name: "unknown step", extra: "gotham_burning", corrupt: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			mem, s, in, _ := rewriteSwapped(t, dbPathOf("state"))
			if c.extra != "" {
				if err := s.appendRec(ctx, in.ID, restorejournal.Record{Step: c.extra}); err != nil {
					t.Fatal(err)
				}
			}
			recs, err := ReadJournal(mem, stateDir, in.ID)
			if err != nil {
				t.Fatal(err)
			}
			out, err := s.resumeRewrite(ctx, in, recs)
			if c.corrupt {
				if !errors.Is(err, ErrJournalCorrupt) {
					t.Fatalf("resume at %q = %+v, %v; want ErrJournalCorrupt", c.extra, out, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := c.extra
			if want == "" {
				want = StepSwapped
			}
			if out.Boot != c.boot || out.ID != in.ID || out.Last != want {
				t.Errorf("outcome = %+v, want boot %d, id %s, last %q", out, c.boot, in.ID, want)
			}
			if ok, _ := exists(mem, s.inflightPath(in.ID)); ok != c.inflight {
				t.Errorf("in-flight file present = %t, want %t", ok, c.inflight)
			}
		})
	}
}

// TestRewriteSwappedBackOutcome: the outcome of a swap-back P0a ran is the
// original store booting plain with the journal's id; a swap-back that failed
// (any error but ErrSwappedBack) halts boot with that error.
func TestRewriteSwappedBackOutcome(t *testing.T) {
	_, s, in, _ := rewriteSwapped(t, dbPathOf("state"))
	out, err := s.rewriteSwappedBack(in, fmt.Errorf("the copy would not open: %w", ErrSwappedBack))
	if err != nil || out.ID != in.ID || out.Last != StepSwappedBack || out.Boot != BootPlain {
		t.Errorf("a swap-back that ran = %+v, %v; want the plain boot of the original", out, err)
	}
	boom := errors.New("the batmobile will not move")
	if out, err := s.rewriteSwappedBack(in, boom); !errors.Is(err, boom) || out.ID != "" {
		t.Errorf("a swap-back that failed = %+v, %v; want the error and no outcome", out, err)
	}
}

// pendingRewriteIntent stages a KindRewrite intent for the step in the slot
// as M8's create leaves it before its in-flight rename.
func pendingRewriteIntent(t *testing.T, s *Swapper, mem *vfs.MemFS, stepID string) restorejournal.Intent {
	t.Helper()
	db := dbPathOf("state")
	id := "rewrite-19390501T130000.000000000Z"
	in := restorejournal.Intent{ID: id, Kind: restorejournal.KindRewrite, DBPath: db,
		Targets:       []restorejournal.Target{{From: db + ".rewrite", To: db}},
		StagedByBuild: testBuild, ExpiresAt: testNow().Add(rewriteExpiry), RequestedBy: "core/storage " + stepID,
		RequestedAt: testNow(), StepID: stepID}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if err := mem.MkdirAll(restorejournal.Dir(stateDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(mem, s.pendingPath(), raw, privateFile); err != nil {
		t.Fatal(err)
	}
	return in
}

// TestP0aRejectsAKindRewriteIntentLeftStaged: a KindRewrite intent in the
// slot (a kill between M8's create and its in-flight rename) moved nothing,
// so P0a journals it rejected, moves it to rejected.json and boots plain with
// no degraded token (M8 stages a new one); the next boot finds the slot
// empty. An intent for a step this build does not register refuses the boot
// with ErrRewriteNotInThisBuild and leaves the slot as it was.
func TestP0aRejectsAKindRewriteIntentLeftStaged(t *testing.T) {
	mem := vfs.NewMem()
	ctx := bootseam.With(context.Background(), &bootseam.Seams{FS: mem})
	s, err := New(Options{FS: mem, StateDir: stateDir, BuildID: testBuild, Clock: testNow, RewriteStep: rewriteStepID})
	if err != nil {
		t.Fatal(err)
	}
	in := pendingRewriteIntent(t, s, mem, rewriteStepID)
	out, err := s.RunP0a(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if out.ID != in.ID || out.Last != StepRejected || out.Degraded != "" || out.Boot != BootPlain {
		t.Errorf("outcome = %+v, want the rejection of %s with no degraded token", out, in.ID)
	}
	if ok, _ := exists(mem, s.pendingPath()); ok {
		t.Error("the rejected intent is still in the slot")
	}
	if ok, _ := exists(mem, filepath.Join(restorejournal.JournalDir(stateDir, in.ID), restorejournal.RejectedName)); !ok {
		t.Error("the rejected intent was not kept as rejected.json")
	}
	if last, err := s.last(in.ID); err != nil || last != StepRejected {
		t.Errorf("journal last = %q, %v; want rejected", last, err)
	}
	if again, err := s.RunP0a(ctx); err != nil || again.ID != "" {
		t.Errorf("the next boot = %+v, %v; want a plain boot with an empty slot", again, err)
	}

	other := pendingRewriteIntent(t, s, mem, "books.other-rewrite")
	if _, err := s.RunP0a(ctx); !errors.Is(err, ErrRewriteNotInThisBuild) {
		t.Errorf("an intent for another build's M8 = %v, want ErrRewriteNotInThisBuild", err)
	}
	if ok, _ := exists(mem, s.pendingPath()); !ok {
		t.Errorf("the refused intent %s left the slot", other.ID)
	}
}

// TestRewriteDryCopiesIntoScratchAndSwapsNothing (`storage migrate
// --dry-run`): phases 1 and 2 run over the scratch store into the second
// scratch directory, whose copy holds every row, and M8's record on the
// scratch store reads done; nothing is staged, no journal is written and the
// store keeps its generation and its own directory.
func TestRewriteDryCopiesIntoScratchAndSwapsNothing(t *testing.T) {
	mem := vfs.NewMem()
	mem.TestingSetDiskUsage(vfs.DiskUsage{AvailBytes: 1 << 40, TotalBytes: 1 << 41, UsedBytes: 1 << 40})
	db := dbPathOf("outside")
	m8Store(t, mem, db, 25)
	dry := db + ".dryrun-copy"
	var stats storage.RewriteCopyStats
	var record string
	res := m8BootWith(t, mem, db, func(ctx context.Context, sdb *storage.DB) error {
		var err error
		stats, err = RewriteDry(ctx, sdb, storage.NewRewriteStep("wayne", "wayne.rewrite"), dry)
		if err != nil {
			return err
		}
		record, err = sdb.RewriteRecord(ctx, "wayne.rewrite")
		return err
	})
	if res.err != nil {
		t.Fatalf("boot: %v", res.err)
	}
	if stats.Keys < 25 || stats.Tables < 1 || stats.Bytes <= 0 {
		t.Errorf("copy stats = %+v, want at least the 25 rows", stats)
	}
	if record != storage.DataMigrationDone {
		t.Errorf("M8's record on the scratch store = %q, want done", record)
	}
	if ok, _ := exists(mem, dry); !ok {
		t.Errorf("the dry copy %s was not written", dry)
	}
	if res.rows != 25 || res.genAfter != res.genBefore {
		t.Errorf("rows %d, generation %d -> %d; want 25 rows and an unchanged generation", res.rows, res.genBefore, res.genAfter)
	}
	if names, _ := mem.List(restorejournal.Dir(stateDir)); len(names) != 0 {
		t.Errorf("a dry run left swap state behind: %v", names)
	}
}

// TestCheckOfflineReadsTheSlotWithoutWriting (R1-25 review SF3): with
// nothing in flight the check passes; with a KindRewrite swap in flight (a
// journal M8 owns, never escapable) any source kind is refused naming the
// swap (restore_inflight), and the check writes nothing either way.
func TestCheckOfflineReadsTheSlotWithoutWriting(t *testing.T) {
	db := dbPathOf("state")
	mem := vfs.NewMem()
	idle, err := New(Options{FS: mem, StateDir: stateDir, BuildID: testBuild, Clock: testNow})
	if err != nil {
		t.Fatal(err)
	}
	if id, err := idle.CheckOffline(db, restorejournal.KindCheckpoint); err != nil || id != "" {
		t.Errorf("an idle slot = %q, %v; want it admitted", id, err)
	}

	_, s, in, _ := rewriteSwapped(t, db)
	for _, src := range []restorejournal.Kind{restorejournal.KindCheckpoint, restorejournal.KindArchive} {
		id, err := s.CheckOffline(db, src)
		if err == nil || id != "" || !strings.Contains(err.Error(), "restore_inflight") || !strings.Contains(err.Error(), StepSwapped) {
			t.Errorf("a KindRewrite swap in flight, source %s = %q, %v; want restore_inflight at %q", src, id, err, StepSwapped)
		}
	}
	if ok, _ := exists(s.fs, s.inflightPath(in.ID)); !ok {
		t.Error("the check removed the in-flight file")
	}
}
