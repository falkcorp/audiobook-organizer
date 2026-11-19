// file: core/app/config_boot_test.go
// version: 1.0.0
// guid: 5d0c7b3e-2a41-4f9a-8c66-91e4b7a2d3f8
// last-edited: 2026-11-18

package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/module"
)

// With Options.CoreConfigBoot storagePath passes on only the compiled path: the
// environment is not read (the layers own core.storage.path and the state
// directory), the state directory is left to P0, and an app with no storage
// still has neither.
func TestStoragePath_GatedPassesOnlyTheCompiledPath(t *testing.T) {
	keeper := offlineKeeper()
	elsewhere := filepath.Join(t.TempDir(), "elsewhere", "main.pebble")
	opts := offlineOpts(t)
	opts.CoreConfigBoot = true
	opts.Env = []string{"GOTHAM_CORE_STORAGE_PATH=" + elsewhere, "GOTHAM_CORE_PATHS_STATE_DIR=" + t.TempDir()}

	db, state, err := storagePath(opts, []module.Module{keeper})
	if err != nil || db != "" || state != "" {
		t.Fatalf("gated storagePath with only the environment = %q, %q, %v; want \"\", \"\", nil", db, state, err)
	}
	opts.R1StoragePath = filepath.Join(t.TempDir(), "abo.db")
	if db, state, err = storagePath(opts, []module.Module{keeper}); err != nil || db != opts.R1StoragePath || state != "" {
		t.Fatalf("gated storagePath with R1StoragePath = %q, %q, %v", db, state, err)
	}
	opts.R1StoragePath = "relative.db"
	if _, _, err = storagePath(opts, []module.Module{keeper}); err == nil || !strings.Contains(err.Error(), "not an absolute path") {
		t.Fatalf("gated storagePath with a relative R1StoragePath = %v", err)
	}
	opts.R1StoragePath = ""
	if db, state, err = storagePath(opts, nil); err != nil || db != "" || state != "" {
		t.Fatalf("gated storagePath with no storage = %q, %q, %v", db, state, err)
	}
	// The gate-off twin reads the environment, as before.
	opts.CoreConfigBoot = false
	if db, _, err = storagePath(opts, []module.Module{keeper}); err != nil || db != elsewhere {
		t.Fatalf("ungated storagePath = %q, %v; want the environment's %q", db, err, elsewhere)
	}
}

// TestCoreConfigBoot_ServeAndOfflineOpenOneStore: with the gate on, New and
// every offline command resolve core.storage.path and the state directory
// through P0, so they open one store and one keyring; the boot itself creates
// the keyring (the gate-on twin of TestExportKeyring_OpensNoStore's "writes
// nothing" cell, where a gate-off boot leaves none).
func TestCoreConfigBoot_ServeAndOfflineOpenOneStore(t *testing.T) {
	root := t.TempDir()
	db := filepath.Join(root, "data", "main.pebble")
	state := filepath.Join(root, "st")
	opts := offlineOpts(t)
	opts.CoreConfigBoot = true
	opts.StateDir = filepath.Join(root, "compiled-default-unused")
	opts.Env = []string{"GOTHAM_CORE_STORAGE_PATH=" + db, "GOTHAM_CORE_PATHS_STATE_DIR=" + state}
	keeper := offlineKeeper()

	served(t, opts, keeper)
	if _, err := os.Stat(db); err != nil {
		t.Fatalf("the gated boot did not open the store at core.storage.path: %v", err)
	}
	keyring := filepath.Join(state, "secrets.keyring")
	if _, err := os.Stat(keyring); err != nil {
		t.Fatalf("a gated boot left no keyring in core.paths.state_dir: %v", err)
	}
	if _, err := os.Stat(opts.StateDir); !os.IsNotExist(err) {
		t.Fatalf("the compiled state directory default was created although the environment named one: %v", err)
	}

	// Offline: the open finds the same store, and export-keyring the same keyring.
	off, err := OpenOffline(t.Context(), opts, true, keeper)
	if err != nil {
		t.Fatalf("OpenOffline did not find the store a gated boot opened: %v", err)
	}
	if err := off.Close(); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "keyring.export")
	var stdout bytes.Buffer
	cmds := OfflineCommands(opts, keeper)
	if err := secretsCmd(t, cmds, "secrets export-keyring").Run(t.Context(), []string{"--out", out}, &stdout); err != nil {
		t.Fatalf("export-keyring: %v", err)
	}
	got, err := os.ReadFile(out)
	want, werr := os.ReadFile(keyring)
	if err != nil || werr != nil || !bytes.Equal(got, want) {
		t.Fatalf("the export is not the gated boot's keyring (%v, %v)", err, werr)
	}
	// prune-checkpoints resolves the same store and state directory.
	stdout.Reset()
	if err := secretsCmd(t, cmds, "storage prune-checkpoints").Run(t.Context(), nil, &stdout); err != nil {
		t.Fatalf("prune-checkpoints: %v", err)
	}
}

// TestCoreConfigBoot_NoStorageOfflineRefused: with the gate on and no module
// declaring storage, an offline command that needs the store says so (the
// resolve returns "" rather than inventing a path).
func TestCoreConfigBoot_NoStorageOfflineRefused(t *testing.T) {
	opts := offlineOpts(t)
	opts.CoreConfigBoot = true
	cmds := OfflineCommands(opts)
	err := secretsCmd(t, cmds, "storage prune-checkpoints").Run(context.Background(), nil, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "declares storage") {
		t.Fatalf("prune-checkpoints with no storage = %v", err)
	}
}

// TestCoreConfigBoot_ClaimsModeIsStillRead: core.storage.claims.mode and the
// state-file table do not depend on the store path being known at New (the
// gate leaves it to P0), so a mistyped dial fails the gated boot as it fails
// the ungated one.
func TestCoreConfigBoot_ClaimsModeIsStillRead(t *testing.T) {
	opts := offlineOpts(t)
	opts.CoreConfigBoot = true
	opts.Env = []string{"GOTHAM_CORE_STORAGE_CLAIMS_MODE=lenient"}
	a, err := New(t.Context(), opts, offlineKeeper())
	if err == nil {
		_ = a.Close(context.Background())
		t.Fatal("a gated boot accepted an unknown claims mode")
	}
	if !strings.Contains(err.Error(), "claims mode") {
		t.Fatalf("error = %v", err)
	}
}
