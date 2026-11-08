// file: core/internal/restoreswap/kept_test.go
// version: 1.0.1
// guid: 4e7c9d27-c099-4504-9295-f6fe874514bf
// last-edited: 2026-11-08

package restoreswap

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// keptEntries is rule (4)'s kept paths for a reset of db with id: the DB's
// parent, the state directory, restore/, the set-aside store and a file in
// it, and the live store.
func keptEntries(db, id string) []string {
	prev := restorejournal.SetAsideDest(stateDir, db, id, db)
	return []string{filepath.Dir(db), stateDir, restorejournal.Dir(stateDir), prev, filepath.Join(prev, "MANIFEST-000001"), db}
}

// TestP0aRejectsKeptDeleteEntries (05-storage §5S.3.15 rule (4); R1-21
// review M1, probe 1; verification review): an intent that reached the slot
// past Stage's checks lists every kept path among its delete entries. P0a's
// step a refuses it as Stage does (ValidateDelete): the journal says
// rejected, the intent is moved to rejected.json and health is degraded,
// and nothing is set aside or deleted.
func TestP0aRejectsKeptDeleteEntries(t *testing.T) {
	mem := vfs.NewCrashableMem()
	db := dbPathOf("outside")
	const id = "z-kept"
	makeStore(t, mem, db, 0, "live")
	live := treeHash(t, mem, db)
	aux := "/arkham/aux/ol"
	writeFile(t, mem, filepath.Join(aux, "000001.sst"), "penguin")
	in := restorejournal.Intent{ID: id, Kind: restorejournal.KindReset, Reset: restorejournal.ResetFactory, DBPath: db,
		Delete:        append(keptEntries(db, id), aux),
		StagedByBuild: testBuild, ExpiresAt: testNow().Add(time.Hour), RequestedBy: "alfred", RequestedAt: testNow()}
	for _, p := range []string{db, db + restorejournal.LevelsSuffix} {
		in.Targets = append(in.Targets, restorejournal.Target{From: p, To: restorejournal.SetAsideDest(stateDir, db, id, p)})
	}
	s := newSwapper(t, mem, testBuild, testNow)
	if err := s.Stage(context.Background(), in); !errors.Is(err, restorejournal.ErrDeleteRefused) {
		t.Fatalf("Stage = %v, want ErrDeleteRefused", err)
	}
	// Past Stage: the slot's create itself, as a writer that skipped the
	// check would make it.
	if err := mkdirAllSynced(mem, restorejournal.JournalDir(stateDir, id)); err != nil {
		t.Fatal(err)
	}
	if err := restorejournal.CreateIntentFS(mem, stateDir, in); err != nil {
		t.Fatal(err)
	}
	out, err := s.RunP0a(context.Background())
	if err != nil || out.Last != StepRejected || out.Degraded != DegradedRestoreRejected ||
		!strings.Contains(out.Reason, restorejournal.ErrDeleteRefused.Error()) {
		t.Fatalf("P0a = %+v, %v; want step a's rejection naming the refused delete entries", out, err)
	}
	if ok, err := exists(mem, s.rejectedPath(id)); err != nil || !ok {
		t.Fatalf("rejected.json: %v, %v; want the intent moved out of the slot", ok, err)
	}
	if got := treeHash(t, mem, db); got != live {
		t.Fatal("the live store changed: a rejected reset moved or deleted it")
	}
	if ok, err := exists(mem, restorejournal.SetAsideDest(stateDir, db, id, db)); err != nil || ok {
		t.Fatalf("a rejected reset set the store aside (%v)", err)
	}
	if _, ok := readFile(t, mem, filepath.Join(aux, "000001.sst")); !ok {
		t.Fatal("a rejected reset deleted its auxiliary entry")
	}
}

// TestResetDoneSkipsKeptPaths (05-storage §5S.3.15 rule (4); R1-21 review M1,
// probe 1): once the in-flight file exists nothing but ResetDone reads the
// delete entries again, so its own skip still guards them. A reset staged
// with one auxiliary entry is applied, and its in-flight intent is then
// rewritten to list every kept path and a relative entry as well. The
// kernel's completion removes none of the kept paths and records each as
// kept, records the relative entry as failed, removes the auxiliary entry,
// and the set-aside original (the only undo) and the fresh store survive.
func TestResetDoneSkipsKeptPaths(t *testing.T) {
	f := resetFixture(t, restorejournal.ResetFactory, "outside")
	id, db, aux := f.in.ID, f.in.DBPath, f.in.Delete[0]
	ctx := context.Background()
	s := newSwapper(t, f.mem, testBuild, testNow)
	if out, err := s.RunP0a(ctx); err != nil || out.Boot != BootReset {
		t.Fatalf("P0a = %+v, %v; want a reset boot", out, err)
	}
	// P4a's fresh install on the empty path, then applied.
	if err := mkdirAllSynced(f.mem, db); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(f.mem, filepath.Join(db, "fresh"), []byte("new install"), privateFile); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetApplied(ctx, id); err != nil {
		t.Fatal(err)
	}
	kept := keptEntries(db, id)
	in := f.in
	in.Delete = append(append(append([]string{}, kept...), "batcave-index"), aux)
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(f.mem, s.inflightPath(id), b, privateFile); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetDone(ctx, id); err != nil {
		t.Fatalf("ResetDone: %v", err)
	}
	recs, err := s.Journal(id)
	if err != nil {
		t.Fatal(err)
	}
	last := recs[len(recs)-1]
	if last.Step != StepHookDone || !slices.Equal(last.Kept, kept) || !slices.Equal(last.Failed, []string{"batcave-index"}) {
		t.Fatalf("hook_done = %+v, want kept %v and failed [batcave-index]", last, kept)
	}
	if got := treeHash(t, f.mem, restorejournal.SetAsideDest(stateDir, db, id, db)); got != f.live {
		t.Fatal("the set-aside original store changed: the reset's undo is gone")
	}
	if _, ok := readFile(t, f.mem, filepath.Join(db, "fresh")); !ok {
		t.Fatal("the fresh install is gone")
	}
	if ok, err := exists(f.mem, aux); err != nil || ok {
		t.Fatalf("the auxiliary delete entry is still there (%v)", err)
	}
}

// TestResetDoneOwedOnlyAfterApplied (R1-21 review M1): hook_done follows
// applied; a ResetDone call on a journal at set_aside (the fresh install not
// journaled) refuses, journals nothing and keeps the in-flight file.
func TestResetDoneOwedOnlyAfterApplied(t *testing.T) {
	f := resetFixture(t, restorejournal.ResetFactory, "outside")
	s := newSwapper(t, f.mem, testBuild, testNow)
	out, err := s.RunP0a(context.Background())
	if err != nil || out.Boot != BootReset || out.Last != StepSetAside {
		t.Fatalf("P0a = %+v, %v; want a reset boot at set_aside", out, err)
	}
	if err := s.ResetDone(context.Background(), f.in.ID); err == nil {
		t.Fatal("ResetDone at set_aside succeeded")
	}
	if last, err := s.last(f.in.ID); err != nil || last != StepSetAside {
		t.Fatalf("journal at %q (%v), want it left at set_aside", last, err)
	}
	if _, ok, err := InFlight(f.mem, stateDir); err != nil || !ok {
		t.Fatalf("in flight = %v, %v; want the in-flight file kept", ok, err)
	}
	if ok, _ := exists(f.mem, f.in.Delete[0]); !ok {
		t.Fatal("a delete entry was removed before applied")
	}
}

// TestStageRefusesDeleteEntries (05-storage §5S.3.15 "Stage"; R1-21 review M1,
// probe 1 on the real filesystem): a reset whose delete entry is the DB's
// parent, or any entry on the DB's filesystem, is refused before anything
// is written.
func TestStageRefusesDeleteEntries(t *testing.T) {
	h := newOSHost(t.TempDir())
	if err := os.MkdirAll(h.db, 0o700); err != nil {
		t.Fatal(err)
	}
	aux := filepath.Join(h.state, "batcave-index")
	if err := os.MkdirAll(aux, 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := New(Options{StateDir: h.state, BuildID: testBuild, Clock: testNow, Probe: restorejournal.OSProbe()})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range []string{filepath.Dir(h.db), aux} {
		in := h.intent(t, 0, "factory")
		in.Delete = []string{entry}
		if err := s.Stage(context.Background(), in); !errors.Is(err, restorejournal.ErrDeleteRefused) {
			t.Fatalf("delete %s: %v, want ErrDeleteRefused", entry, err)
		}
		if ok, _ := exists(vfs.Default, restorejournal.PendingPath(h.state)); ok {
			t.Fatalf("delete %s: a refused intent was written", entry)
		}
	}
}
