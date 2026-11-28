// file: core/app/wants_store_test.go
// version: 0.1.0
// guid: d8ac972a-d8e5-4160-9aed-d9a60b3ceccb
// last-edited: 2026-11-21

package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
)

// F4-08 B5b: an app that declared Options.IdentityBoot has a store whether or
// not a module declares storage, and package app's sites (the claims mode and
// state-file table New and the offline commands read, and the path resolution)
// ask kernel.Config.WantsStore, the one predicate the kernel asks too.

// identityOpts are Options over a fresh state directory and an in-memory store,
// with the identity boot declared.
func identityOpts(t *testing.T, on bool, env ...string) Options {
	t.Helper()
	if env == nil {
		env = []string{}
	}
	return Options{AppID: "gotham", MetricNamespace: "gotham", StateDir: t.TempDir(), Args: []string{}, Env: env,
		StorageFS: storage.MemFS(), IdentityBoot: on}
}

// TestWantsStore_NewBootsAnIdentityAppWithNoStorageModule: zero modules and
// storage-less modules both get the store the identity boot needs; with the
// switch off they get none, as before.
func TestWantsStore_NewBootsAnIdentityAppWithNoStorageModule(t *testing.T) {
	plain := manifestOnly{ID: "lucius", Version: "1.0.0", DefaultEnabled: true}
	for name, mods := range map[string][]module.Module{"zero modules": nil, "storage-less module": {plain}} {
		t.Run(name, func(t *testing.T) {
			a, err := New(t.Context(), identityOpts(t, true), mods...)
			if err != nil {
				t.Fatalf("an identity app with no storage module did not boot: %v", err)
			}
			defer func() { _ = a.Close(context.Background()) }()
			if a.StorageAdmin() == nil {
				t.Error("StorageAdmin() = nil: the identity app has no store")
			}
			off, err := New(t.Context(), identityOpts(t, false), mods...)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = off.Close(context.Background()) }()
			if off.StorageAdmin() != nil {
				t.Error("StorageAdmin() != nil with the switch off and no storage module")
			}
		})
	}
}

// TestWantsStore_NewReadsTheClaimsModeForAnIdentityApp: New reads
// core.storage.claims.mode for every app that has a store, so a mistyped dial
// fails an identity app's boot with no storage module through claimsShadow, which
// an app that has no store never calls.
func TestWantsStore_NewReadsTheClaimsModeForAnIdentityApp(t *testing.T) {
	bad := "GOTHAM_CORE_STORAGE_CLAIMS_MODE=lenient"
	a, err := New(t.Context(), identityOpts(t, true, bad))
	if err == nil {
		_ = a.Close(context.Background())
		t.Fatal("an identity app accepted an unknown claims mode")
	}
	if !strings.Contains(err.Error(), "not a claims mode") {
		t.Errorf("error = %v, want the claims-mode refusal", err)
	}
	// An app with no store never reaches claimsShadow; the config layer still
	// refuses the value, in its own words, exactly as before F4-08.
	a, err = New(t.Context(), identityOpts(t, false, bad))
	if err == nil {
		_ = a.Close(context.Background())
		t.Fatal("an app with no store accepted an unknown claims mode")
	}
	if strings.Contains(err.Error(), "not a claims mode") {
		t.Errorf("an app with no store read the claims mode itself: %v", err)
	}
}

// TestWantsStore_OfflineConfigReadsTheClaimsModeForAnIdentityApp: the offline
// commands' config applies the same rule as New: the claims mode and the
// state-file table are set for an identity app of no storage module.
func TestWantsStore_OfflineConfigReadsTheClaimsModeForAnIdentityApp(t *testing.T) {
	files := []storage.StateFile{{Label: "batcave key", Path: "batcave.key"}}
	on := identityOpts(t, true, "GOTHAM_CORE_STORAGE_CLAIMS_MODE=shadow")
	on.StorageStateFiles = files
	c, err := offlineConfig(t.Context(), on, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if !c.StorageClaimsShadow || len(c.StorageStateFiles) != 1 {
		t.Errorf("offline config of an identity app: shadow=%v, state files %v; want shadow and %v", c.StorageClaimsShadow, c.StorageStateFiles, files)
	}
	if want := filepath.Join(on.StateDir, "storage", "main.pebble"); c.StoragePath != want {
		t.Errorf("offline config store path = %q, want %q", c.StoragePath, want)
	}
	off := identityOpts(t, false, "GOTHAM_CORE_STORAGE_CLAIMS_MODE=shadow")
	off.StorageStateFiles = files
	c, err = offlineConfig(t.Context(), off, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if c.StorageClaimsShadow || c.StorageStateFiles != nil || c.StoragePath != "" {
		t.Errorf("offline config with the switch off and no storage module: shadow=%v, files %v, path %q; want none", c.StorageClaimsShadow, c.StorageStateFiles, c.StoragePath)
	}
}

// TestWantsStore_ResolveStorageForAnIdentityApp: the exported resolution
// agrees with the boot: a path for an identity app of no storage module.
func TestWantsStore_ResolveStorageForAnIdentityApp(t *testing.T) {
	opts := identityOpts(t, true)
	p, dir, err := ResolveStorage(t.Context(), opts, nil, "")
	if want := filepath.Join(opts.StateDir, "storage", "main.pebble"); err != nil || p != want || dir != opts.StateDir {
		t.Fatalf("ResolveStorage = %q, %q, %v; want %q in %q", p, dir, err, want, opts.StateDir)
	}
	if p, dir, err := ResolveStorage(t.Context(), identityOpts(t, false), nil, ""); err != nil || p != "" || dir != "" {
		t.Fatalf("ResolveStorage with the switch off = %q, %q, %v; want none", p, dir, err)
	}
}

// TestWantsStore_FastExitStaysModuleLevel: --print-storage-levels lists the
// owners of the modules that DECLARE storage; it cannot see the identity switch
// (FastExit takes no Options), and a storage-less module adds no owner.
func TestWantsStore_FastExitStaysModuleLevel(t *testing.T) {
	plain := manifestOnly{ID: "lucius", Version: "1.0.0"}
	keeper := manifestOnly{ID: "alfred", Version: "1.0.0", Storage: storage.Spec{Keyspace: true}}
	line := func(mods ...module.Module) string {
		var out strings.Builder
		if handled, err := FastExit([]string{"x", "--print-storage-levels"}, &out, mods...); !handled || err != nil {
			t.Fatalf("FastExit = %v, %v", handled, err)
		}
		return out.String()
	}
	if got, want := line(plain), line(); got != want {
		t.Errorf("a storage-less module changed the storage levels: %q vs %q", got, want)
	}
	if line(keeper) == line() {
		t.Error("a module that declares storage left the storage levels unchanged")
	}
}
