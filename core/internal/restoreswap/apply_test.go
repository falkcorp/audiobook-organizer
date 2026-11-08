// file: core/internal/restoreswap/apply_test.go
// version: 1.0.0
// guid: 8c3c6678-ce6b-467d-8c60-d8d958259492
// last-edited: 2026-11-07

package restoreswap

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/cockroachdb/pebble/v2/vfs/errorfs"

	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/internal/bootseam/bootseamtest"
	ipebble "github.com/falkcorp/mediacore/internal/pebble"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// scenario is one fixture and how its restore boot ends.
type scenario struct {
	name string
	f    fixture
	mode bootMode
}

func (sc scenario) reset() bool { return sc.f.in.Kind == restorejournal.KindReset }

// wantClean is the one terminal step a crash-only run of sc reaches.
func (sc scenario) wantClean() string {
	switch {
	case sc.reset():
		return StepHookDone
	case sc.mode == bootFailsAfter:
		return StepSwappedBack
	}
	return StepApplied
}

// scenarios is every kind of 05-storage §5S.3.16 machine 1, on both store
// layouts, with and without the location file, and each restore kind with a
// boot that fails after the store opened (step x run by the kernel).
func scenarios(t testing.TB) []scenario {
	t.Helper()
	var out []scenario
	for _, f := range []fixture{
		restoreFixture(t, restorejournal.KindArchive, "inside", true),
		restoreFixture(t, restorejournal.KindRevert, "outside", false),
		restoreFixture(t, restorejournal.KindCheckpoint, "inside", false),
	} {
		out = append(out, scenario{f.name + "/boots", f, bootSucceeds}, scenario{f.name + "/p4b-fails", f, bootFailsAfter})
	}
	for _, f := range []fixture{
		resetFixture(t, restorejournal.ResetStore, "inside"),
		resetFixture(t, restorejournal.ResetFactory, "outside"),
	} {
		out = append(out, scenario{f.name, f, bootSucceeds})
	}
	return out
}

// runBoots boots mem through a recorder until a boot reaches readiness
// (at most three boots, the automatic restarts).
func runBoots(t testing.TB, mem *vfs.MemFS, mode bootMode, opts bootseamtest.Options) (*bootseamtest.Recorder, error) {
	t.Helper()
	rec := bootseamtest.New(mem, opts)
	ctx := bootseam.With(context.Background(), rec.Seams())
	_, err := bootUntilReady(ctx, func() *Swapper { return newSwapper(t, rec.FS(), testBuild, testNow) }, mode)
	return rec, err
}

// errInjected is an error returned by one operation (decision D12).
func errInjected(n int) error {
	if n%2 == 0 {
		return fmt.Errorf("injected: %w", syscall.ENOSPC)
	}
	return fmt.Errorf("injected: %w", syscall.EACCES)
}

// failAt injects one error into the first operation matching ev (its label,
// ordinal and kind; Pebble's own operations are kill points only).
func failAt(ev bootseamtest.Event, n int) func(bootseamtest.Event) error {
	var done atomic.Bool
	return func(e bootseamtest.Event) error {
		if e.Pebble() || e.Label != ev.Label || e.Ordinal != ev.Ordinal || e.Kind != ev.Kind {
			return nil
		}
		if done.CompareAndSwap(false, true) {
			return errInjected(n)
		}
		return nil
	}
}

// checkEnd checks the cross-mechanism invariants after the boots of a
// fixture reached readiness: a terminal journal (one of want), no slot file
// left, exactly one copy of the original store and (restore kinds) of the
// staged store, the one live store being the one the journal names, its
// level sidecar agreeing with it, and the location file kept.
func checkEnd(t testing.TB, fs vfs.FS, f fixture, want ...string) {
	t.Helper()
	in := f.in
	db := in.DBPath
	recs, err := ReadJournal(fs, stateDir, in.ID)
	if err != nil {
		t.Fatalf("journal: %v", err)
	}
	last := lastStep(recs)
	if !terminal(last) || !slices.Contains(want, last) {
		t.Fatalf("journal ends at %q, want one of %v (records %v)", last, want, steps(recs))
	}
	if ok, _ := exists(fs, restorejournal.PendingPath(stateDir)); ok {
		t.Fatal("pending.json is left")
	}
	if id, ok, err := InFlight(fs, stateDir); err != nil || ok {
		t.Fatalf("in flight after a terminal step: %q %v %v", id, ok, err)
	}
	aside := restorejournal.SetAsideDest(stateDir, db, in.ID, db)
	failed := restorejournal.FailedDest(stateDir, db, in.ID, db)
	wantLevel := 0
	if in.Kind == restorejournal.KindReset {
		switch last {
		case StepHookDone:
			if got := treeHash(t, fs, aside); got != f.live {
				t.Fatalf("the original store is not at its set-aside %s", aside)
			}
			if ok, _ := exists(fs, filepath.Join(db, "fresh")); !ok {
				t.Fatal("no fresh install at the store path")
			}
			for _, tg := range in.Targets[1:] {
				if ok, _ := exists(fs, tg.From); ok {
					t.Fatalf("set-aside target %s is still live", tg.From)
				}
			}
			// A delete entry the kernel could not remove is recorded on
			// hook_done, never fatal (05-storage §5S.3.15).
			for _, p := range in.Delete {
				if ok, _ := exists(fs, p); ok && !slices.Contains(recs[len(recs)-1].Failed, p) {
					t.Fatalf("delete entry %s is left and not recorded as failed", p)
				}
			}
			return // the fake fresh install writes no sidecar
		case StepRejected:
			if got := treeHash(t, fs, db); got != f.live {
				t.Fatal("a rejected reset left the store moved")
			}
			for _, tg := range in.Targets {
				if ok, _ := exists(fs, tg.To); ok {
					t.Fatalf("a rejected reset left %s set aside", tg.To)
				}
			}
			if ok, _ := exists(fs, filepath.Join(stateDir, "config.boot.json")); !ok {
				t.Fatal("a rejected reset lost config.boot.json")
			}
		}
	} else {
		staged := in.Targets[0].From
		switch last {
		case StepApplied:
			wantLevel = 1
			if got := treeHash(t, fs, db); got != f.staged {
				t.Fatal("the live store is not the staged store")
			}
			if got := treeHash(t, fs, aside); got != f.live {
				t.Fatalf("the original store is not at %s", aside)
			}
			if ok, _ := exists(fs, staged); ok {
				t.Fatal("the staged item is still in staging")
			}
		case StepSwappedBack, StepRejected:
			if got := treeHash(t, fs, db); got != f.live {
				t.Fatal("the live store is not the original store")
			}
			if ok, _ := exists(fs, aside); ok {
				t.Fatalf("the original store is also at %s", aside)
			}
			if ok, _ := exists(fs, db+restorejournal.CheckpointSuffix); ok {
				t.Fatal("the restore boot's checkpoint pointer is left beside the original store")
			}
			inStaging, inFailed := treeHash(t, fs, staged), treeHash(t, fs, failed)
			if (inStaging == f.staged) == (inFailed == f.staged) {
				t.Fatalf("the staged store is in staging %v and in failed/ %v: want exactly one", inStaging != "", inFailed != "")
			}
		}
	}
	levels, ok, err := ipebble.ReadLevels(fs, db)
	if err != nil || !ok || levels["wayne"] != wantLevel {
		t.Fatalf("level sidecar %v (ok %v, err %v) disagrees with the store, want wayne %d", levels, ok, err, wantLevel)
	}
	if f.location != "" {
		if got, ok := readFile(t, fs, filepath.Join(stateDir, locationFile)); !ok || got != f.location {
			t.Fatalf("backup.location.json = %q (present %v), want %q", got, ok, f.location)
		}
	} else if ok, _ := exists(fs, filepath.Join(stateDir, locationFile)); ok {
		t.Fatal("a location file appeared")
	}
}

func steps(recs []restorejournal.Record) []string {
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = r.Step
	}
	return out
}

// TestRestoreCrashAtEveryJournalStep is the swap's own crash matrix
// (05-storage §5S.3.16 machine 1; 06 §6.5.1 P0a): for every kind, a kill
// before every durable operation of the swap (a crash clone with no
// unsynced data, rebooted up to three times) and an error returned by every
// operation of the swap's own (ENOSPC or EACCES; decision D12) each end in a
// terminal journal with exactly one copy of each store, the live one the
// journal names, its sidecar agreeing, and the location file kept.
func TestRestoreCrashAtEveryJournalStep(t *testing.T) {
	for _, sc := range scenarios(t) {
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			rec, err := runBoots(t, copyMem(sc.f.mem), sc.mode, bootseamtest.Options{Clones: true})
			if err != nil {
				t.Fatalf("clean run: %v", err)
			}
			checkEnd(t, rec.Mem(), sc.f, sc.wantClean())
			clones := rec.Clones()
			if len(clones) < 20 {
				t.Fatalf("only %d kill points recorded", len(clones))
			}
			for _, c := range clones {
				t.Run(fmt.Sprintf("kill-%03d-%s", c.Event.Seq, c.Event.Label), func(t *testing.T) {
					if _, err := runBoots(t, c.FS, sc.mode, bootseamtest.Options{}); err != nil {
						t.Fatalf("kill before %v %s: reboot: %v", c.Event.Kind, c.Event.Path, err)
					}
					checkEnd(t, c.FS, sc.f, sc.wantClean())
				})
			}
			want := []string{StepApplied, StepSwappedBack, StepRejected}
			if sc.reset() {
				want = []string{StepHookDone, StepRejected}
			}
			for n, ev := range rec.Events() {
				if ev.Pebble() {
					continue
				}
				t.Run(fmt.Sprintf("error-%03d-%s", ev.Seq, ev.Label), func(t *testing.T) {
					mem := copyMem(sc.f.mem)
					if _, err := runBoots(t, mem, sc.mode, bootseamtest.Options{Fail: failAt(ev, n)}); err != nil {
						t.Fatalf("error at %v %s: boots: %v", ev.Kind, ev.Path, err)
					}
					checkEnd(t, mem, sc.f, want...)
				})
			}
		})
	}
}

// livePaths are the live items a swap renames: a restore's step c list, a
// reset's set-aside sources.
func livePaths(s *Swapper, in restorejournal.Intent) []string {
	var out []string
	moves := in.Targets
	if in.Kind != restorejournal.KindReset {
		moves = s.setAsides(in)
	}
	for _, m := range moves {
		out = append(out, m.From)
	}
	return out
}

// TestInflightPrecedesFirstRename (R9-7, DATA9-3): in every kind the
// in-flight rename's events precede the first rename of a live item, and a
// kill anywhere from that rename until the in-flight file is deleted finds
// the in-flight file and no pending.json, so the rollback guard's one probe
// (InFlight, the in-process twin of the find(1) probe) refuses.
func TestInflightPrecedesFirstRename(t *testing.T) {
	for _, sc := range scenarios(t) {
		if sc.mode != bootSucceeds {
			continue
		}
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			rec, err := runBoots(t, copyMem(sc.f.mem), sc.mode, bootseamtest.Options{Clones: true})
			if err != nil {
				t.Fatal(err)
			}
			live := livePaths(newSwapper(t, rec.FS(), testBuild, testNow), sc.f.in)
			first, inflight, removed := -1, -1, -1
			for _, ev := range rec.Events() {
				switch {
				case first < 0 && ev.Kind == errorfs.OpRename && slices.Contains(live, ev.Path):
					first = ev.Seq
				case inflight < 0 && ev.Label == "swap.slot.inflight" && ev.Kind == errorfs.OpRename:
					inflight = ev.Seq
				case removed < 0 && ev.Label == "swap.slot.remove" && ev.Kind == errorfs.OpRemove:
					removed = ev.Seq
				}
			}
			if first < 0 || inflight < 0 || removed < 0 {
				t.Fatalf("events missing: first live rename %d, in-flight rename %d, in-flight removal %d", first, inflight, removed)
			}
			if inflight > first {
				t.Fatalf("the in-flight rename (#%d) follows the first live rename (#%d)", inflight, first)
			}
			checked := 0
			for _, c := range rec.Clones() {
				if c.Event.Seq < first || c.Event.Seq > removed {
					continue
				}
				checked++
				if id, ok, err := InFlight(c.FS, stateDir); err != nil || !ok || id != sc.f.in.ID {
					t.Fatalf("kill before #%d %s: in flight %q %v %v", c.Event.Seq, c.Event.Label, id, ok, err)
				}
				if ok, _ := exists(c.FS, restorejournal.PendingPath(stateDir)); ok {
					t.Fatalf("kill before #%d %s: pending.json still exists", c.Event.Seq, c.Event.Label)
				}
			}
			if checked == 0 {
				t.Fatal("no kill point between the first live rename and the in-flight removal")
			}
		})
	}
}

// TestSwapFailureExitBeforeFirstMove (R10-6, DATA10-4, COR10-4): an error
// returned by any operation from the in-flight rename to the first
// journalled move ends that same boot in swapped_back (a restore; rejected
// when the in-flight rename itself never happened) or rejected (a reset),
// with the untouched store booted degraded, never a halt that wedges every
// later boot behind restore_inflight.
func TestSwapFailureExitBeforeFirstMove(t *testing.T) {
	for _, sc := range scenarios(t) {
		if sc.mode != bootSucceeds {
			continue
		}
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			rec, err := runBoots(t, copyMem(sc.f.mem), sc.mode, bootseamtest.Options{})
			if err != nil {
				t.Fatal(err)
			}
			end := "swap.journal." + StepPreviousMoved
			if sc.reset() {
				end = "swap.journal." + StepMoved
			}
			in, tried := false, 0
			for n, ev := range rec.Events() {
				if ev.Label == "swap.slot.inflight" {
					in = true
				}
				if !in || ev.Pebble() {
					continue
				}
				tried++
				mem := copyMem(sc.f.mem)
				r := bootseamtest.New(mem, bootseamtest.Options{Fail: failAt(ev, n)})
				out, err := newSwapper(t, r.FS(), testBuild, testNow).RunP0a(bootseam.With(context.Background(), r.Seams()))
				if err != nil {
					t.Fatalf("error at #%d %s %v: P0a halted: %v", ev.Seq, ev.Label, ev.Kind, err)
				}
				wantDegraded := DegradedRestoreFailed
				if sc.reset() {
					wantDegraded = DegradedFactoryResetFailed
				}
				if out.Boot != BootPlain || out.Degraded != wantDegraded {
					t.Fatalf("error at #%d %s %v: outcome %+v, want the untouched store booted degraded", ev.Seq, ev.Label, ev.Kind, out)
				}
				checkEnd(t, mem, sc.f, StepSwappedBack, StepRejected)
				if ev.Label == end {
					break
				}
			}
			if tried < 5 {
				t.Fatalf("only %d operations in the window", tried)
			}
		})
	}
}

// TestRestoreWithoutLocationFile (R10-6): with no backup.location.json (R1,
// a host loss) step c's copy-if-exists copies nothing, the restore applies,
// a swap-back restores nothing, and no location file appears either way.
func TestRestoreWithoutLocationFile(t *testing.T) {
	f := restoreFixture(t, restorejournal.KindArchive, "outside", false)
	for _, mode := range []bootMode{bootSucceeds, bootFailsAfter} {
		sc := scenario{f: f, mode: mode}
		mem := copyMem(f.mem)
		if _, err := runBoots(t, mem, mode, bootseamtest.Options{}); err != nil {
			t.Fatal(err)
		}
		checkEnd(t, mem, f, sc.wantClean())
		if ok, _ := exists(mem, filepath.Join(restorejournal.JournalDir(stateDir, f.in.ID), locationFile)); ok {
			t.Fatal("a location copy appeared in the journal directory")
		}
	}
}

// TestStaleOrForeignIntentRejected: step a's one rule for every kind (an
// intent past expires_at, with none, or staged by another binary) rejects
// it at P0a with restore_rejected, moves pending.json to rejected.json and
// leaves the store untouched; nothing is in flight, so the rollback guard
// passes.
func TestStaleOrForeignIntentRejected(t *testing.T) {
	late := func() time.Time { return testNow().Add(2 * time.Hour) }
	for _, c := range []struct {
		name  string
		f     func(t *testing.T) fixture
		build string
		clock func() time.Time
	}{
		{"archive-expired", func(t *testing.T) fixture { return restoreFixture(t, restorejournal.KindArchive, "inside", true) }, testBuild, late},
		{"checkpoint-foreign", func(t *testing.T) fixture { return restoreFixture(t, restorejournal.KindCheckpoint, "outside", false) }, "batcomputer-r0", testNow},
		{"reset-expired", func(t *testing.T) fixture { return resetFixture(t, restorejournal.ResetFactory, "inside") }, testBuild, late},
		{"reset-foreign", func(t *testing.T) fixture { return resetFixture(t, restorejournal.ResetStore, "outside") }, "batcomputer-r0", testNow},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := c.f(t)
			out, err := newSwapper(t, f.mem, c.build, c.clock).RunP0a(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if out.Boot != BootPlain || out.Degraded != DegradedRestoreRejected || out.Last != StepRejected {
				t.Fatalf("outcome %+v, want restore_rejected", out)
			}
			if ok, _ := exists(f.mem, filepath.Join(restorejournal.JournalDir(stateDir, f.in.ID), restorejournal.RejectedName)); !ok {
				t.Fatal("no rejected.json")
			}
			if got := treeHash(t, f.mem, f.in.DBPath); got != f.live {
				t.Fatal("a rejected intent moved the store")
			}
			recs, _ := ReadJournal(f.mem, stateDir, f.in.ID)
			if got := steps(recs); !slices.Equal(got, []string{StepStaged, StepRejected}) {
				t.Fatalf("journal %v", got)
			}
			if _, ok, _ := InFlight(f.mem, stateDir); ok {
				t.Fatal("a rejected intent is in flight")
			}
		})
	}
	t.Run("no-expires-at", func(t *testing.T) {
		f := restoreFixture(t, restorejournal.KindRevert, "inside", false)
		in := f.in
		in.ExpiresAt = time.Time{}
		// Rewrite the slot as a stager that set no expiry would have.
		if err := f.mem.Remove(restorejournal.PendingPath(stateDir)); err != nil {
			t.Fatal(err)
		}
		if err := restorejournal.CreateIntentFS(f.mem, stateDir, in); err != nil {
			t.Fatal(err)
		}
		out, err := newSwapper(t, f.mem, testBuild, testNow).RunP0a(context.Background())
		if err != nil || out.Degraded != DegradedRestoreRejected {
			t.Fatalf("outcome %+v, %v; want restore_rejected", out, err)
		}
	})
}

// TestCheckpointFailedRefusedBySameBinary (DATA7-12, DATA8-6): a
// KindCheckpoint restore whose migrations failed journals failed with the
// binary's ID and keeps its in-flight file; the same binary then refuses to
// boot (ErrCheckpointRestoreFailedHere), and a fixed build resumes it
// (failed → opened) to applied.
func TestCheckpointFailedRefusedBySameBinary(t *testing.T) {
	f := restoreFixture(t, restorejournal.KindCheckpoint, "inside", false)
	ctx := context.Background()
	s := newSwapper(t, f.mem, testBuild, testNow)
	out, err := s.RunP0a(ctx)
	if err != nil || out.Boot != BootRestore {
		t.Fatalf("P0a: %+v %v", out, err)
	}
	if err := errors.Join(s.Opened(ctx, out.ID), s.CheckpointFailed(ctx, out.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := newSwapper(t, f.mem, testBuild, testNow).RunP0a(ctx); !errors.Is(err, ErrCheckpointRestoreFailedHere) {
		t.Fatalf("same binary: %v, want ErrCheckpointRestoreFailedHere", err)
	}
	if _, ok, _ := InFlight(f.mem, stateDir); !ok {
		t.Fatal("a failed checkpoint restore lost its in-flight file")
	}
	fixed := newSwapper(t, f.mem, "batcomputer-r1-fixed", testNow)
	if _, err := fakeBoot(ctx, fixed, bootSucceeds); err != nil {
		t.Fatalf("fixed build: %v", err)
	}
	checkEnd(t, f.mem, f, StepApplied)
}

// TestRewriteJournalRefused: a KindRewrite intent is F3-04's M8 to apply;
// this build refuses to stage one and halts boot on one staged or in
// flight, touching nothing.
func TestRewriteJournalRefused(t *testing.T) {
	f := restoreFixture(t, restorejournal.KindRevert, "inside", false)
	rw := f.in
	rw.ID, rw.Kind, rw.StepID = "rw-1", restorejournal.KindRewrite, "books#M8"
	s := newSwapper(t, f.mem, testBuild, testNow)
	if err := s.Stage(context.Background(), rw); !errors.Is(err, ErrRewriteNotInThisBuild) {
		t.Fatalf("Stage: %v", err)
	}
	if err := f.mem.Remove(restorejournal.PendingPath(stateDir)); err != nil {
		t.Fatal(err)
	}
	if err := restorejournal.CreateIntentFS(f.mem, stateDir, rw); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RunP0a(context.Background()); !errors.Is(err, ErrRewriteNotInThisBuild) {
		t.Fatalf("pending: %v", err)
	}
	if err := f.mem.MkdirAll(restorejournal.JournalDir(stateDir, rw.ID), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := f.mem.Rename(restorejournal.PendingPath(stateDir), s.inflightPath(rw.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RunP0a(context.Background()); !errors.Is(err, ErrRewriteNotInThisBuild) {
		t.Fatalf("in flight: %v", err)
	}
	if got := treeHash(t, f.mem, f.in.DBPath); got != f.live {
		t.Fatal("a refused rewrite moved the store")
	}
}

// TestJournalTornTail (decision D2): a final line without its newline or
// that does not decode is a torn append and is dropped, the next append
// writes over it; an undecodable line before the last is ErrJournalCorrupt.
func TestJournalTornTail(t *testing.T) {
	mem := vfs.NewMem()
	s := newSwapper(t, mem, testBuild, testNow)
	ctx := context.Background()
	if err := mkdirAllSynced(mem, s.journalDir("j")); err != nil {
		t.Fatal(err)
	}
	path := journalPath(stateDir, "j")
	write := func(content string) {
		t.Helper()
		f, err := mem.Create(path, vfs.WriteCategoryUnspecified)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"step":"staged"}` + "\n" + `{"step":"rechecked"}` + "\n" + `{"step":"previous_mo`)
	recs, err := ReadJournal(mem, stateDir, "j")
	if err != nil || !slices.Equal(steps(recs), []string{StepStaged, StepRechecked}) {
		t.Fatalf("torn tail: %v %v", steps(recs), err)
	}
	if err := s.appendRec(ctx, "j", restorejournal.Record{Step: StepPreviousMoved}); err != nil {
		t.Fatal(err)
	}
	recs, err = ReadJournal(mem, stateDir, "j")
	if err != nil || !slices.Equal(steps(recs), []string{StepStaged, StepRechecked, StepPreviousMoved}) {
		t.Fatalf("after the append: %v %v", steps(recs), err)
	}
	write(`{"step":"staged"}` + "\n" + `{"step":"rechecked"` + "\n" + `{"step":"previous_moved"}` + "\n")
	if _, err := ReadJournal(mem, stateDir, "j"); !errors.Is(err, ErrJournalCorrupt) {
		t.Fatalf("corrupt middle line: %v, want ErrJournalCorrupt", err)
	}
}
