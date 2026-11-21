// file: core/kernel/restore_staging_sweep_test.go
// version: 0.1.0
// guid: 9a4e7c12-5b3d-4e68-8f21-c07d3a96b5e4
// last-edited: 2026-11-21

package kernel

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// TestRestoreStagingChainIsSynced: the directories a restore creates to
// stage a state file on the state directory's side are fsynced level by
// level before the swap starts (mkdirStaging), so a power loss after step c
// set the live key aside cannot drop the staged copy step d renames into
// place. Before, only the leaf directory was synced, and nothing else on the
// state directory's side synced .mediacore-restore/<id>/staging.
func TestRestoreStagingChainIsSynced(t *testing.T) {
	h, probe := newCrossDeviceHost(t)
	name := h.completedUpgrade()
	rec := &syncRecorder{FS: vfs.Default}
	h.ctx = bootseam.With(t.Context(), &bootseam.Seams{BuildID: swapBuild, Probe: probe, FS: rec})
	stateAside := filepath.Join(h.state, restorejournal.AsideDirName)
	var want, synced []string
	c := h.keyed(h.config(nil))
	c.BeforeSwap = func(string) error {
		ids, err := os.ReadDir(stateAside)
		if err != nil || len(ids) != 1 {
			t.Fatalf("the state directory's staging before the swap: %v (%v), want one restore", ids, err)
		}
		root := filepath.Join(stateAside, ids[0].Name())
		want = []string{stateAside, root, filepath.Join(root, "staging")}
		synced = slices.Clone(rec.synced)
		return nil
	}
	if _, err := RestoreOffline(h.ctx, c, RestoreOfflineRequest{Checkpoint: name, AcceptLoss: true}); err != nil {
		t.Fatalf("restore = %v", err)
	}
	if want == nil {
		t.Fatal("BeforeSwap never ran: this test checks nothing")
	}
	for _, d := range want {
		if !slices.Contains(synced, d) {
			t.Errorf("%s was not fsynced before the swap; synced: %v", d, synced)
		}
	}
}

// syncRecorder is vfs.Default recording the path of every directory a Sync
// reached through OpenDir.
type syncRecorder struct {
	vfs.FS
	synced []string
}

func (r *syncRecorder) Unwrap() vfs.FS { return r.FS }

func (r *syncRecorder) OpenDir(name string) (vfs.File, error) {
	f, err := r.FS.OpenDir(name)
	if err != nil {
		return nil, err
	}
	return &syncedDir{File: f, name: filepath.Clean(name), r: r}, nil
}

type syncedDir struct {
	vfs.File
	name string
	r    *syncRecorder
}

func (d *syncedDir) Sync() error {
	err := d.File.Sync()
	if err == nil {
		d.r.synced = append(d.r.synced, d.name)
	}
	return err
}

// assertNoOrphanStaging: every restore's private directory on either side
// is one an intent was linked for (restore/<id> exists). One without is a
// restore's staging that nothing will ever remove.
func (h *swapHost) assertNoOrphanStaging(what string) {
	h.t.Helper()
	for _, dir := range []string{filepath.Dir(h.db), h.state} {
		entries, _ := os.ReadDir(filepath.Join(dir, restorejournal.AsideDirName))
		for _, e := range entries {
			if _, err := os.Stat(restorejournal.JournalDir(h.state, e.Name())); errors.Is(err, os.ErrNotExist) {
				h.t.Errorf("%s: %s is left with no intent linked for it",
					what, filepath.Join(dir, restorejournal.AsideDirName, e.Name()))
			}
		}
	}
}
