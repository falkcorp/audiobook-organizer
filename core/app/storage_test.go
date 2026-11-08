// file: core/app/storage_test.go
// version: 1.0.1
// guid: cd83307e-c3ce-4393-b440-9d8ba60a7afb
// last-edited: 2026-11-08

package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
)

// manifestOnly is a module that is only a manifest.
type manifestOnly module.Manifest

func (m manifestOnly) Manifest() module.Manifest          { return module.Manifest(m) }
func (m manifestOnly) Register(r *module.Registrar) error { return nil }

// TestStoragePath_Rule: core.storage.path is resolved only when a module
// declares storage; an absolute <EnvPrefix>_CORE_STORAGE_PATH wins and a
// relative one is refused (05-storage §5S.4 step 1); otherwise
// <state_dir>/storage/main.pebble, absolute even for a relative state
// directory. The state directory comes back with it, absolute, in both
// cases: New resolves it once (R1-21 review n4).
func TestStoragePath_Rule(t *testing.T) {
	t.Chdir(t.TempDir())
	plain := manifestOnly{ID: "alfred", Version: "1.0.0"}
	keeper := manifestOnly{ID: "alfred", Version: "1.0.0", Storage: storage.Spec{Keyspace: true}}
	opts := Options{AppID: "gotham", StateDir: "gotham-state", Env: []string{}}

	if p, dir, err := storagePath(opts, []module.Module{plain}); err != nil || p != "" || dir != "" {
		t.Fatalf("no module declares storage: path %q, state %q, %v; want none", p, dir, err)
	}
	if _, err := os.Stat("gotham-state"); !os.IsNotExist(err) {
		t.Fatalf("an app without storage created its state directory: %v", err)
	}

	p, dir, err := storagePath(opts, []module.Module{plain, keeper})
	wd, _ := os.Getwd()
	state := filepath.Join(wd, "gotham-state")
	if want := filepath.Join(state, "storage", "main.pebble"); err != nil || p != want || dir != state {
		t.Fatalf("default path = %q, state %q, %v; want %q in %q", p, dir, err, want, state)
	}

	abs := filepath.Join(t.TempDir(), "batcave.pebble")
	opts.Env = []string{"GOTHAM_CORE_STORAGE_PATH=" + abs}
	if p, dir, err := storagePath(opts, []module.Module{keeper}); err != nil || p != abs || dir != state {
		t.Fatalf("env path = %q, state %q, %v; want %q, state %q", p, dir, err, abs, state)
	}

	opts.Env = []string{"GOTHAM_CORE_STORAGE_PATH=batcave.pebble"}
	if _, _, err := storagePath(opts, []module.Module{keeper}); err == nil || !strings.Contains(err.Error(), "not an absolute path") {
		t.Fatalf("relative env path = %v, want a refusal", err)
	}
}

// TestCloseReleasesTheStore: an App that is never Run releases the store New
// opened when it is closed, so a second App boots the same state directory;
// a second Close does nothing, and Run after Close fails without serving.
func TestCloseReleasesTheStore(t *testing.T) {
	keeper := manifestOnly{ID: "alfred", Version: "1.0.0", Storage: storage.Spec{Keyspace: true}}
	opts := Options{AppID: "gotham", StateDir: t.TempDir(), Args: []string{}, Env: []string{}}
	a, err := New(t.Context(), opts, keeper)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Close(context.Background()); err != nil {
		t.Fatalf("Close = %v", err)
	}
	if err := a.Close(context.Background()); err != nil {
		t.Fatalf("a second Close = %v, want nil", err)
	}
	if err := a.Run(t.Context()); err == nil {
		t.Fatal("Run after Close served")
	}
	b, err := New(t.Context(), opts, keeper)
	if err != nil {
		t.Fatalf("a second App over the closed App's state directory: %v", err)
	}
	if err := b.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// TestStorageFSKeepsTheStoreOffDisk: Options.StorageFS reaches the store, so
// an App on storage.MemFS leaves nothing under its state directory; the zero
// value is the OS's, where the store is on disk (the control).
func TestStorageFSKeepsTheStoreOffDisk(t *testing.T) {
	keeper := manifestOnly{ID: "alfred", Version: "1.0.0", Storage: storage.Spec{Keyspace: true}}
	for _, tc := range []struct {
		name   string
		fs     storage.FS
		onDisk bool
	}{
		{"in-memory", storage.MemFS(), false},
		{"os", storage.FS{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := Options{AppID: "gotham", StateDir: t.TempDir(), Args: []string{}, Env: []string{}, StorageFS: tc.fs}
			a, err := New(t.Context(), opts, keeper)
			if err != nil {
				t.Fatal(err)
			}
			if err := a.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			_, err = os.Stat(filepath.Join(opts.StateDir, "storage", "main.pebble"))
			if onDisk := err == nil; onDisk != tc.onDisk {
				t.Fatalf("store on disk = %v (%v), want %v", onDisk, err, tc.onDisk)
			}
		})
	}
}
