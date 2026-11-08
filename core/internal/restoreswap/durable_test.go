// file: core/internal/restoreswap/durable_test.go
// version: 1.0.0
// guid: 849b3691-9d70-4ed4-a14a-5b121e9a9bb5
// last-edited: 2026-11-08

package restoreswap

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/cockroachdb/pebble/v2/vfs/errorfs"

	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// The tests below are R1-21 review M6's: the crash matrix's nested faults
// (an injected error, then a kill in a later boot) found states an earlier
// boot made visible without making durable, which the next boot trusted. A
// step found done is not known durable: it is synced again before anything
// is built on it.

// crashed is fs after a crash: only what was synced.
func crashed(fs *vfs.MemFS) *vfs.MemFS { return fs.CrashClone(vfs.CrashCloneCfg{}) }

// syncAll syncs every file and directory of fs, so a fixture is durable
// before the boot under test runs and a crash clone shows only that boot's
// unsynced work missing.
func syncAll(t *testing.T, fs *vfs.MemFS, dir string) {
	t.Helper()
	names, err := fs.List(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		p := filepath.Join(dir, n)
		fi, err := fs.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.IsDir() {
			syncAll(t, fs, p)
			continue
		}
		f, err := fs.Open(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := errors.Join(f.Sync(), f.Close()); err != nil {
			t.Fatal(err)
		}
	}
	if err := syncDir(fs, dir); err != nil {
		t.Fatal(err)
	}
}

// failOnce is fs wrapping mem, where the first operation fail accepts
// returns EIO and does not run.
func failOnce(mem *vfs.MemFS, fail func(errorfs.Op) bool) (vfs.FS, func() bool) {
	var mu sync.Mutex
	hit := false
	fs := errorfs.Wrap(mem, errorfs.InjectorFunc(func(op errorfs.Op) error {
		mu.Lock()
		defer mu.Unlock()
		if hit || !fail(op) {
			return nil
		}
		hit = true
		return syscall.EIO
	}))
	return fs, func() bool { mu.Lock(); defer mu.Unlock(); return hit }
}

// TestFoundDoneOperationsAreMadeDurable: a rename, slot rename or removal
// an earlier boot made, whose directory fsyncs failed, is found done by the
// next boot's idempotent step, which syncs its directories again.
func TestFoundDoneOperationsAreMadeDurable(t *testing.T) {
	setup := func(t *testing.T) *vfs.MemFS {
		mem := vfs.NewCrashableMem()
		for _, d := range []string{"/cave/a", "/cave/b"} {
			if err := mkdirAllSynced(mem, d); err != nil {
				t.Fatal(err)
			}
		}
		writeFile(t, mem, "/cave/a/batarang", "robin")
		return mem
	}
	for name, step := range map[string]func(vfs.FS) error{
		"mv":       func(fs vfs.FS) error { return mv(fs, "/cave/a/batarang", "/cave/b/batarang", memGhost) },
		"slotMove": func(fs vfs.FS) error { return slotMove(fs, "/cave/a/batarang", "/cave/b/batarang") },
	} {
		t.Run(name, func(t *testing.T) {
			mem := setup(t)
			if err := mem.Rename("/cave/a/batarang", "/cave/b/batarang"); err != nil {
				t.Fatal(err)
			}
			if err := step(mem); err != nil {
				t.Fatal(err)
			}
			c := crashed(mem)
			if ok, _ := exists(c, "/cave/a/batarang"); ok {
				t.Fatal("after a crash the item is back at its source: the rename found done was not synced")
			}
			if ok, _ := exists(c, "/cave/b/batarang"); !ok {
				t.Fatal("after a crash the item is not at its destination")
			}
		})
	}
	t.Run("removeSynced", func(t *testing.T) {
		mem := setup(t)
		if err := mem.Remove("/cave/a/batarang"); err != nil {
			t.Fatal(err)
		}
		if err := removeSynced(mem, "/cave/a/batarang"); err != nil {
			t.Fatal(err)
		}
		if ok, _ := exists(crashed(mem), "/cave/a/batarang"); ok {
			t.Fatal("after a crash the removed file is back: the removal found done was not synced")
		}
	})
}

// TestRenameUndoneWhenItsDestinationSyncFails: a rename whose destination
// directory fsync fails is in neither directory durably, and any later sync
// of the source's directory (another item's step, a chain sync) makes the
// item durable nowhere. The matrix found it on a reset whose set-aside of
// the store failed so: the move-back's next step synced the store's live
// directory first. So the rename is undone before the error returns, and
// what is visible is again what is durable.
func TestRenameUndoneWhenItsDestinationSyncFails(t *testing.T) {
	for name, step := range map[string]func(vfs.FS) error{
		"mv":       func(fs vfs.FS) error { return mv(fs, "/cave/a/batarang", "/cave/b/batarang", memGhost) },
		"slotMove": func(fs vfs.FS) error { return slotMove(fs, "/cave/a/batarang", "/cave/b/batarang") },
	} {
		t.Run(name, func(t *testing.T) {
			mem := vfs.NewCrashableMem()
			for _, d := range []string{"/cave/a", "/cave/b"} {
				if err := mkdirAllSynced(mem, d); err != nil {
					t.Fatal(err)
				}
			}
			writeFile(t, mem, "/cave/a/batarang", "robin")
			fs, hit := failOnce(mem, func(op errorfs.Op) bool {
				return op.Kind == errorfs.OpFileSync && filepath.Clean(op.Path) == "/cave/b"
			})
			if err := step(fs); err == nil || !hit() {
				t.Fatalf("%s = %v (fault hit: %v); want the destination fsync's error", name, err, hit())
			}
			if ok, _ := exists(mem, "/cave/a/batarang"); !ok {
				t.Fatal("the rename was left standing after its destination fsync failed")
			}
			// Another step syncs the source's directory.
			if err := syncDir(mem, "/cave/a"); err != nil {
				t.Fatal(err)
			}
			if ok, _ := exists(crashed(mem), "/cave/a/batarang"); !ok {
				t.Fatal("after a crash the item is in neither directory")
			}
		})
	}
}

// TestSwapBackSyncsTheFailedChainItFinds: step x's move of the restored
// store to failed/ creates its directories and syncs each one's parent.
// When the first of those syncs fails, x halts with the directories present
// and not durable; the next boot's x finds them, renames the store into
// them and journals swapped_back. A crash after that must keep the restored
// store in failed/, so x syncs the chain it finds before the rename.
func TestSwapBackSyncsTheFailedChainItFinds(t *testing.T) {
	for _, layout := range []string{"inside", "outside"} {
		t.Run(layout, func(t *testing.T) {
			f := restoreFixture(t, restorejournal.KindArchive, layout, false)
			syncAll(t, f.mem, "/")
			ctx := context.Background()
			db, id := f.in.DBPath, f.in.ID
			s := newSwapper(t, f.mem, testBuild, testNow)
			if out, err := s.RunP0a(ctx); err != nil || out.Boot != BootRestore {
				t.Fatalf("P0a = %+v, %v; want a restore boot", out, err)
			}
			if err := s.Opened(ctx, id); err != nil {
				t.Fatal(err)
			}
			failed := restorejournal.FailedDest(stateDir, db, id, db)
			tree := filepath.Dir(failed)
			for filepath.Base(tree) != "failed" {
				tree = filepath.Dir(tree)
			}
			made := false
			fs, hit := failOnce(f.mem, func(op errorfs.Op) bool {
				if op.Kind == errorfs.OpMkdirAll && restorejournal.InTree(filepath.Clean(op.Path), tree) {
					made = true
				}
				return made && op.Kind == errorfs.OpFileSync
			})
			cause := errors.New("P4b refused the restored store")
			if err := newSwapper(t, fs, testBuild, testNow).SwapBack(ctx, id, cause); err == nil || errors.Is(err, ErrSwappedBack) || !hit() {
				t.Fatalf("SwapBack = %v (fault hit: %v); want x halted by the failed fsync", err, hit())
			}
			out, err := newSwapper(t, f.mem, testBuild, testNow).RunP0a(ctx)
			if err != nil || out.Last != StepSwappedBack {
				t.Fatalf("the next P0a = %+v, %v; want x finished", out, err)
			}
			c := crashed(f.mem)
			if ok, _ := exists(c, failed); !ok {
				t.Fatalf("after a crash the restored store is gone from %s: x renamed it into a chain it never synced", failed)
			}
			if got := treeHash(t, c, failed); got != f.staged {
				t.Fatal("after a crash failed/ does not hold the restored store")
			}
			if got := treeHash(t, c, db); got != f.live {
				t.Fatal("after a crash the original is not at the DB path")
			}
		})
	}
}

// staleFixture is a restore P0a rejects at step a: its intent expired.
func staleFixture(t *testing.T) fixture {
	t.Helper()
	f := restoreSetup(t, restorejournal.KindArchive, "outside", false)
	f.in.RequestedAt, f.in.ExpiresAt = testNow().Add(-2*time.Hour), testNow().Add(-time.Hour)
	if err := newSwapper(t, f.mem, testBuild, func() time.Time { return testNow().Add(-3 * time.Hour) }).Stage(context.Background(), f.in); err != nil {
		t.Fatal(err)
	}
	syncAll(t, f.mem, "/")
	return f
}

// TestP0aMakesTheJournalDurableBeforeActing: a record whose fsync failed is
// visible to the next boot and not durable. That boot acts on what it read
// (here: the rejected record, then pending.json renamed to rejected.json),
// so it first makes the journal durable: after a crash the rename never
// stands without its record.
func TestP0aMakesTheJournalDurableBeforeActing(t *testing.T) {
	f := staleFixture(t)
	ctx := context.Background()
	journal := journalPath(stateDir, f.in.ID)
	fs, hit := failOnce(f.mem, func(op errorfs.Op) bool {
		return op.Kind == errorfs.OpFileSync && filepath.Clean(op.Path) == journal
	})
	if _, err := newSwapper(t, fs, testBuild, testNow).RunP0a(ctx); err == nil || !hit() {
		t.Fatalf("P0a = %v (fault hit: %v); want the boot halted by the rejected record's fsync", err, hit())
	}
	if out, err := newSwapper(t, f.mem, testBuild, testNow).RunP0a(ctx); err != nil || out.Last != StepRejected {
		t.Fatalf("the next P0a = %+v, %v; want the intent rejected", out, err)
	}
	c := crashed(f.mem)
	recs, err := ReadJournal(c, stateDir, f.in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := exists(c, filepath.Join(restorejournal.JournalDir(stateDir, f.in.ID), restorejournal.RejectedName)); !ok {
		t.Fatal("after a crash rejected.json is gone: the slot rename was not synced")
	}
	if lastStep(recs) != StepRejected {
		t.Fatalf("after a crash rejected.json stands beside journal %v: the boot acted on a record it never made durable", steps(recs))
	}
}

// TestP0aRepairsARejectionWithoutItsRecord: rejected.json in the slot is
// the durable mark of a rejection; when the journal lacks the rejected
// record P0a writes it, so the journal ends terminal.
func TestP0aRepairsARejectionWithoutItsRecord(t *testing.T) {
	f := staleFixture(t)
	pending, _ := readFile(t, f.mem, restorejournal.PendingPath(stateDir))
	writeFile(t, f.mem, filepath.Join(restorejournal.JournalDir(stateDir, f.in.ID), restorejournal.RejectedName), pending)
	out, err := newSwapper(t, f.mem, testBuild, testNow).RunP0a(context.Background())
	if err != nil || out.Last != StepRejected {
		t.Fatalf("P0a = %+v, %v; want the rejection finished", out, err)
	}
	recs, err := ReadJournal(f.mem, stateDir, f.in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(steps(recs), []string{StepStaged, StepRejected}) {
		t.Fatalf("journal %v, want staged then rejected", steps(recs))
	}
	if ok, _ := exists(f.mem, restorejournal.PendingPath(stateDir)); ok {
		t.Fatal("pending.json is still in the slot")
	}
}
