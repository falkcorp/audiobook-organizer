// file: core/kernel/wants_store_test.go
// version: 0.1.0
// guid: c813fb90-464d-4cc2-9a76-fc785bd890ef
// last-edited: 2026-11-21

package kernel

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/internal/authstore"
	"github.com/falkcorp/mediacore/internal/rbacstore"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
)

// This file is F4-08 B5b: a declared identity boot makes the app have a store
// (Config.WantsStore), however many modules declare storage, and every site
// that asks "does this app have a store" asks that one predicate. The sites
// that ask "does this module declare storage" (a module's owner, its handles)
// keep DeclaresStorage and are unchanged by the identity boot.

// identityOnly is a Config for the Gotham app with the identity boot declared
// and the given modules, over a fresh state directory and no store path: P0
// resolves both.
func identityOnly(t *testing.T, on bool, mods ...module.Module) Config {
	t.Helper()
	c := testConfig(t, mods...)
	c.IdentityBoot = on
	c.StateDirDefault = filepath.Join(t.TempDir(), "state")
	return c
}

// TestWantsStore_Predicate is the one predicate over its inputs: modules
// declaring storage or not, the identity switch on or off.
func TestWantsStore_Predicate(t *testing.T) {
	none := []module.Module{}
	plain := []module.Module{mod("lucius", nil)}
	keeper := []module.Module{mod("lucius", nil), storageMod("alfred", nil)}
	for _, tc := range []struct {
		name string
		mods []module.Module
		on   bool
		want bool
	}{
		{"no modules, switch off", none, false, false},
		{"storage-less module, switch off", plain, false, false},
		{"storage module, switch off", keeper, false, true},
		{"no modules, identity boot", none, true, true},
		{"storage-less module, identity boot", plain, true, true},
		{"storage module, identity boot", keeper, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := (Config{Modules: tc.mods, IdentityBoot: tc.on}).WantsStore(); got != tc.want {
				t.Errorf("WantsStore = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestWantsStore_IdentityBootWithNoStorageModuleBoots: the gap B5 found. An app
// that declared the identity boot and has no module declaring storage opens the
// store at P0 (under <state dir>/storage/main.pebble, as for a storage module)
// and the boot reaches P8 instead of failing at P5 with "needs the stores".
func TestWantsStore_IdentityBootWithNoStorageModuleBoots(t *testing.T) {
	for name, mods := range map[string][]module.Module{
		"zero modules":         nil,
		"storage-less modules": {mod("lucius", nil)},
	} {
		t.Run(name, func(t *testing.T) {
			c := identityOnly(t, true, mods...)
			k := mustNew(t, c)
			want := filepath.Join(c.StateDirDefault, "storage", "main.pebble")
			if k.store == nil || k.store.db == nil || k.cfg.StoragePath != want {
				t.Fatalf("store = %+v at %q, want one open at %q", k.store, k.cfg.StoragePath, want)
			}
			if k.cfg.StateDir != c.StateDirDefault {
				t.Errorf("state directory = %q, want %q", k.cfg.StateDir, c.StateDirDefault)
			}
			for _, o := range []storage.Owner{authstore.Owner, rbacstore.Owner} {
				if _, ok := k.store.db.Keyspace(o); !ok {
					t.Errorf("the identity boot's store has no %s keyspace", o)
				}
			}
			if k.ident == nil || k.ident.users == nil || k.ident.roles == nil {
				t.Fatalf("P4b built no identity stores: %+v", k.ident)
			}
			if _, _, ok := k.IdentityServices(); !ok {
				t.Error("the boot built no identity services")
			}
			// A storage-less module is served, not held for "storage": the
			// held check is module-level (DeclaresStorage), not the app's.
			for _, e := range k.entries {
				if !e.enabled || e.held != "" {
					t.Errorf("module %s: enabled=%v held=%q after the boot, want enabled and not held", e.man.ID, e.enabled, e.held)
				}
			}
			if !k.swapWanted(false) {
				t.Error("P0a's swap is not wanted for an app with a store")
			}
		})
	}
}

// TestWantsStore_SwitchOffNoStorageModuleHasNoStore: with the switch off the
// same apps have no store, as before F4-08: nothing opened, no path, no state
// directory, no swap.
func TestWantsStore_SwitchOffNoStorageModuleHasNoStore(t *testing.T) {
	for name, mods := range map[string][]module.Module{
		"zero modules":         nil,
		"storage-less modules": {mod("lucius", nil)},
	} {
		t.Run(name, func(t *testing.T) {
			c := identityOnly(t, false, mods...)
			k := mustNew(t, c)
			if k.store != nil || k.cfg.StoragePath != "" || k.cfg.StateDir != "" {
				t.Fatalf("a switch-off app with no storage module has a store: %+v, path %q, state %q", k.store, k.cfg.StoragePath, k.cfg.StateDir)
			}
			if k.swapWanted(false) {
				t.Error("P0a's swap is wanted for an app with no store")
			}
			if k.ident != nil {
				t.Errorf("a switch-off boot built identity state: %+v", k.ident)
			}
		})
	}
}

// TestWantsStore_OwnersAreModuleLevel: the owner list is core's own plus the
// identity three plus one owner per module that DECLARES storage. A storage-less
// module in an identity app has no owner of its own (the identity boot gives the
// app a store, not the module a keyspace), and a storage module keeps its.
func TestWantsStore_OwnersAreModuleLevel(t *testing.T) {
	k := mustNew(t, identityOnly(t, true, mod("lucius", nil), storageMod("alfred", nil)))
	owners := ownerIDs(k)
	if slices.Contains(owners, "lucius") {
		t.Errorf("storageOwners = %v: the storage-less module has an owner", owners)
	}
	if !slices.Contains(owners, "alfred") || !slices.Contains(owners, authstore.Owner) || !slices.Contains(owners, rbacstore.Owner) {
		t.Errorf("storageOwners = %v, want alfred and the identity owners", owners)
	}
	if _, ok := k.store.db.Keyspace("lucius"); ok {
		t.Error("the storage-less module has a keyspace")
	}
}

// TestWantsStore_ResolveStorageAgreesWithTheBoot: the offline resolution
// (ResolveStorage) and the boot's P0 answer the same: a path and a state
// directory for an identity app of no storage module, none with the switch off.
func TestWantsStore_ResolveStorageAgreesWithTheBoot(t *testing.T) {
	c := identityOnly(t, true)
	c = withState(t, c)
	path, dir, err := ResolveStorage(t.Context(), c, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(c.StateDirDefault, "storage", "main.pebble"); path != want || dir != c.StateDirDefault {
		t.Errorf("ResolveStorage = %q, %q; want %q, %q", path, dir, want, c.StateDirDefault)
	}
	booted := mustNew(t, c)
	if booted.cfg.StoragePath != path || booted.cfg.StateDir != dir {
		t.Errorf("the boot resolved %q, %q; ResolveStorage %q, %q", booted.cfg.StoragePath, booted.cfg.StateDir, path, dir)
	}

	off := withState(t, identityOnly(t, false))
	if p, d, err := ResolveStorage(t.Context(), off, ""); err != nil || p != "" || d != "" {
		t.Errorf("ResolveStorage with the switch off = %q, %q, %v; want none", p, d, err)
	}
}

// TestWantsStore_OfflineOpenOfAnIdentityApp: the offline open sees the identity
// app's store (it exists, from a boot) and refuses, by name, an app that has
// none; Offline.Storage stays module-level, so a storage-less module has no
// handles to hand out even though the app has a store.
func TestWantsStore_OfflineOpenOfAnIdentityApp(t *testing.T) {
	c := withState(t, identityOnly(t, true, mod("lucius", nil), storageMod("alfred", nil)))
	k := mustNew(t, c)
	if err := k.closeCore(context.Background(), time.Now().Add(defaultShutdownBudget)); err != nil {
		t.Fatal(err)
	}
	var err error
	if c.StoragePath, c.StateDir, err = ResolveStorage(t.Context(), c, ""); err != nil {
		t.Fatal(err)
	}
	o, err := OpenOffline(t.Context(), c, true)
	if err != nil {
		t.Fatalf("offline open of the identity app's store: %v", err)
	}
	t.Cleanup(func() { _ = o.Close() })
	if _, err := o.Storage("alfred"); err != nil {
		t.Errorf("Storage(alfred) = %v, want its handles", err)
	}
	if _, err := o.Storage("lucius"); err == nil || !strings.Contains(err.Error(), "declares no storage") {
		t.Errorf("Storage(lucius) = %v, want it to say the module declares no storage", err)
	}

	// The same app with the switch off and no storage module has no store to open.
	off := withState(t, identityOnly(t, false, mod("lucius", nil)))
	if off.StoragePath, off.StateDir, err = ResolveStorage(t.Context(), off, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenOffline(t.Context(), off, true); err == nil || !strings.Contains(err.Error(), "no store to open offline") {
		t.Errorf("offline open with no store = %v, want the refusal that there is no store to open", err)
	}
}
