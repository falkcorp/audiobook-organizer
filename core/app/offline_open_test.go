// file: core/app/offline_open_test.go
// version: 1.0.0
// guid: 050b91d4-5e16-41df-8591-df020d8ac71b
// last-edited: 2026-11-08

package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/coretest/logcapture"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// offlineKeeper is a module with a keyspace and the given migrations.
func offlineKeeper(migs ...storage.Migration) manifestOnly {
	return manifestOnly{ID: "alfred", Version: "1.0.0", DefaultEnabled: true, Storage: storage.Spec{Keyspace: true, Migrations: migs}}
}

// offlineOpts are Options over a fresh state directory, isolated from the
// environment, logging to a capture.
func offlineOpts(t *testing.T) Options {
	return Options{AppID: "gotham", StateDir: t.TempDir(), Args: []string{}, Env: []string{}, Logger: logcapture.New(t).Logger()}
}

// served boots and closes an App over opts, so the store exists and was
// served once.
func served(t *testing.T, opts Options, mods ...module.Module) {
	t.Helper()
	a, err := New(t.Context(), opts, mods...)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// storeFiles lists the store directory's files with their sizes.
func storeFiles(t *testing.T, opts Options) []string {
	t.Helper()
	dir := filepath.Join(opts.StateDir, "storage", "main.pebble")
	var out []string
	err := filepath.WalkDir(filepath.Dir(dir), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() == "LOCK" {
			return err // taking the lock touches LOCK, read-only too
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		out = append(out, p+":"+fi.ModTime().String()+":"+strconv.FormatInt(fi.Size(), 10))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestOfflineOpenRules (05-storage §5S.10; 06 §6.10; R7-4, DATA7-11): an
// app.OpenOffline read-write session commits and closes cleanly; a
// read-only open writes nothing and refuses writes; an open with a pending
// migration step, of a store that does not exist, with a staged or an
// in-flight swap of any kind, or while the service holds the store, is
// refused. Release R1 has no revocation journal, so the row's journaled
// offline revoking write has no R1 shape.
func TestOfflineOpenRules(t *testing.T) {
	seed := storage.Migration{Version: 1, Name: "seed-cave", Compat: storage.Additive, Seed: true,
		Up: func(_ context.Context, tx *storage.MigrationTx) error { return tx.Set([]byte("cave"), []byte("1")) }}
	keeper := offlineKeeper(seed)
	t.Run("read-write commits", func(t *testing.T) {
		opts := offlineOpts(t)
		served(t, opts, keeper)
		o, err := OpenOffline(t.Context(), opts, false, keeper)
		if err != nil {
			t.Fatalf("read-write open = %v", err)
		}
		h, err := o.Storage("alfred")
		if err != nil {
			t.Fatal(err)
		}
		if err := h.Keyspace.Set(t.Context(), []byte("robin"), []byte("dick")); err != nil {
			t.Fatalf("offline write = %v", err)
		}
		if err := o.Close(); err != nil {
			t.Fatalf("Close = %v", err)
		}
		if err := o.Close(); err != nil {
			t.Fatalf("a second Close = %v", err)
		}
		r, err := OpenOffline(t.Context(), opts, true, keeper)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = r.Close() }()
		rh, _ := r.Storage("alfred")
		if v, err := rh.Keyspace.Get(t.Context(), []byte("robin")); err != nil || string(v) != "dick" {
			t.Fatalf("the committed write reads %q (%v)", v, err)
		}
		if _, err := r.Storage("joker"); err == nil {
			t.Fatal("handles of a module that is not compiled in")
		}
	})
	t.Run("read-only writes nothing", func(t *testing.T) {
		opts := offlineOpts(t)
		served(t, opts, keeper)
		before := storeFiles(t, opts)
		o, err := OpenOffline(t.Context(), opts, true, keeper)
		if err != nil {
			t.Fatalf("read-only open = %v", err)
		}
		h, _ := o.Storage("alfred")
		if err := h.Keyspace.Set(t.Context(), []byte("robin"), []byte("jason")); err == nil {
			t.Fatal("a read-only open wrote")
		}
		if err := o.Close(); err != nil {
			t.Fatal(err)
		}
		if after := storeFiles(t, opts); !slices.Equal(before, after) {
			t.Fatalf("a read-only open changed the store's files:\nbefore %v\nafter  %v", before, after)
		}
	})
	refusals := []struct {
		name  string
		setup func(t *testing.T, opts Options) func()
		mods  []module.Module
		want  error
		token string
	}{
		{name: "pending migration step", mods: []module.Module{offlineKeeper(seed, storage.Migration{Version: 2, Name: "cowl",
			Compat: storage.Additive, Up: func(_ context.Context, tx *storage.MigrationTx) error { return tx.Set([]byte("cowl"), []byte("1")) }})},
			want: storage.ErrOfflinePendingSteps},
		{name: "staged swap", setup: func(t *testing.T, opts Options) func() {
			writeSlot(t, restorejournal.PendingPath(opts.StateDir))
			return nil
		}, want: restorejournal.ErrSwapPending, token: restorejournal.TokenRestorePending},
		{name: "in-flight swap", setup: func(t *testing.T, opts Options) func() {
			writeSlot(t, filepath.Join(restorejournal.JournalDir(opts.StateDir, "20261108T000000Z-0a0b0c0d"), restorejournal.InflightName))
			return nil
		}, want: restorejournal.ErrSwapPending, token: restorejournal.TokenRestoreInflight},
		{name: "running service", setup: func(t *testing.T, opts Options) func() {
			a, err := New(t.Context(), opts, keeper)
			if err != nil {
				t.Fatal(err)
			}
			return func() { _ = a.Close(context.Background()) }
		}},
	}
	for _, tc := range refusals {
		t.Run(tc.name, func(t *testing.T) {
			opts := offlineOpts(t)
			served(t, opts, keeper)
			if tc.setup != nil {
				if undo := tc.setup(t, opts); undo != nil {
					defer undo()
				}
			}
			mods := tc.mods
			if mods == nil {
				mods = []module.Module{keeper}
			}
			for _, ro := range []bool{false, true} {
				o, err := OpenOffline(t.Context(), opts, ro, mods...)
				if err == nil {
					_ = o.Close()
					t.Fatalf("readOnly=%v open succeeded, want a refusal", ro)
				}
				if tc.want != nil && !errors.Is(err, tc.want) {
					t.Fatalf("readOnly=%v open = %v, want %v", ro, err, tc.want)
				}
				if !strings.Contains(err.Error(), tc.token) {
					t.Fatalf("readOnly=%v open = %v, want the token %q", ro, err, tc.token)
				}
			}
		})
	}
	t.Run("absent store", func(t *testing.T) {
		opts := offlineOpts(t)
		if o, err := OpenOffline(t.Context(), opts, false, keeper); !errors.Is(err, storage.ErrNoStore) {
			if err == nil {
				_ = o.Close()
			}
			t.Fatalf("an offline open of no store = %v, want ErrNoStore", err)
		}
		if _, err := os.Stat(filepath.Join(opts.StateDir, "storage", "main.pebble")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("an offline open left a store directory: %v", err)
		}
	})
}

// writeSlot writes a minimal intent file at path.
func writeSlot(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"id":"20261108T000000Z-0a0b0c0d","kind":"reset","reset":"factory"}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestOfflineCommandsList (06 §6.4.5; R9-9): core's R1 offline commands are
// storage prune-checkpoints (R1-24), storage restore-offline and storage
// reset; each answers --help with its help and success, refuses arguments
// and unknown flags, and storage reset stages a reset with the service
// stopped, refusing without --confirm.
func TestOfflineCommandsList(t *testing.T) {
	opts := offlineOpts(t)
	keeper := offlineKeeper()
	cmds := OfflineCommands(opts, keeper)
	var names []string
	for _, c := range cmds {
		names = append(names, c.Name)
		var out bytes.Buffer
		if err := c.Run(t.Context(), []string{"--help"}, &out); err != nil || !strings.Contains(out.String(), "service stopped") {
			t.Fatalf("%s --help = %v, output %q", c.Name, err, out.String())
		}
		if err := c.Run(t.Context(), []string{"--batarang"}, &out); err == nil {
			t.Fatalf("%s accepted an unknown flag", c.Name)
		}
		if err := c.Run(t.Context(), []string{"gotham"}, &out); err == nil {
			t.Fatalf("%s accepted an argument", c.Name)
		}
	}
	if !slices.Equal(names, []string{"storage prune-checkpoints", "storage restore-offline", "storage reset"}) {
		t.Fatalf("commands %v", names)
	}
	served(t, opts, keeper)
	reset, restore := cmds[2], cmds[1]
	var out bytes.Buffer
	if err := reset.Run(t.Context(), []string{"--kind", "store"}, &out); !errors.Is(err, storage.ErrResetConfirmRequired) {
		t.Fatalf("reset without --confirm = %v", err)
	}
	if err := reset.Run(t.Context(), []string{"--kind", "store", "--confirm", "RESET"}, &out); err != nil {
		t.Fatalf("reset = %v", err)
	}
	st, err := restorejournal.ReadSlot(vfs.Default, opts.StateDir)
	if err != nil || st.Pending == nil || st.Pending.Reset != restorejournal.ResetStore {
		t.Fatalf("slot %+v (%v), want the store reset pending", st, err)
	}
	if !strings.Contains(out.String(), "next start") {
		t.Fatalf("output %q", out.String())
	}
	if err := restore.Run(t.Context(), nil, &out); err == nil {
		t.Fatal("restore-offline without a source succeeded")
	}
}
