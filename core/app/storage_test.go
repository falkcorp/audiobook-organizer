// file: core/app/storage_test.go
// version: 1.1.0
// guid: cd83307e-c3ce-4393-b440-9d8ba60a7afb
// last-edited: 2026-11-08

package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
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

// TestStoragePath_R1StoragePath: the app's own database path names the store
// when the env is unset, and the env naming a different store fails rather
// than letting serve and the app's offline commands open two databases.
func TestStoragePath_R1StoragePath(t *testing.T) {
	keeper := manifestOnly{ID: "alfred", Version: "1.0.0", Storage: storage.Spec{Keyspace: true}}
	cave := filepath.Join(t.TempDir(), "batcave.pebble")
	opts := Options{AppID: "gotham", StateDir: t.TempDir(), Env: []string{}, R1StoragePath: cave}
	if p, _, err := storagePath(opts, []module.Module{keeper}); err != nil || p != cave {
		t.Fatalf("R1StoragePath alone = %q, %v; want %q", p, err, cave)
	}
	opts.Env = []string{"GOTHAM_CORE_STORAGE_PATH=" + cave + "/"}
	if p, _, err := storagePath(opts, []module.Module{keeper}); err != nil || p != cave {
		t.Fatalf("env and R1StoragePath naming one store = %q, %v; want %q", p, err, cave)
	}
	opts.Env = []string{"GOTHAM_CORE_STORAGE_PATH=" + filepath.Join(t.TempDir(), "arkham.pebble")}
	if _, _, err := storagePath(opts, []module.Module{keeper}); err == nil || !strings.Contains(err.Error(), "name different stores") {
		t.Fatalf("env and R1StoragePath naming two stores = %v, want a refusal", err)
	}
	opts.Env, opts.R1StoragePath = []string{}, "batcave.pebble"
	if _, _, err := storagePath(opts, []module.Module{keeper}); err == nil || !strings.Contains(err.Error(), "not an absolute path") {
		t.Fatalf("relative R1StoragePath = %v, want a refusal", err)
	}
}

// outOfClaim is a module with one legacy claim whose Register tries a write
// outside it, recording what storage answered.
type outOfClaim struct{ got *error }

func (outOfClaim) Manifest() module.Manifest {
	return module.Manifest{ID: "joker", Version: "1.0.0", DefaultEnabled: true,
		Storage: storage.Spec{Claims: []storage.Claim{{Prefix: "joker:", Mode: storage.ClaimReadWrite}}}}
}

func (m outOfClaim) Register(r *module.Registrar) error {
	*m.got = r.Deps().Storage.Legacy.Set(r.Context(), []byte("bane:1"), []byte("venom"))
	return nil
}

// TestClaimsModeDial: <EnvPrefix>_CORE_STORAGE_CLAIMS_MODE reaches the store.
// Unset and "strict" refuse an out-of-claim write and count it as
// mode="refused"; "shadow" allows it and counts it as mode="shadow", on
// <namespace>_storage_claim_violations_total{owner,op,mode} (05-storage §5S.8);
// any other value fails New before anything opens.
func TestClaimsModeDial(t *testing.T) {
	for _, tc := range []struct {
		env     string
		allowed bool
		mode    string
	}{
		{"", false, "refused"},
		{"strict", false, "refused"},
		{"shadow", true, "shadow"},
	} {
		t.Run("mode="+tc.env, func(t *testing.T) {
			var got error
			env := []string{}
			if tc.env != "" {
				env = []string{"GOTHAM_CORE_STORAGE_CLAIMS_MODE=" + tc.env}
			}
			opts := Options{AppID: "gotham", MetricNamespace: "gotham", StateDir: t.TempDir(), Args: []string{}, Env: env, StorageFS: storage.MemFS()}
			a, err := New(t.Context(), opts, outOfClaim{got: &got})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = a.Close(context.Background()) }()
			if allowed := got == nil; allowed != tc.allowed {
				t.Fatalf("out-of-claim Set = %v, allowed %v; want allowed %v", got, allowed, tc.allowed)
			}
			if !tc.allowed && !errors.Is(got, storage.ErrOutsideKeyspace) {
				t.Fatalf("strict refusal = %v, want ErrOutsideKeyspace", got)
			}
			if a.StorageAdmin() == nil {
				t.Fatal("StorageAdmin() = nil with a store open")
			}
			fams, err := a.Metrics().Gatherer().Gather()
			if err != nil {
				t.Fatal(err)
			}
			served := map[string]string{}
			for _, f := range fams {
				if f.GetName() != "gotham_storage_claim_violations_total" {
					continue
				}
				for _, m := range f.GetMetric() {
					labels := map[string]string{}
					for _, l := range m.GetLabel() {
						labels[l.GetName()] = l.GetValue()
					}
					key := labels["owner"] + "/" + labels["op"] + "/" + labels["mode"]
					served[key] = strconv.FormatFloat(m.GetCounter().GetValue(), 'f', -1, 64)
				}
			}
			want := map[string]string{"joker/write/" + tc.mode: "1"}
			if len(served) != 1 || served["joker/write/"+tc.mode] != "1" {
				t.Fatalf("gotham_storage_claim_violations_total = %v, want %v", served, want)
			}
		})
	}
	opts := Options{AppID: "gotham", StateDir: t.TempDir(), Args: []string{}, Env: []string{"GOTHAM_CORE_STORAGE_CLAIMS_MODE=lenient"}, StorageFS: storage.MemFS()}
	var got error
	if _, err := New(t.Context(), opts, outOfClaim{got: &got}); err == nil || !strings.Contains(err.Error(), "not a claims mode") {
		t.Fatalf("an unknown claims mode = %v, want New to fail", err)
	}
}

// TestStorageAdminNilWithoutAStore: an app with no storage has no admin.
func TestStorageAdminNilWithoutAStore(t *testing.T) {
	plain := manifestOnly{ID: "alfred", Version: "1.0.0"}
	a, err := New(t.Context(), Options{AppID: "gotham", Args: []string{}, Env: []string{}}, plain)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close(context.Background()) }()
	if a.StorageAdmin() != nil {
		t.Fatal("StorageAdmin() != nil with no store")
	}
}
