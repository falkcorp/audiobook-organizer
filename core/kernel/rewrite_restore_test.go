// file: core/kernel/rewrite_restore_test.go
// version: 1.0.0
// guid: fb59c561-9bd5-4d96-9ad9-833ccb66559c
// last-edited: 2026-11-11

package kernel

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/cockroachdb/pebble/v2/vfs/errorfs"

	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/internal/restoreswap"
	"github.com/falkcorp/mediacore/storage"
)

// m8Boot boots the host's store as the kernel's P2-P4b would with alfred's
// M8 (alfred.rewrite) as its one data step, run by a Swapper over fs, and
// closes it: M8's runs and the boot's error. Boot does not run data steps
// before F2-16, so this opens the store with the kernel's own options.
func (h *swapHost) m8Boot(fs vfs.FS) (runs int, err error) {
	h.t.Helper()
	c := h.keyed(h.config(nil))
	c.StorageFS = storage.FSOf(fs)
	k, err := offlineKernel(c)
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() { err = errors.Join(err, k.closeOffline(context.Background())) }()
	ctx := bootseam.With(h.t.Context(), &bootseam.Seams{FS: fs, BuildID: swapBuild})
	opts, err := k.storageOptions(ctx)
	if err != nil {
		h.t.Fatal(err)
	}
	rw := storage.NewRewriteStep("alfred", "alfred.rewrite")
	sw, err := restoreswap.New(restoreswap.Options{FS: fs, StateDir: h.state, BuildID: opts.Build, Owners: opts.Owners, RewriteStep: rw.ID})
	if err != nil {
		h.t.Fatal(err)
	}
	opts.FreeSpaceMargin = 1
	opts.DataSteps = []storage.DataStep{{Owner: rw.Owner, ID: rw.ID, Cutover: true, Rewrite: true, Run: func(ctx context.Context, db *storage.DB) error {
		runs++
		return sw.RunRewrite(ctx, db, rw)
	}}}
	o, err := storage.Open(ctx, opts)
	if err != nil {
		return runs, err
	}
	db, err := o.Prepare(ctx)
	if err != nil {
		return runs, errors.Join(err, o.Close())
	}
	return runs, errors.Join(db.Migrate(ctx), db.Close())
}

// failingPlace is the real filesystem failing the first rename of M8's
// copy into the store's path, so the swap swaps back.
func failingPlace(db string) vfs.FS {
	var mu sync.Mutex
	hit := false
	return errorfs.Wrap(vfs.Default, errorfs.InjectorFunc(func(op errorfs.Op) error {
		mu.Lock()
		defer mu.Unlock()
		if hit || op.Kind != errorfs.OpRename || filepath.Clean(op.Path) != storage.RewriteDir(db) {
			return nil
		}
		hit = true
		return syscall.EIO
	}))
}

// TestRestoreOfflineLiftsM8sSwapBackRefusal (05-storage §5S.3.13 "M8
// never loops", §5S.3.12 "In place"): two consecutive swap-backs refuse
// every later boot with storage.ErrRewriteSwappedBack (exit 78) until the
// operator acts; storage restore-offline of the attempt's checkpoint is that
// act: it retires the replaced store's KindRewrite journals, and the
// restored store's next boot, a new upgrade attempt with the cause gone,
// runs M8 once more and finishes it.
func TestRestoreOfflineLiftsM8sSwapBackRefusal(t *testing.T) {
	h := newSwapHost(t)
	if err := os.MkdirAll(h.state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.keyFile(), []byte("wayne"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.close(h.mustBoot(h.keyed(h.config(nil))))
	for i := range 2 {
		if runs, err := h.m8Boot(failingPlace(h.db)); runs != 1 || !errors.Is(err, restoreswap.ErrSwappedBack) {
			t.Fatalf("swap %d = %v after %d runs, want ErrSwappedBack", i+1, err, runs)
		}
	}
	if _, err := h.m8Boot(vfs.Default); !errors.Is(err, storage.ErrRewriteSwappedBack) {
		t.Fatalf("the boot after two swap-backs = %v, want storage.ErrRewriteSwappedBack", err)
	}
	attempt := h.pointer()
	res, err := RestoreOffline(h.ctx, h.keyed(h.config(nil)), RestoreOfflineRequest{Checkpoint: attempt, AcceptLoss: true})
	if err != nil || len(res.Warnings) != 0 {
		t.Fatalf("restore-offline of the attempt's checkpoint %s = %+v, %v", attempt, res, err)
	}
	entries, err := os.ReadDir(filepath.Join(h.state, "restore"))
	if err != nil {
		t.Fatal(err)
	}
	retired := 0
	for _, e := range entries {
		if !e.IsDir() || e.Name() == res.ID {
			continue
		}
		steps := stepsOf(h.journal(e.Name()))
		if len(steps) == 0 || steps[len(steps)-1] != restoreswap.StepRetired {
			t.Errorf("M8's journal %s ends %v after the restore, want retired", e.Name(), steps)
			continue
		}
		retired++
	}
	if retired != 2 {
		t.Errorf("%d journals retired, want M8's two", retired)
	}
	if runs, err := h.m8Boot(vfs.Default); err != nil || runs != 1 {
		t.Fatalf("the restored store's boot = %v after %d runs, want M8 run once and finished", err, runs)
	}
	if runs, err := h.m8Boot(vfs.Default); err != nil || runs != 0 {
		t.Fatalf("the boot after M8 finished = %v after %d runs, want nothing to run", err, runs)
	}
}
