// file: core/internal/restoreswap/kept_test.go
// version: 1.0.0
// guid: 4e7c9d27-c099-4504-9295-f6fe874514bf
// last-edited: 2026-11-08

package restoreswap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// TestResetDoneSkipsKeptPaths (05-storage §5S.3.15 rule (4); R1-21 review M1,
// probe 1): an intent that reached the slot past Stage's checks lists the
// DB's parent, the state directory, restore/, the set-aside store and a
// file in it, and the live store among its delete entries. The kernel's
// completion removes none of them and records each as kept, records a
// relative entry as failed, removes the one auxiliary entry, and the
// set-aside original (the only undo) and the fresh store survive.
func TestResetDoneSkipsKeptPaths(t *testing.T) {
	mem := vfs.NewCrashableMem()
	db := dbPathOf("outside")
	const id = "z-kept"
	makeStore(t, mem, db, 0, "live")
	live := treeHash(t, mem, db)
	aux := "/arkham/aux/ol"
	writeFile(t, mem, filepath.Join(aux, "000001.sst"), "penguin")
	prev := restorejournal.SetAsideDest(stateDir, db, id, db)
	kept := []string{filepath.Dir(db), stateDir, restorejournal.Dir(stateDir), prev, filepath.Join(prev, "MANIFEST-000001"), db}
	in := restorejournal.Intent{ID: id, Kind: restorejournal.KindReset, Reset: restorejournal.ResetFactory, DBPath: db,
		Delete:        append(append([]string{}, kept...), "batcave-index", aux),
		StagedByBuild: testBuild, ExpiresAt: testNow().Add(time.Hour), RequestedBy: "alfred", RequestedAt: testNow()}
	for _, p := range []string{db, db + restorejournal.LevelsSuffix} {
		in.Targets = append(in.Targets, restorejournal.Target{From: p, To: restorejournal.SetAsideDest(stateDir, db, id, p)})
	}
	if err := newSwapper(t, mem, testBuild, testNow).Stage(context.Background(), in); !errors.Is(err, restorejournal.ErrDeleteRefused) {
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
	s := newSwapper(t, mem, testBuild, testNow)
	if done, err := fakeBoot(context.Background(), s, bootSucceeds); !done || err != nil {
		t.Fatalf("reset boot: %v, %v", done, err)
	}
	recs, err := s.Journal(id)
	if err != nil {
		t.Fatal(err)
	}
	last := recs[len(recs)-1]
	if last.Step != StepHookDone || !slices.Equal(last.Kept, kept) || !slices.Equal(last.Failed, []string{"batcave-index"}) {
		t.Fatalf("hook_done = %+v, want kept %v and failed [batcave-index]", last, kept)
	}
	if got := treeHash(t, mem, prev); got != live {
		t.Fatal("the set-aside original store changed: the reset's undo is gone")
	}
	if _, ok := readFile(t, mem, filepath.Join(db, "fresh")); !ok {
		t.Fatal("the fresh install is gone")
	}
	if ok, err := exists(mem, aux); err != nil || ok {
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
