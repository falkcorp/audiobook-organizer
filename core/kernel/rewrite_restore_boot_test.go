// file: core/kernel/rewrite_restore_boot_test.go
// version: 1.0.0
// guid: 6d1c0b7e-52a4-4f93-8e1a-7b3f9c20d5e8
// last-edited: 2026-11-20

package kernel

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/internal/restoreswap"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// restoreM8Mod is alfred with a version-2 step that records the restore
// journal's last record as it runs (the order probe) and M8 declared: the build
// that restores a store written before it existed, which then migrates.
//
// alsoRun, when set, runs inside that step: the failing test uses it to make the
// store's parent directory unwritable, which M8's copy needs and nothing the
// restore's own journal needs after P0a.
func restoreM8Mod(sawAtStep *string, h *swapHost, restoreID string, alsoRun func()) testModule {
	rw := storage.NewRewriteStep(storage.Owner("alfred"), "alfred.rewrite")
	observe := storage.Migration{Version: 2, Name: "observe", Compat: storage.Additive,
		Up: func(context.Context, *storage.MigrationTx) error {
			steps := stepsOf(h.journal(restoreID))
			*sawAtStep = steps[len(steps)-1]
			if alsoRun != nil {
				alsoRun()
			}
			return nil
		}}
	return storageMod("alfred", nil, seedStep(1, "cave"), observe).with(func(m *module.Manifest) { m.Rewrite = &rw })
}

// TestRestoreBoot_AppliedIsJournaledAfterTheMigrationsAndBeforeM8 (05-storage
// §5S.3.13 "M8", 05-backup §5B.3.4 step h): a staged restore of a store older
// than the build's M8 applies, runs the build's migrations, journals applied
// and only then runs M8, whose own swap refuses while a restore holds the one
// intent slot. Before this order the boot failed with ErrSwapPending and swapped
// the restore back, so a legacy archive could never be restored by a build that
// carries M8.
func TestRestoreBoot_AppliedIsJournaledAfterTheMigrationsAndBeforeM8(t *testing.T) {
	h := newSwapHost(t)
	h.close(h.mustBoot(h.config(nil)))
	h.stage("r-m8", restorejournal.KindArchive, h.stagedCopy("r-m8", "robin"))

	var sawAtStep string
	c := testConfig(t, restoreM8Mod(&sawAtStep, h, "r-m8", nil))
	c.StoragePath, c.StateDir = h.db, h.state
	k, err := h.boot(c)
	if err != nil {
		t.Fatalf("restore boot with M8 pending = %v, want it to apply, migrate and rewrite", err)
	}
	if sawAtStep != "opened" {
		t.Errorf("the restore journal read %q when the build's migration ran, want opened: applied is not journaled before the migrations", sawAtStep)
	}
	steps := stepsOf(h.journal("r-m8"))
	if steps[len(steps)-1] != "applied" {
		t.Errorf("restore journal %v, want it to end at applied", steps)
	}
	if _, ok, err := restoreswap.InFlight(vfs.Default, h.state); err != nil || ok {
		t.Errorf("in flight after the boot: %v %v", ok, err)
	}
	if state, err := k.store.db.RewriteRecord(h.ctx, "alfred.rewrite"); err != nil || state != storage.DataMigrationDone {
		t.Errorf("M8's record = %q, %v; want done", state, err)
	}
	ks, _ := k.store.db.Keyspace("alfred")
	if v, err := ks.Get(h.ctx, []byte("cave")); err != nil || string(v) != "robin" {
		t.Errorf("cave = %q, %v; want the restored store's robin", v, err)
	}
}

// TestRestoreBoot_FailedM8KeepsTheRestoreAndRetries: M8 fails after the
// restore is journaled applied. The boot fails and serves nothing; the restore is
// not swapped back (it is complete: applied, no in-flight restore file); the
// pre-restore store is still under restore/<id>/previous for a hand rollback;
// and the next boot resumes M8, finishes it and serves the restored data.
func TestRestoreBoot_FailedM8KeepsTheRestoreAndRetries(t *testing.T) {
	h := newSwapHost(t)
	h.close(h.mustBoot(h.config(nil)))
	h.stage("r-m8f", restorejournal.KindArchive, h.stagedCopy("r-m8f", "robin"))
	previous := restorejournal.SetAsideDest(h.state, h.db, "r-m8f", h.db)

	if os.Geteuid() == 0 {
		t.Skip("root writes through a read-only directory, so M8 cannot be made to fail this way")
	}
	storageDir := filepath.Dir(h.db)
	t.Cleanup(func() { _ = os.Chmod(storageDir, 0o700) })
	var saw string
	lockStorage := func() {
		if err := os.Chmod(storageDir, 0o500); err != nil {
			t.Error(err)
		}
	}
	c := testConfig(t, restoreM8Mod(&saw, h, "r-m8f", lockStorage))
	c.StoragePath, c.StateDir = h.db, h.state
	_, bootErr := h.boot(c)
	if bootErr == nil {
		t.Fatal("restore boot with a failing M8 succeeded, want the boot to fail")
	}
	if errors.Is(bootErr, restoreswap.ErrSwappedBack) {
		t.Fatalf("the failed M8 swapped the restore back: %v", bootErr)
	}
	steps := stepsOf(h.journal("r-m8f"))
	if steps[len(steps)-1] != "applied" {
		t.Fatalf("restore journal %v after M8 failed, want it to end at applied: the restore must not be swapped back", steps)
	}
	if _, err := os.Stat(previous); err != nil {
		t.Errorf("the pre-restore store %s is gone while M8 is not done: %v", previous, err)
	}

	if err := os.Chmod(storageDir, 0o700); err != nil {
		t.Fatal(err)
	}
	c = testConfig(t, restoreM8Mod(&saw, h, "r-m8f", nil))
	c.StoragePath, c.StateDir = h.db, h.state
	k, err := h.boot(c)
	if err != nil {
		t.Fatalf("the next boot = %v, want M8 resumed and the store served", err)
	}
	if state, err := k.store.db.RewriteRecord(h.ctx, "alfred.rewrite"); err != nil || state != storage.DataMigrationDone {
		t.Errorf("M8's record after the retry = %q, %v; want done", state, err)
	}
	ks, _ := k.store.db.Keyspace("alfred")
	if v, err := ks.Get(h.ctx, []byte("cave")); err != nil || string(v) != "robin" {
		t.Errorf("cave after the retry = %q, %v; want the restored store's robin", v, err)
	}
}
