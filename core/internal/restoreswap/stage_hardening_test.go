// file: core/internal/restoreswap/stage_hardening_test.go
// version: 1.0.0
// guid: a47d83fd-a048-4b46-beab-b333f3345a13
// last-edited: 2026-11-08

package restoreswap

import (
	"context"
	"errors"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/cockroachdb/pebble/v2/vfs/errorfs"

	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/internal/bootseam/bootseamtest"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// TestStageNeverReportsFailureForALiveIntent (R1-21 review m1): once link(2)
// put pending.json in the slot, Stage's result says what P0a will do. A
// directory fsync that fails after the link is undone (pending.json
// removed, the directory synced) and reported; one that cannot be undone
// leaves the intent staged and Stage succeeds; a journal write that fails
// after the create succeeds too, and P0a writes the staged record.
func TestStageNeverReportsFailureForALiveIntent(t *testing.T) {
	slot := restorejournal.Dir(stateDir)
	failSlotSyncs := func(times int) func(bootseamtest.Event) error {
		linked, failed := false, 0
		return func(ev bootseamtest.Event) error {
			switch {
			case ev.Kind == errorfs.OpLink:
				linked = true
			case linked && failed < times && ev.Kind == errorfs.OpFileSync && filepath.Clean(ev.Path) == slot:
				failed++
				return syscall.EIO
			}
			return nil
		}
	}
	stage := func(t *testing.T, fail func(bootseamtest.Event) error) (*vfs.MemFS, restorejournal.Intent, error) {
		f := restoreSetup(t, restorejournal.KindArchive, "outside", false)
		rec := bootseamtest.New(f.mem, bootseamtest.Options{Fail: fail})
		err := newSwapper(t, rec.FS(), testBuild, testNow).Stage(bootseam.With(context.Background(), rec.Seams()), f.in)
		return f.mem, f.in, err
	}
	t.Run("undone", func(t *testing.T) {
		mem, in, err := stage(t, failSlotSyncs(1))
		if !errors.Is(err, syscall.EIO) {
			t.Fatalf("Stage = %v, want the fsync's EIO", err)
		}
		if ok, _ := exists(mem, restorejournal.PendingPath(stateDir)); ok {
			t.Fatal("Stage failed and left pending.json for P0a to apply")
		}
		if ok, _ := exists(mem, restorejournal.JournalDir(stateDir, in.ID)); ok {
			t.Fatal("Stage failed and left its journal directory")
		}
	})
	t.Run("cannot-undo", func(t *testing.T) {
		mem, in, err := stage(t, failSlotSyncs(2))
		if err != nil {
			t.Fatalf("Stage = %v, want success: the intent is in the slot", err)
		}
		if ok, _ := exists(mem, restorejournal.PendingPath(stateDir)); !ok {
			t.Fatal("no pending.json")
		}
		if recs, err := ReadJournal(mem, stateDir, in.ID); err != nil || lastStep(recs) != StepStaged {
			t.Fatalf("journal %v, %v; want staged", recs, err)
		}
	})
	t.Run("journal-write-fails", func(t *testing.T) {
		mem, in, err := stage(t, func(ev bootseamtest.Event) error {
			if ev.Kind == errorfs.OpCreate && filepath.Base(ev.Path) == restorejournal.JournalName {
				return syscall.ENOSPC
			}
			return nil
		})
		if err != nil {
			t.Fatalf("Stage = %v, want success: the intent is in the slot and P0a writes its staged record", err)
		}
		if recs, err := ReadJournal(mem, stateDir, in.ID); err != nil || len(recs) != 0 {
			t.Fatalf("journal %v, %v; want none yet", recs, err)
		}
		out, err := newSwapper(t, mem, testBuild, testNow).RunP0a(context.Background())
		if err != nil || out.Boot != BootRestore {
			t.Fatalf("P0a = %+v, %v; want the restore applied", out, err)
		}
		if recs, err := ReadJournal(mem, stateDir, in.ID); err != nil || recs[0].Step != StepStaged {
			t.Fatalf("journal %v, %v; want it to start at staged", recs, err)
		}
	})
}

// TestStageRefusesTargetShapes (R1-21 review m4, m5): a restore must place
// something at its DB path and a reset must set its DB path aside, and no
// target is, holds or lies inside restore/, holds the state directory, or
// is, holds or lies inside a swap's private directory. Nothing is staged.
func TestStageRefusesTargetShapes(t *testing.T) {
	ctx := context.Background()
	refused := func(t *testing.T, mem vfs.FS, in restorejournal.Intent) {
		t.Helper()
		if err := newSwapper(t, mem, testBuild, testNow).Stage(ctx, in); !errors.Is(err, restorejournal.ErrTargetsRefused) {
			t.Fatalf("Stage = %v, want ErrTargetsRefused", err)
		}
		if ok, _ := exists(mem, restorejournal.PendingPath(stateDir)); ok {
			t.Fatal("a refused intent was staged")
		}
	}
	restoreWith := func(t *testing.T, to ...string) (*vfs.MemFS, restorejournal.Intent) {
		f := restoreSetup(t, restorejournal.KindArchive, "outside", false)
		staged := f.in.Targets[0].From
		f.in.Targets = nil
		for i, p := range to {
			from := staged
			if i > 0 {
				from = filepath.Join(filepath.Dir(staged), filepath.Base(p))
				writeFile(t, f.mem, from, "extra")
			}
			f.in.Targets = append(f.in.Targets, restorejournal.Target{From: from, To: p})
		}
		return f.mem, f.in
	}
	db := dbPathOf("outside")
	t.Run("restore-places-nothing-at-db", func(t *testing.T) {
		mem, in := restoreWith(t, db+".old")
		refused(t, mem, in)
	})
	t.Run("restore-target-holds-state-dir", func(t *testing.T) {
		mem, in := restoreWith(t, db, filepath.Dir(stateDir))
		refused(t, mem, in)
	})
	t.Run("restore-target-in-restore-dir", func(t *testing.T) {
		mem, in := restoreWith(t, db, filepath.Join(restorejournal.Dir(stateDir), "joker.json"))
		refused(t, mem, in)
	})
	t.Run("restore-target-in-aside-root", func(t *testing.T) {
		mem, in := restoreWith(t, db, filepath.Join(filepath.Dir(db), ".mediacore-restore", "r-older", "previous", "x"))
		refused(t, mem, in)
	})
	t.Run("reset-leaves-db", func(t *testing.T) {
		f := resetSetup(t, restorejournal.ResetStore, "outside")
		f.in.Targets = f.in.Targets[1:] // the store itself not set aside
		refused(t, f.mem, f.in)
	})
	t.Run("reset-target-holds-state-dir", func(t *testing.T) {
		f := resetSetup(t, restorejournal.ResetStore, "outside")
		p := filepath.Dir(stateDir)
		f.in.Targets = append(f.in.Targets, restorejournal.Target{From: p, To: restorejournal.SetAsideDest(stateDir, f.in.DBPath, f.in.ID, p)})
		refused(t, f.mem, f.in)
	})
}

// TestP0aRejectsTargetShapes (R1-21 review m4, m5): an intent a stager wrote
// into the slot without Stage, whose targets Stage would refuse, is
// rejected at P0a's step a. Nothing moves, the store and the state
// directory stand, and the boot is degraded restore_rejected.
func TestP0aRejectsTargetShapes(t *testing.T) {
	holdsState := func() fixture {
		f := resetSetup(t, restorejournal.ResetStore, "outside")
		p := filepath.Dir(stateDir)
		f.in.Targets = append(f.in.Targets, restorejournal.Target{From: p, To: restorejournal.SetAsideDest(stateDir, f.in.DBPath, f.in.ID, p)})
		return f
	}
	nothingAtDB := func() fixture {
		f := restoreSetup(t, restorejournal.KindArchive, "outside", false)
		f.in.Targets[0].To = f.in.DBPath + ".old"
		return f
	}
	for name, mk := range map[string]func() fixture{"reset-holds-state-dir": holdsState, "restore-places-nothing-at-db": nothingAtDB} {
		t.Run(name, func(t *testing.T) {
			f := mk()
			if err := mkdirAllSynced(f.mem, restorejournal.JournalDir(stateDir, f.in.ID)); err != nil {
				t.Fatal(err)
			}
			if err := restorejournal.CreateIntentFS(f.mem, stateDir, f.in); err != nil {
				t.Fatal(err)
			}
			out, err := newSwapper(t, f.mem, testBuild, testNow).RunP0a(context.Background())
			if err != nil || out.Last != StepRejected || out.Degraded != DegradedRestoreRejected {
				t.Fatalf("P0a = %+v, %v; want a rejection, degraded %s", out, err, DegradedRestoreRejected)
			}
			if got := treeHash(t, f.mem, f.in.DBPath); got != f.live {
				t.Fatal("the store changed under a rejected intent")
			}
			if ok, _ := exists(f.mem, restorejournal.PendingPath(stateDir)); ok {
				t.Fatal("pending.json is still in the slot")
			}
			if ok, _ := exists(f.mem, filepath.Join(restorejournal.JournalDir(stateDir, f.in.ID), restorejournal.RejectedName)); !ok {
				t.Fatal("no rejected.json")
			}
		})
	}
}
