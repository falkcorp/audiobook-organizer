// file: core/app/offline_test.go
// version: 1.0.1
// guid: cc4eb0c6-a5b7-4c42-ade1-8413517a38ae
// last-edited: 2026-11-08

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
)

// seeded is a storage module with n Seed steps, each writing one key.
func seeded(n int) manifestOnly {
	var migs []storage.Migration
	for v := 1; v <= n; v++ {
		key := []byte{'k', byte('0' + v)}
		migs = append(migs, storage.Migration{Version: v, Name: "seed-" + string(key), Compat: storage.Additive, Seed: true,
			Up: func(_ context.Context, tx *storage.MigrationTx) error { return tx.Set(key, []byte("1")) }})
	}
	return manifestOnly{ID: "alfred", Version: "1.0.0", DefaultEnabled: true, Storage: storage.Spec{Keyspace: true, Migrations: migs}}
}

// runPrune runs storage prune-checkpoints from OfflineCommands with args.
func runPrune(t *testing.T, opts Options, mods []module.Module, args ...string) (string, error) {
	t.Helper()
	for _, c := range OfflineCommands(opts, mods...) {
		if c.Name == "storage prune-checkpoints" {
			var out bytes.Buffer
			err := c.Run(t.Context(), args, &out)
			return out.String(), err
		}
	}
	t.Fatal("OfflineCommands lists no storage prune-checkpoints")
	return "", nil
}

// TestPruneCheckpointsCommand (05-storage §5S.3.12, §5S.10
// TestPruneCheckpoints, the command's half; 06 §6.4.5): OfflineCommands
// lists storage prune-checkpoints with its help; it deletes an upgrade's
// completed checkpoint, record, copy and pointer, while the app that took it
// still holds the store open (it opens no store); --include-open needs
// --confirm, --confirm alone and stray arguments are refused, --help
// prints the help, and an app without storage has nothing to prune.
func TestPruneCheckpointsCommand(t *testing.T) {
	opts := Options{AppID: "gotham", StateDir: t.TempDir(), Args: []string{}, Env: []string{}}
	a, err := New(t.Context(), opts, seeded(1))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	mods := []module.Module{seeded(2)}
	a, err = New(t.Context(), opts, mods...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := a.Close(context.Background()); err != nil {
			t.Errorf("Close = %v", err)
		}
	}()
	dbPath, _, err := storagePath(opts, mods)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(dbPath + ".migration-checkpoint")
	if err != nil {
		t.Fatalf("the upgrade left no pointer: %v", err)
	}
	var ptr struct {
		Name, State string
	}
	if err := json.Unmarshal(raw, &ptr); err != nil || ptr.State != "completed" {
		t.Fatalf("the upgrade's pointer = %s (%v), want a completed checkpoint", raw, err)
	}
	h, err := a.CheckpointHealth()
	if err != nil || len(h.Checkpoints) != 1 || h.Checkpoints[0].Name != ptr.Name {
		t.Fatalf("CheckpointHealth before the prune = %+v (%v), want the upgrade's checkpoint", h, err)
	}

	cmds := OfflineCommands(opts, mods...)
	i := slices.IndexFunc(cmds, func(c Command) bool { return c.Name == "storage prune-checkpoints" })
	if i < 0 || cmds[i].Short == "" || !strings.Contains(cmds[i].Long, "while it serves") {
		t.Fatalf("OfflineCommands = %+v, want storage prune-checkpoints with its help", cmds)
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--include-open"}, "add --confirm"},
		{[]string{"--confirm"}, "does nothing alone"},
		{[]string{"arkham"}, "unexpected arguments"},
		{[]string{"--two-face"}, "flag provided but not defined"},
	} {
		if _, err := runPrune(t, opts, mods, tc.args...); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("prune %v = %v, want an error containing %q", tc.args, err, tc.want)
		}
	}
	if out, err := runPrune(t, opts, mods, "--help"); err != nil || !strings.Contains(out, "--include-open") {
		t.Fatalf("prune --help = %q, %v; want the usage", out, err)
	}
	if _, err := os.Stat(dbPath + ".migration-checkpoint"); err != nil {
		t.Fatalf("a refused prune deleted something: %v", err)
	}

	out, err := runPrune(t, opts, mods)
	if err != nil || out != "deleted checkpoint "+ptr.Name+"\n" {
		t.Fatalf("prune = %q, %v; want the completed checkpoint deleted", out, err)
	}
	cps := filepath.Join(filepath.Dir(dbPath), ".mediacore-checkpoints")
	for _, p := range []string{filepath.Join(cps, ptr.Name+".json"), filepath.Join(cps, ptr.Name), dbPath + ".migration-checkpoint"} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s survived the prune (%v)", filepath.Base(p), err)
		}
	}
	if out, err := runPrune(t, opts, mods); err != nil || out != "no checkpoints to delete\n" {
		t.Fatalf("a second prune = %q, %v; want nothing to delete", out, err)
	}
	if h, err := a.CheckpointHealth(); err != nil || len(h.Checkpoints) != 0 {
		t.Fatalf("CheckpointHealth after the prune = %+v (%v), want none", h, err)
	}

	plain := []module.Module{manifestOnly{ID: "alfred", Version: "1.0.0"}}
	if _, err := runPrune(t, opts, plain); err == nil || !strings.Contains(err.Error(), "declares storage") {
		t.Fatalf("prune for an app without storage = %v, want a refusal", err)
	}
}
