// file: core/kernel/restore_xdev_test.go
// version: 0.1.0
// guid: c704cbaa-a5ec-43b4-8496-681289b2f376
// last-edited: 2026-11-21

package kernel

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/internal/restoreswap"
	"github.com/falkcorp/mediacore/storage/restorejournal"
	"github.com/falkcorp/mediacore/storage/restorejournal/restorejournaltest"
)

// newCrossDeviceHost is a swapHost laid out as a packaged install often is:
// the state directory (/var/lib/app) and the store's parent on two
// filesystems. The test host has one filesystem, so the state directory reads
// as a device of its own through restorejournaltest.OtherDevice, handed to
// every call through the host's context (bootseam.Seams.Probe) and returned
// for the kill matrices' contexts. The store lies outside the state
// directory: a store under it would be set aside to restore/<id>/previous/
// (the set-aside rule's first branch), across the modelled boundary.
func newCrossDeviceHost(t *testing.T) (*swapHost, restorejournal.Probe) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	probe := restorejournaltest.OtherDevice{Probe: restorejournal.OSProbe(), Dirs: []string{state}}
	return &swapHost{t: t, state: state, db: filepath.Join(root, "data", "storage", "main.pebble"),
		ctx: bootseam.With(t.Context(), &bootseam.Seams{BuildID: swapBuild, Probe: probe})}, probe
}

// TestCheckpointRestoreAcrossFilesystems: storage restore-offline
// --checkpoint (make rollback-restore after a flip) with the state directory
// on another filesystem than the store. Each checkpoint state file is staged
// on its own target's filesystem, so the swap's rename pre-check passes and
// the state file and the store both come back. Before the fix every state
// file was staged beside the store and the restore refused with
// "storage: a swap target cannot be moved by rename: ...: is on another
// filesystem than <store parent>/.mediacore-restore/<id>/staging/state/<label>".
func TestCheckpointRestoreAcrossFilesystems(t *testing.T) {
	h, _ := newCrossDeviceHost(t)
	name := h.completedUpgrade()
	res, err := RestoreOffline(h.ctx, h.keyed(h.config(nil)), RestoreOfflineRequest{Checkpoint: name, AcceptLoss: true})
	if err != nil {
		t.Fatalf("checkpoint restore across filesystems = %v, want it to restore", err)
	}
	if got := stepsOf(h.journal(res.ID)); got[len(got)-1] != restoreswap.StepAppliedOffline {
		t.Fatalf("journal %v, want applied_offline", got)
	}
	if got := read(t, h.keyFile()); got != "wayne" {
		t.Fatalf("key file = %q, want the checkpoint's wayne", got)
	}
	if h.slot().Busy() {
		t.Fatalf("slot after the restore = %+v, want empty", h.slot())
	}
	if got := h.readCave(); got != "1" {
		t.Fatalf("cave after the restore = %q, want the checkpoint's 1", got)
	}
	// The replaced key file is set aside on the state directory's side.
	if got := filesHolding(t, h.state, "bane"); len(got) != 1 {
		t.Fatalf("the replaced key file is kept %d times under the state directory (%v), want once", len(got), got)
	}
	h.assertNoStagingLeft(res.ID)
	// The state directory's private directory held only the staging, which
	// the applied restore emptied and removed.
	if _, err := os.Stat(filepath.Join(h.state, restorejournal.AsideDirName, res.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the state directory's staging root of %s is still there (%v), want it removed once empty", res.ID, err)
	}
}

// TestArchiveRestoreAcrossFilesystems: storage restore-offline --archive
// with --keyring (a state file in the state directory) and --gap-fill (a
// file beside the store), the state directory on another filesystem than the
// store. One restore stages on both sides, each file on its own target's
// filesystem, and both come back.
func TestArchiveRestoreAcrossFilesystems(t *testing.T) {
	h, _ := newCrossDeviceHost(t)
	h.close(h.mustBoot(h.config(nil)))
	saved := filepath.Join(t.TempDir(), "archived.pebble")
	copyTree(t, h.db, saved)
	h.close(h.mustBoot(h.config(nil, caveStep(2, "bane"))))
	c := h.config(nil)
	c.ArchiveExtract = func(_ context.Context, _, dir string) (string, error) {
		copyTree(t, saved, dir)
		return "legacy-joker_20261112", nil
	}
	keyring := write(t, filepath.Join(h.state, "secrets.keyring"), "live-keyring", 0o600)
	gap := write(t, filepath.Join(filepath.Dir(h.db), "config.yaml"), "live: gap\n", 0o600)
	exported := write(t, filepath.Join(t.TempDir(), "gotham.keyring"), "exported-keyring", 0o600)
	gapFill := write(t, filepath.Join(t.TempDir(), "config.yaml"), "root_dir: /batcave\n", 0o600)
	res, err := RestoreOffline(h.ctx, c, RestoreOfflineRequest{Archive: filepath.Join(t.TempDir(), "joker.tar.gz"),
		Keyring: exported, GapFill: gapFill, AcceptLoss: true})
	if err != nil {
		t.Fatalf("archive restore across filesystems = %v, want it to restore", err)
	}
	if got := read(t, keyring); got != "exported-keyring" {
		t.Errorf("keyring = %q, want the export's", got)
	}
	if got := read(t, gap); got != "root_dir: /batcave\n" {
		t.Errorf("gap-fill file = %q, want the operator's", got)
	}
	if got := h.readCave(); got != "1" {
		t.Errorf("cave after the restore = %q, want the archive's 1", got)
	}
	h.assertNoStagingLeft(res.ID)
}

// TestRefusedRestoreAcrossFilesystemsLeavesNoStaging: a restore refused after
// it staged (here BeforeSwap fails) removes what it staged on both
// filesystems, the state directory's private directory included, for a
// checkpoint and for an archive with both files.
func TestRefusedRestoreAcrossFilesystemsLeavesNoStaging(t *testing.T) {
	bat := errors.New("the batsignal is out")
	t.Run("checkpoint", func(t *testing.T) {
		h, _ := newCrossDeviceHost(t)
		name := h.completedUpgrade()
		c := h.keyed(h.config(nil))
		c.BeforeSwap = func(string) error { return bat }
		if _, err := RestoreOffline(h.ctx, c, RestoreOfflineRequest{Checkpoint: name, AcceptLoss: true}); !errors.Is(err, bat) {
			t.Fatalf("restore with a failing BeforeSwap = %v, want its error", err)
		}
		h.assertNothingStaged()
		if got := read(t, h.keyFile()); got != "bane" {
			t.Fatalf("key file = %q, want the live bane untouched", got)
		}
	})
	t.Run("archive", func(t *testing.T) {
		h, _ := newCrossDeviceHost(t)
		h.close(h.mustBoot(h.config(nil)))
		saved := filepath.Join(t.TempDir(), "archived.pebble")
		copyTree(t, h.db, saved)
		c := h.config(nil)
		c.ArchiveExtract = func(_ context.Context, _, dir string) (string, error) {
			copyTree(t, saved, dir)
			return "legacy-joker_20261112", nil
		}
		c.BeforeSwap = func(string) error { return bat }
		exported := write(t, filepath.Join(t.TempDir(), "gotham.keyring"), "exported-keyring", 0o600)
		gapFill := write(t, filepath.Join(t.TempDir(), "config.yaml"), "root_dir: /batcave\n", 0o600)
		_, err := RestoreOffline(h.ctx, c, RestoreOfflineRequest{Archive: filepath.Join(t.TempDir(), "joker.tar.gz"),
			Keyring: exported, GapFill: gapFill, AcceptLoss: true})
		if !errors.Is(err, bat) {
			t.Fatalf("restore with a failing BeforeSwap = %v, want its error", err)
		}
		h.assertNothingStaged()
	})
}

// assertNoStagingLeft fails when restore id left a staged file behind: every
// staged item was placed, so each staging leaf it made holds no file.
func (h *swapHost) assertNoStagingLeft(id string) {
	h.t.Helper()
	for _, root := range []string{
		filepath.Join(h.state, restorejournal.AsideDirName, id, "staging"),
		restorejournal.StagingDir(h.db, id),
	} {
		var files []string
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				files = append(files, p)
			}
			return nil
		})
		if len(files) > 0 {
			h.t.Errorf("staging %s still holds %v after the restore placed everything", root, files)
		}
	}
}
