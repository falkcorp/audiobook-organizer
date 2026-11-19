// file: core/kernel/config_boot_test.go
// version: 1.0.0
// guid: d2618bbb-991d-4090-bcf6-a00ef3f98c75
// last-edited: 2026-11-18

package kernel

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
)

// gatedConfig is the swap host's Config with Config.CoreConfigBoot on and no
// ambient environment: the operator layers are exactly env.
func (h *swapHost) gatedConfig(deps *module.Deps, env ...string) Config {
	c := h.config(deps)
	c.CoreConfigBoot = true
	c.ConfigOptions = config.Options{Environ: append([]string{}, env...)}
	c.StateDirDefault = h.state
	return c
}

func ownerIDs(k *Kernel) []storage.Owner {
	var ids []storage.Owner
	for _, o := range k.storageOwners() {
		ids = append(ids, o.Owner)
	}
	return ids
}

// TestConfigBoot_GateOffIsTheOldBoot pins what ABO's boot is with the gate
// off: storageOwners is exactly audit, tasks and the modules, no layered store
// is built, the core config is never built (it is lazy and a boot with no task
// never reads it), and no keyring or c/config/ or c/secrets/ keyspace exists.
// The gate-on twin is TestConfigBoot_GateOnClaimsBothOwners.
func TestConfigBoot_GateOffIsTheOldBoot(t *testing.T) {
	h := newSwapHost(t)
	k := h.mustBoot(h.config(nil))
	want := []storage.Owner{"core/audit", "core/tasks", "alfred"}
	if got := ownerIDs(k); !slices.Equal(got, want) {
		t.Fatalf("storageOwners = %v, want %v", got, want)
	}
	if k.cfgBoot != nil || k.coreCfg != nil || k.settingHandles != nil {
		t.Fatalf("a gate-off boot built config: cfgBoot=%v coreCfg=%v", k.cfgBoot, k.coreCfg)
	}
	for _, o := range []storage.Owner{configOwnerSpec().Owner, secretsOwnerID} {
		if _, ok := k.store.db.Keyspace(o); ok {
			t.Errorf("a gate-off boot opened the %s keyspace", o)
		}
	}
	if _, err := os.Stat(filepath.Join(h.state, "secrets.keyring")); !os.IsNotExist(err) {
		t.Fatalf("a gate-off boot created the keyring: %v", err)
	}
}

// TestConfigBoot_GateOnClaimsBothOwners: with the gate on the store claims
// c/config/ and c/secrets/ at its first P4b, after audit and tasks, and the
// layered store is the kernel's (P0 ran, P3 loaded).
func TestConfigBoot_GateOnClaimsBothOwners(t *testing.T) {
	h := newSwapHost(t)
	k := h.mustBoot(h.gatedConfig(nil))
	want := []storage.Owner{"core/audit", "core/tasks", configOwnerSpec().Owner, secretsOwnerID, "alfred"}
	if got := ownerIDs(k); !slices.Equal(got, want) {
		t.Fatalf("storageOwners = %v, want %v", got, want)
	}
	if k.cfgBoot == nil {
		t.Fatal("the gate is on and P0 built no config store")
	}
	for _, o := range []storage.Owner{configOwnerSpec().Owner, secretsOwnerID} {
		if _, ok := k.store.db.Keyspace(o); !ok {
			t.Errorf("the gate-on boot has no %s keyspace", o)
		}
	}
	if k.cfg.StateDir != h.state || k.cfgBoot.stateDir != h.state {
		t.Errorf("state dir = %q / %q, want %q", k.cfg.StateDir, k.cfgBoot.stateDir, h.state)
	}
}

// TestConfigBoot_StoragePath: core.storage.path comes from the pre-boot
// layers (here the environment), which replace the app's own resolution; with
// none the compiled default stands; a layer and a compiled path naming two
// stores refuse the boot; so does a relative path.
func TestConfigBoot_StoragePath(t *testing.T) {
	t.Run("layer replaces the default", func(t *testing.T) {
		h := newSwapHost(t)
		moved := filepath.Join(h.state, "elsewhere", "main.pebble")
		c := h.gatedConfig(nil, "GOTHAM_CORE_STORAGE_PATH="+moved)
		c.StoragePath = ""
		k := h.mustBoot(c)
		if k.cfg.StoragePath != moved {
			t.Fatalf("StoragePath = %q, want %q", k.cfg.StoragePath, moved)
		}
		if _, err := os.Stat(moved); err != nil {
			t.Fatalf("the store is not at the layer's path: %v", err)
		}
	})
	t.Run("no layer: the compiled default, then the state directory", func(t *testing.T) {
		h := newSwapHost(t)
		if k := h.mustBoot(h.gatedConfig(nil)); k.cfg.StoragePath != h.db {
			t.Fatalf("StoragePath = %q, want the compiled %q", k.cfg.StoragePath, h.db)
		}
		h2 := newSwapHost(t)
		c := h2.gatedConfig(nil)
		c.StoragePath = ""
		if k := h2.mustBoot(c); k.cfg.StoragePath != h2.db {
			t.Fatalf("StoragePath = %q, want <state>/storage/main.pebble %q", k.cfg.StoragePath, h2.db)
		}
	})
	t.Run("two stores", func(t *testing.T) {
		h := newSwapHost(t)
		c := h.gatedConfig(nil, "GOTHAM_CORE_STORAGE_PATH="+filepath.Join(h.state, "other.pebble"))
		if _, err := h.boot(c); err == nil || !strings.Contains(err.Error(), "name different stores") {
			t.Fatalf("boot = %v, want the two-stores refusal", err)
		}
	})
	t.Run("relative", func(t *testing.T) {
		h := newSwapHost(t)
		c := h.gatedConfig(nil, "GOTHAM_CORE_STORAGE_PATH=rel/main.pebble")
		c.StoragePath = ""
		if _, err := h.boot(c); err == nil || !strings.Contains(err.Error(), "not an absolute path") {
			t.Fatalf("boot = %v, want the absolute-path refusal", err)
		}
	})
}

// TestConfigBoot_OneStateDirResolver: an app whose own resolution of the state
// directory differs from step 0's fails the boot.
func TestConfigBoot_OneStateDirResolver(t *testing.T) {
	h := newSwapHost(t)
	c := h.gatedConfig(nil)
	c.StateDir = filepath.Join(filepath.Dir(h.state), "another")
	if _, err := h.boot(c); err == nil || !strings.Contains(err.Error(), "one resolver must own it") {
		t.Fatalf("boot = %v, want the two-resolvers refusal", err)
	}
}

// TestConfigBoot_NoStorageApp: an app that declares no storage keeps what it
// passed (no store to swap, no keyring to keep), and the layered store still
// serves its sections and module enablement.
func TestConfigBoot_NoStorageApp(t *testing.T) {
	var got *config.Handle[utilityBelt]
	m := beltModule(func(r *module.Registrar) error {
		got = module.Setting(r, beltSection)
		return r.Err()
	}, module.Settings(beltSection))
	c := testConfig(t, m)
	c.CoreConfigBoot = true
	c.StateDirDefault = filepath.Join(t.TempDir(), "state")
	c.ConfigOptions = config.Options{Environ: []string{"GOTHAM_BATCAVE_BELT_GRAPPLE=9"}}
	k := mustNew(t, c)
	if k.store != nil || k.cfg.StoragePath != "" || k.cfg.StateDir != "" {
		t.Fatalf("a no-storage app got a store or paths: store=%v path=%q state=%q", k.store, k.cfg.StoragePath, k.cfg.StateDir)
	}
	if got == nil || got.Get().Grapple != 9 {
		t.Fatalf("the module's Setting = %v, want the layered value 9", got)
	}
}

// TestConfigBoot_ModuleSections: an enabled module's declared sections
// register at P3 and its module.Setting reads the live handle, so an
// operator layer's value reaches it; a module a layer disables registers
// nothing, is not started, and reports Disabled "config".
func TestConfigBoot_ModuleSections(t *testing.T) {
	var live *config.Handle[utilityBelt]
	registered := map[string]bool{}
	batcave := beltModule(func(r *module.Registrar) error {
		registered["batcave"] = true
		live = module.Setting(r, beltSection)
		return r.Err()
	}, module.Settings(beltSection))
	arkhamSection := config.Define("modules.arkham.cells", utilityBelt{Grapple: 1})
	arkham := mod("arkham", func(r *module.Registrar) error { registered["arkham"] = true; return r.Err() }).
		with(func(m *module.Manifest) { m.Settings = []module.SettingsSection{module.Settings(arkhamSection)} })
	h := newSwapHost(t)
	c := h.gatedConfig(nil, "GOTHAM_BATCAVE_BELT_CORD=nylon", "GOTHAM_CORE_MODULES_ARKHAM_ENABLED=false")
	c.Modules = append(c.Modules, batcave, arkham)
	k := h.mustBoot(c)
	if live == nil || live.Get().Cord != "nylon" || live.Get().Grapple != 3 {
		t.Fatalf("batcave's Setting = %v, want cord nylon over the default grapple 3", live)
	}
	if registered["arkham"] {
		t.Error("a module the environment disabled registered")
	}
	if _, ok := k.cfgBoot.store.Lookup("modules.arkham.cells.grapple"); ok {
		t.Error("a disabled module's section registered with the store")
	}
	for _, s := range k.Modules() {
		if s.ID == "arkham" && (s.Enabled || s.Disabled != disabledByConfig) {
			t.Errorf("arkham = %+v, want disabled by config", s)
		}
		if s.ID == "batcave" && !s.Enabled {
			t.Errorf("batcave = %+v, want enabled", s)
		}
	}
}

// TestConfigBoot_CoreConfigIsTheLayeredStore: the tasks wiring reads the
// booted store, so an operator value reaches the executor's config, and
// closing the kernel closes that store once.
func TestConfigBoot_CoreConfigIsTheLayeredStore(t *testing.T) {
	h := newSwapHost(t)
	k := h.mustBoot(h.gatedConfig(nil, "GOTHAM_CORE_TASKS_WORKERS=7"))
	cc, err := k.coreConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !cc.shared || cc.store != k.cfgBoot.store {
		t.Fatal("the core config is not the layered store")
	}
	if got := cc.executorConfig().Workers; got != 7 {
		t.Fatalf("executor workers = %d, want the environment's 7", got)
	}
	if err := k.closeCore(context.Background(), time.Now().Add(defaultShutdownBudget)); err != nil {
		t.Fatalf("closeCore: %v", err)
	}
}

// TestConfigBoot_DescribeDoesNoIO: Describe with the gate on builds no store
// and creates no state directory.
func TestConfigBoot_DescribeDoesNoIO(t *testing.T) {
	h := newSwapHost(t)
	c := h.gatedConfig(nil)
	if _, err := Describe(h.ctx, c); err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if _, err := os.Stat(h.state); !os.IsNotExist(err) {
		t.Fatalf("Describe created the state directory: %v", err)
	}
}
