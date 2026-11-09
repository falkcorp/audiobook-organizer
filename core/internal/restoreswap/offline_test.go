// file: core/internal/restoreswap/offline_test.go
// version: 1.0.0
// guid: 6b0f3c2e-8d4a-4e71-9a5c-2f17d84be903
// last-edited: 2026-11-08

package restoreswap

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// TestStageResetRecordsThePlan (05-storage §5S.3.15 "Stage"): StageReset
// records the reset's set-aside targets (ResetTargets), the hook's plan as
// hook_args and hook_paths, the wipe flag and the live install ID, and
// stages the one intent; a store reset that carries a plan, a hook path
// that is not absolute and an unknown kind are refused with nothing staged.
func TestStageResetRecordsThePlan(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		spec ResetSpec
	}{
		{"store reset with a plan", ResetSpec{Reset: restorejournal.ResetStore, Plan: storage.ResetPlan{Paths: []string{"/arkham/ol"}}}},
		{"store reset with the wipe", ResetSpec{Reset: restorejournal.ResetStore, DeleteLibraryFiles: true}},
		{"relative hook path", ResetSpec{Reset: restorejournal.ResetFactory, Plan: storage.ResetPlan{Paths: []string{"arkham/ol"}}}},
		{"unknown kind", ResetSpec{Reset: "joker"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mem := vfs.NewMem()
			db := dbPathOf("outside")
			makeStore(t, mem, db, 0, "live")
			s := newSwapper(t, mem, testBuild, testNow)
			tc.spec.DBPath = db
			if _, err := s.StageReset(ctx, tc.spec); err == nil {
				t.Fatal("StageReset succeeded")
			}
			if st, err := restorejournal.ReadSlot(mem, stateDir); err != nil || st.Pending != nil {
				t.Fatalf("slot after a refused reset = %+v, %v; want nothing staged", st, err)
			}
		})
	}

	mem := vfs.NewMem()
	db := dbPathOf("outside")
	makeStore(t, mem, db, 0, "live")
	writeFile(t, mem, filepath.Join(stateDir, configBootRecord), `{"owner":"bruce"}`)
	s := newSwapper(t, mem, testBuild, testNow)
	plan := storage.ResetPlan{Hook: []byte(`{"dump_dir":"/arkham/ol"}`), Paths: []string{"/arkham/ol"}, LibraryRoot: "/arkham/library", LibraryFiles: 2}
	in, err := s.StageReset(ctx, ResetSpec{DBPath: db, Reset: restorejournal.ResetFactory, DeleteLibraryFiles: true,
		Plan: plan, InstallID: "wayne-manor", RequestedBy: "alfred"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(in.ID, testNow().UTC().Format("20060102")) {
		t.Errorf("intent ID %q, want one NewID made from the clock", in.ID)
	}
	want := ResetTargets(stateDir, db, in.ID, s.legacySuffixes(), nil)
	if !slices.Equal(in.Targets, want) || string(in.HookArgs) != string(plan.Hook) || !slices.Equal(in.HookPaths, plan.Paths) ||
		!in.DeleteLibraryFiles || in.InstallID != "wayne-manor" || in.StagedByBuild != testBuild || in.RequestedBy != "alfred" {
		t.Fatalf("intent %+v, want the reset's targets %v, the plan, the wipe and the install ID", in, want)
	}
	if st, err := restorejournal.ReadSlot(mem, stateDir); err != nil || st.Pending == nil || st.Pending.ID != in.ID {
		t.Fatalf("slot = %+v, %v; want %s pending", st, err, in.ID)
	}
	if _, err := s.StageReset(ctx, ResetSpec{DBPath: db, Reset: restorejournal.ResetStore}); !errors.Is(err, restorejournal.ErrSwapPending) {
		t.Fatalf("a second reset while one is staged = %v, want ErrSwapPending", err)
	}
}

// TestResetHookJournalSide (05-storage §5S.3.15 "The app's reset hook"): on
// an applied ResetFactory, HookIntent returns the in-flight intent and
// HookDone journals hook_done with the hook's failed and kept paths and
// deletes the in-flight file; HookDone again is a no-op, and HookIntent on
// a restore journal is refused.
func TestResetHookJournalSide(t *testing.T) {
	ctx := context.Background()
	f := resetFixture(t, restorejournal.ResetFactory, "outside")
	s := newSwapper(t, f.mem, testBuild, testNow)
	out, err := s.RunP0a(ctx)
	if err != nil || out.Boot != BootReset {
		t.Fatalf("P0a = %+v, %v; want the reset applied", out, err)
	}
	if err := mkdirAllSynced(s.fs, f.in.DBPath); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetApplied(ctx, out.ID); err != nil {
		t.Fatal(err)
	}
	in, err := s.HookIntent(out.ID)
	if err != nil || in.ID != f.in.ID || in.Kind != restorejournal.KindReset {
		t.Fatalf("HookIntent = %+v, %v; want the applied reset", in, err)
	}
	if err := s.HookDone(ctx, out.ID, []string{"/arkham/ol: scarecrow"}, []string{filepath.Dir(f.in.DBPath)}); err != nil {
		t.Fatal(err)
	}
	recs, err := ReadJournal(f.mem, stateDir, out.ID)
	if err != nil {
		t.Fatal(err)
	}
	last := recs[len(recs)-1]
	if last.Step != StepHookDone || !slices.Equal(last.Failed, []string{"/arkham/ol: scarecrow"}) || !slices.Equal(last.Kept, []string{filepath.Dir(f.in.DBPath)}) {
		t.Fatalf("last record %+v, want hook_done with the hook's failed and kept", last)
	}
	if ok, _ := exists(f.mem, s.inflightPath(out.ID)); ok {
		t.Fatal("the in-flight file survived hook_done")
	}
	if err := s.HookDone(ctx, out.ID, []string{"riddler"}, nil); err != nil {
		t.Fatalf("HookDone again = %v, want a no-op", err)
	}
	if again, _ := ReadJournal(f.mem, stateDir, out.ID); len(again) != len(recs) {
		t.Fatalf("journal after a second HookDone %v, want it as the first left it %v", steps(again), steps(recs))
	}
	if _, err := s.HookIntent(out.ID); err == nil {
		t.Fatal("HookIntent after hook_done succeeded")
	}

	r := restoreFixture(t, restorejournal.KindArchive, "outside", false)
	rs := newSwapper(t, r.mem, testBuild, testNow)
	if out, err := rs.RunP0a(ctx); err != nil || out.Boot != BootRestore {
		t.Fatalf("restore P0a = %+v, %v", out, err)
	}
	if _, err := rs.HookIntent(r.in.ID); err == nil {
		t.Fatal("HookIntent on a restore journal succeeded")
	}
}

// TestRestoreOfflineInPlace (05-storage §5S.3.12 "In place"): the offline
// restore places the source store over the live one in this process and
// finishes its own journal at applied_offline with no in-flight file left;
// a pending intent is rejected as superseded first; a source of another
// kind is refused; a swap in flight refuses.
func TestRestoreOfflineInPlace(t *testing.T) {
	ctx := context.Background()
	mem := vfs.NewMem()
	db := dbPathOf("outside")
	makeStore(t, mem, db, 0, "live")
	if err := mem.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	s := newSwapper(t, mem, testBuild, testNow)
	if _, err := s.RestoreOffline(ctx, db, "alfred", OfflineSource{Kind: restorejournal.KindReset}); err == nil {
		t.Fatal("an offline restore of a reset source succeeded")
	}
	pending, err := s.StageReset(ctx, ResetSpec{DBPath: db, Reset: restorejournal.ResetStore, InstallID: "wayne-manor"})
	if err != nil {
		t.Fatal(err)
	}
	const id = "20261108T000000Z-cave"
	src := filepath.Join(restorejournal.StagingDir(db, id), "db")
	makeStore(t, mem, src, 1, "checkpoint")
	want := treeHash(t, mem, src)
	res, err := s.RestoreOffline(ctx, db, "alfred", OfflineSource{ID: id, Kind: restorejournal.KindCheckpoint, Store: src,
		HooksOwed: true, InstallID: "wayne-manor"})
	if err != nil {
		t.Fatal(err)
	}
	if res.ID != id || res.Escaped || res.Superseded != pending.ID {
		t.Fatalf("result %+v, want journal %s with %s superseded", res, id, pending.ID)
	}
	if got := treeHash(t, mem, db); got != want {
		t.Fatal("the live store is not the source after the offline restore")
	}
	recs, err := ReadJournal(mem, stateDir, id)
	if err != nil {
		t.Fatal(err)
	}
	if last := recs[len(recs)-1]; last.Step != StepAppliedOffline || !last.HooksOwed {
		t.Fatalf("journal %v (last %+v), want it finished at applied_offline with hooks owed", steps(recs), last)
	}
	rej, err := ReadJournal(mem, stateDir, pending.ID)
	if err != nil {
		t.Fatal(err)
	}
	if last := rej[len(rej)-1]; last.Step != StepRejected || last.Note != supersededNote {
		t.Fatalf("superseded journal %v (last %+v), want rejected %q", steps(rej), last, supersededNote)
	}
	st, err := restorejournal.ReadSlot(mem, stateDir)
	if err != nil || st.Pending != nil || len(st.InFlight) != 0 {
		t.Fatalf("slot after the offline restore = %+v, %v; want it empty", st, err)
	}

	// A swap in flight that is not a failed checkpoint restore refuses.
	f := restoreFixture(t, restorejournal.KindArchive, "outside", false)
	fs := newSwapper(t, f.mem, testBuild, testNow)
	if err := f.mem.MkdirAll(fs.journalDir(f.in.ID), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := f.mem.Rename(fs.pendingPath(), fs.inflightPath(f.in.ID)); err != nil {
		t.Fatal(err)
	}
	src2 := filepath.Join(restorejournal.StagingDir(f.in.DBPath, "r2"), "db")
	makeStore(t, f.mem, src2, 1, "checkpoint")
	if _, err := fs.RestoreOffline(ctx, f.in.DBPath, "alfred", OfflineSource{ID: "r2", Kind: restorejournal.KindCheckpoint, Store: src2}); !errors.Is(err, restorejournal.ErrSwapPending) {
		t.Fatalf("an offline restore with an archive swap in flight = %v, want ErrSwapPending", err)
	}
	if _, err := s.RestoreOffline(ctx, "relative/main.pebble", "alfred", OfflineSource{Kind: restorejournal.KindCheckpoint, Store: src}); err == nil {
		t.Fatal("an offline restore to a relative store path succeeded")
	}
}

// TestRestoreOfflineEscapesAFailedCheckpointRestore (05-storage §5S.3.12
// "In place", the escape): a KindCheckpoint journal left at failed by
// another binary is finished by a checkpoint source: the source is placed,
// the journal finishes at applied_offline with hooks owed, and the slot is
// empty; the binary whose boot failed is refused
// (ErrCheckpointRestoreFailedHere), and an archive source never escapes.
func TestRestoreOfflineEscapesAFailedCheckpointRestore(t *testing.T) {
	ctx := context.Background()
	f := restoreFixture(t, restorejournal.KindCheckpoint, "outside", false)
	// The restore boot: P0a swaps the checkpoint in, the store opens, and
	// the migration DAG fails on it (CheckpointFailed keeps the in-flight
	// file).
	boot := newSwapper(t, f.mem, testBuild, testNow)
	out, err := boot.RunP0a(ctx)
	if err != nil || out.Boot != BootRestore {
		t.Fatalf("P0a = %+v, %v", out, err)
	}
	if err := boot.Opened(ctx, out.ID); err != nil {
		t.Fatal(err)
	}
	if err := boot.CheckpointFailed(ctx, out.ID); err != nil {
		t.Fatal(err)
	}
	recs, err := ReadJournal(f.mem, stateDir, f.in.ID)
	if err != nil || recs[len(recs)-1].Step != StepFailed {
		t.Fatalf("journal %v, %v; want the checkpoint restore at failed", steps(recs), err)
	}
	src := filepath.Join(restorejournal.StagingDir(f.in.DBPath, "escape"), "db")
	makeStore(t, f.mem, src, 0, "previous-build-checkpoint")
	want := treeHash(t, f.mem, src)
	cp := OfflineSource{Kind: restorejournal.KindCheckpoint, Store: src, HooksOwed: true}

	if _, err := newSwapper(t, f.mem, testBuild, testNow).RestoreOffline(ctx, f.in.DBPath, "alfred", cp); !errors.Is(err, ErrCheckpointRestoreFailedHere) {
		t.Fatalf("the escape by the binary whose boot failed = %v, want ErrCheckpointRestoreFailedHere", err)
	}
	prev := newSwapper(t, f.mem, "batcomputer-r0", testNow)
	if _, err := prev.RestoreOffline(ctx, f.in.DBPath, "alfred", OfflineSource{Kind: restorejournal.KindArchive, Store: src}); !errors.Is(err, restorejournal.ErrSwapPending) {
		t.Fatalf("an archive source over a failed checkpoint restore = %v, want ErrSwapPending", err)
	}
	res, err := prev.RestoreOffline(ctx, f.in.DBPath, "alfred", cp)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Escaped || res.ID != f.in.ID {
		t.Fatalf("result %+v, want the failed journal %s escaped", res, f.in.ID)
	}
	if got := treeHash(t, f.mem, f.in.DBPath); got != want {
		t.Fatal("the live store is not the escape's source")
	}
	recs, err = ReadJournal(f.mem, stateDir, f.in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if last := recs[len(recs)-1]; last.Step != StepAppliedOffline || !last.HooksOwed {
		t.Fatalf("journal %v (last %+v), want it finished at applied_offline with hooks owed", steps(recs), last)
	}
	if st, err := restorejournal.ReadSlot(f.mem, stateDir); err != nil || st.Busy() {
		t.Fatalf("slot after the escape = %+v, %v; want it empty", st, err)
	}
}
