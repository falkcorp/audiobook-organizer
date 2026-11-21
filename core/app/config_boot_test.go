// file: core/app/config_boot_test.go
// version: 1.0.2
// guid: 5d0c7b3e-2a41-4f9a-8c66-91e4b7a2d3f8
// last-edited: 2026-11-19

package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
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

// TestCoreConfigBoot_OfflineWritesRefuseANonOwnerFirst (F3-07 a6,
// seams.d/F3-07.md): with the gate on, each offline command that writes
// names itself to kernel.ResolveStorage, which refuses a state directory
// candidate that this user does not own (or, if it is absent, whose nearest
// existing ancestor this user does not own) before step 0 creates anything. The compiled default here exists and cannot be used as
// a directory (a regular file), so without the check step 0 would fall back
// and create the store's directory beside it before the command's own owner
// check refused; with it nothing is made. A read-only open is not refused.
// The resolver never reads the boot record, so none is set aside as .bad
// (kernel's TestResolveStorage_RefusesANonOwnerBeforeAnything).
func TestCoreConfigBoot_OfflineWritesRefuseANonOwnerFirst(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("files carry no owner storage.StateDirOwner reads")
	}
	root := t.TempDir()
	state, data := filepath.Join(root, "st"), filepath.Join(root, "data")
	if err := os.WriteFile(state, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	opts := offlineOpts(t)
	opts.CoreConfigBoot = true
	opts.StateDir = state // the compiled default: exists, not a directory
	opts.Env = []string{"GOTHAM_CORE_STORAGE_PATH=" + filepath.Join(data, "main.pebble")}
	keeper := offlineKeeper()
	stranger := os.Geteuid() + 1
	ctx := bootseam.With(t.Context(), &bootseam.Seams{Euid: func() int { return stranger }})
	cmds := OfflineCommands(opts, keeper)

	for _, tc := range []struct {
		name, what string
		run        func() error
	}{
		{"storage prune-checkpoints", "storage prune-checkpoints", nil},
		{"storage reset", "storage reset", nil},
		{"storage restore-offline", "storage restore-offline", nil},
		{"secrets export-keyring", "secrets export-keyring", nil},
		{"secrets rotate-derive", "secrets rotate-derive", nil},
		{"secrets set-install-id", "secrets set-install-id", nil},
		{"storage quarantine purge", "a read-write offline open", nil},
		{"OpenOffline read-write", "a read-write offline open", func() error {
			_, err := OpenOffline(ctx, opts, false, keeper)
			return err
		}},
	} {
		args := map[string][]string{
			"storage reset":            {"--confirm", "RESET"},
			"secrets export-keyring":   {"--out", filepath.Join(t.TempDir(), "keyring.export")},
			"secrets set-install-id":   {"--install-id", "wayne-manor"},
			"storage quarantine purge": {"--step", "joker", "--confirm"},
		}[tc.name]
		run := tc.run
		if run == nil {
			run = func() error { return secretsCmd(t, cmds, tc.name).Run(ctx, args, &bytes.Buffer{}) }
		}
		err := run()
		if !errors.Is(err, storage.ErrNotStateDirOwner) || !strings.Contains(err.Error(), tc.what) {
			t.Errorf("%s as a stranger = %v, want storage.ErrNotStateDirOwner naming %q", tc.name, err, tc.what)
		}
		if _, err := os.Stat(data); !os.IsNotExist(err) {
			t.Fatalf("%s as a stranger created the store's directory (the fallback): %v", tc.name, err)
		}
	}
	// A read-only open is not the owner rule's: it falls back beside the
	// store, as serve would, and is not refused as a non-owner.
	if _, err := OpenOffline(ctx, opts, true, keeper); errors.Is(err, storage.ErrNotStateDirOwner) {
		t.Fatalf("a read-only OpenOffline as a stranger was refused as a non-owner: %v", err)
	}
}

// TestCoreConfigBoot_RestoreOfflineOnABlankHost (F3-07 b, seams.d/F3-07.md
// "Absent state directory"): storage restore-offline gets no exemption from
// the owner rule on a host with no state directory yet. A blank host here is
// a state directory four levels below a directory that exists. Run as the
// user who owns that directory, the command's resolve creates the chain and
// the restore goes on to fail for want of a checkpoint (not as a non-owner).
// Run as another user it is refused as a non-owner, naming the command, and
// not one level of the chain is created.
func TestCoreConfigBoot_RestoreOfflineOnABlankHost(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("files carry no owner storage.StateDirOwner reads")
	}
	keeper := offlineKeeper()
	args := []string{"--checkpoint", "joker"}
	chain := func(t *testing.T) (top, state string, opts Options) {
		base := t.TempDir()
		top = filepath.Join(base, "wayne", "manor")
		state = filepath.Join(top, "batcave", "state")
		opts = offlineOpts(t)
		opts.CoreConfigBoot = true
		opts.StateDir = state
		return top, state, opts
	}
	restore := func(ctx context.Context, opts Options) error {
		return secretsCmd(t, OfflineCommands(opts, keeper), "storage restore-offline").Run(ctx, args, &bytes.Buffer{})
	}

	t.Run("this user owns the nearest existing ancestor", func(t *testing.T) {
		top, state, opts := chain(t)
		err := restore(t.Context(), opts)
		if errors.Is(err, storage.ErrNotStateDirOwner) {
			t.Fatalf("restore-offline as the ancestor's owner was refused as a non-owner: %v", err)
		}
		if err == nil {
			t.Fatal("restore-offline of a checkpoint that does not exist succeeded")
		}
		if fi, serr := os.Stat(state); serr != nil || !fi.IsDir() {
			t.Fatalf("the blank host's state directory %s was not created (%v) after: %v", state, serr, err)
		}
		if _, serr := os.Stat(top); serr != nil {
			t.Fatalf("the missing levels were not created: %v", serr)
		}
	})
	t.Run("another user owns it", func(t *testing.T) {
		top, _, opts := chain(t)
		stranger := os.Geteuid() + 1
		ctx := bootseam.With(t.Context(), &bootseam.Seams{Euid: func() int { return stranger }})
		err := restore(ctx, opts)
		if !errors.Is(err, storage.ErrNotStateDirOwner) || !strings.Contains(err.Error(), "storage restore-offline") {
			t.Fatalf("restore-offline onto a blank host as a stranger = %v, want storage.ErrNotStateDirOwner naming the command", err)
		}
		if _, serr := os.Stat(filepath.Dir(top)); !os.IsNotExist(serr) {
			t.Fatalf("the refused restore-offline created a level of the chain: %v", serr)
		}
	})
}
