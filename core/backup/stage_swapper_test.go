// file: core/backup/stage_swapper_test.go
// version: 0.1.0
// guid: 0f6a2d3e-8b41-4c7e-9a25-d1e3b7c4f580
// last-edited: 2026-11-21

package backup_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/backup"
	"github.com/falkcorp/mediacore/internal/restoreswap"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// TestStageRestoreThroughTheSwapperKeepsTheConfigCopy: a staged restore of
// an archive that carries the config file, driven through the real
// restoreswap.Swapper (the Stager the kernel wires), not the fake that only
// creates the intent. Without ReplaceConfig the archive's config files go
// to restore/<id>/config.restored/ for the operator to merge. They used to
// be written there before Stage, which refuses an intent whose restore/<id>
// already exists ("an intent id is never reused"), so every such restore was
// refused; they are written once Stage created the directory.
func TestStageRestoreThroughTheSwapperKeepsTheConfigCopy(t *testing.T) {
	ctx := asOwner(context.Background())
	e, sw := swapperEnv(t, nil)
	a := e.create("manual")
	pr, err := e.svc.StageRestore(ctx, a.ID, backup.RestoreOptions{})
	if err != nil {
		t.Fatalf("StageRestore through the Swapper = %v, want the restore staged", err)
	}
	st, err := restorejournal.ReadSlot(vfs.Default, e.stateDir)
	if err != nil || st.Pending == nil || st.Pending.Kind != restorejournal.KindArchive {
		t.Fatalf("slot = %+v (%v), want the archive restore pending", st, err)
	}
	id := st.Pending.ID
	copies := filepath.Join(restorejournal.JournalDir(e.stateDir, id), "config.restored")
	for name, want := range map[string]string{"mediacore.yaml": "library: gotham\n", "config.boot.json": `{"storage":"x"}`} {
		got, err := os.ReadFile(filepath.Join(copies, name))
		if err != nil || string(got) != want {
			t.Errorf("config.restored/%s = %q (%v), want the archive's %q", name, got, err, want)
		}
	}
	if !strings.Contains(strings.Join(pr.Warnings, "|"), "config.restored") {
		t.Errorf("no pointer to the staged config copy: %v", pr.Warnings)
	}
	if recs, err := sw.Journal(id); err != nil || len(recs) == 0 {
		t.Fatalf("journal of %s = %v (%v), want its staged record", id, recs, err)
	}
}

// swapperEnv is an env whose config file and boot record are set and whose
// Stager is the real restoreswap.Swapper, wrapped by wrap (given the state
// directory) when it is not nil.
func swapperEnv(t *testing.T, wrap func(s backup.Stager, stateDir string) backup.Stager) (*env, *restoreswap.Swapper) {
	t.Helper()
	confDir := t.TempDir()
	cfg, boot := filepath.Join(confDir, "mediacore.yaml"), filepath.Join(confDir, "config.boot.json")
	for p, body := range map[string]string{cfg: "library: gotham\n", boot: `{"storage":"x"}`} {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	e := newEnv(t)
	sw, err := restoreswap.New(restoreswap.Options{StateDir: e.stateDir, BuildID: e.opts.BuildID, Owners: e.owners})
	if err != nil {
		t.Fatal(err)
	}
	var st backup.Stager = sw
	if wrap != nil {
		st = wrap(sw, e.stateDir)
	}
	e.opts.Config, e.opts.ConfigPath, e.opts.BootRecordPath, e.opts.Stager = e.cfg, cfg, boot, st
	e.rebuild()
	return e, sw
}

// blockConfigCopy is a Stager that, once the real Stage linked the intent,
// puts a file where restore/<id>/config.restored/ goes, so the copy cannot
// be written.
type blockConfigCopy struct {
	backup.Stager
	stateDir string
}

func (b blockConfigCopy) Stage(ctx context.Context, in restorejournal.Intent) error {
	if err := b.Stager.Stage(ctx, in); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(restorejournal.JournalDir(b.stateDir, in.ID), "config.restored"), []byte("joker"), 0o600)
}

// TestStageRestoreSurfacesAMissingConfigCopy: a config copy that cannot be
// written once the restore is staged leaves the restore staged and tells the
// operator, in the stage result and in the stage note that Pending reads
// back, that the copy is not in config.restored and where the files are.
func TestStageRestoreSurfacesAMissingConfigCopy(t *testing.T) {
	ctx := asOwner(context.Background())
	e, _ := swapperEnv(t, func(s backup.Stager, stateDir string) backup.Stager {
		return blockConfigCopy{Stager: s, stateDir: stateDir}
	})
	a := e.create("manual")
	pr, err := e.svc.StageRestore(ctx, a.ID, backup.RestoreOptions{})
	if err != nil {
		t.Fatalf("StageRestore = %v, want the restore staged with a warning", err)
	}
	const missing = "could not be copied to <state_dir>/restore/"
	if w := strings.Join(pr.Warnings, "|"); !strings.Contains(w, missing) || strings.Contains(w, "config files are in") {
		t.Fatalf("stage result warnings %v, want the missing copy named and no claim that it is there", pr.Warnings)
	}
	p, err := e.svc.Pending(ctx)
	if err != nil || p == nil || !strings.Contains(strings.Join(p.Warnings, "|"), missing) {
		t.Fatalf("Pending = %+v (%v), want the stage note to carry the missing copy", p, err)
	}
}
