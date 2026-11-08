// file: core/app/storage_test.go
// version: 1.0.0
// guid: cd83307e-c3ce-4393-b440-9d8ba60a7afb
// last-edited: 2026-11-07

package app

import (
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
// directory.
func TestStoragePath_Rule(t *testing.T) {
	t.Chdir(t.TempDir())
	plain := manifestOnly{ID: "alfred", Version: "1.0.0"}
	keeper := manifestOnly{ID: "alfred", Version: "1.0.0", Storage: storage.Spec{Keyspace: true}}
	opts := Options{AppID: "gotham", StateDir: "gotham-state", Env: []string{}}

	if p, err := storagePath(opts, []module.Module{plain}); err != nil || p != "" {
		t.Fatalf("no module declares storage: path %q, %v; want none", p, err)
	}
	if _, err := os.Stat("gotham-state"); !os.IsNotExist(err) {
		t.Fatalf("an app without storage created its state directory: %v", err)
	}

	p, err := storagePath(opts, []module.Module{plain, keeper})
	wd, _ := os.Getwd()
	if want := filepath.Join(wd, "gotham-state", "storage", "main.pebble"); err != nil || p != want {
		t.Fatalf("default path = %q, %v; want %q", p, err, want)
	}

	abs := filepath.Join(t.TempDir(), "batcave.pebble")
	opts.Env = []string{"GOTHAM_CORE_STORAGE_PATH=" + abs}
	if p, err := storagePath(opts, []module.Module{keeper}); err != nil || p != abs {
		t.Fatalf("env path = %q, %v; want %q", p, err, abs)
	}

	opts.Env = []string{"GOTHAM_CORE_STORAGE_PATH=batcave.pebble"}
	if _, err := storagePath(opts, []module.Module{keeper}); err == nil || !strings.Contains(err.Error(), "not an absolute path") {
		t.Fatalf("relative env path = %v, want a refusal", err)
	}
}
