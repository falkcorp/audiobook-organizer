// file: core/kernel/config_boot_test.go
// version: 1.3.0
// guid: d2618bbb-991d-4090-bcf6-a00ef3f98c75
// last-edited: 2026-11-19

package kernel

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
	"time"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/internal/configstore"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
	"github.com/falkcorp/mediacore/storage/restorejournal"
	"github.com/falkcorp/mediacore/tasks"
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

// TestConfigBoot_P4bOpensTheSecretStore: the gate-on boot opens the secret
// store at the start of P4b (the keyring appears with the store's install
// marker in its header), binds both Importers, and hands each module a Secrets
// scope under modules.<id>. The gate-off twin is TestSecretsOwnerSpec's "a boot
// created no keyring" and TestOffline...OpensNoStore in package app.
func TestConfigBoot_P4bOpensTheSecretStore(t *testing.T) {
	h := newSwapHost(t)
	var deps module.Deps
	k := h.mustBoot(h.gatedConfig(&deps))
	kr, err := os.ReadFile(filepath.Join(h.state, "secrets.keyring"))
	if err != nil {
		t.Fatalf("the gate-on boot wrote no keyring: %v", err)
	}
	if !bytes.Contains(kr, []byte(`"install_id": "`+k.store.db.Admin().InstallID()+`"`)) {
		t.Fatal("the keyring header does not carry the store's install marker")
	}
	if k.sec == nil || k.importerCfg == nil || k.importerSec == nil {
		t.Fatalf("P4b bound sec=%v importerCfg=%v importerSec=%v", k.sec, k.importerCfg, k.importerSec)
	}
	ctx := t.Context()
	if _, err := deps.Secrets.Put(ctx, "modules.alfred.signal_key", secrets.NewValue(secretsFixture), "user:alfred"); err != nil {
		t.Fatalf("a module's own secret: %v", err)
	}
	if _, err := deps.Secrets.Put(ctx, "modules.bane.venom", secrets.NewValue(secretsFixture), "user:alfred"); !errors.Is(err, secrets.ErrOutOfScope) {
		t.Fatalf("another module's name through alfred's scope: %v, want ErrOutOfScope", err)
	}
}

// appConfigHost boots two modules over a gate-on kernel: alfred, which
// declares no AppConfig, and lucius, which does and admits only names under
// "integrations.batcave.". It returns each module's Deps.
func appConfigHost(t *testing.T, gateOn bool, env ...string) (k *Kernel, alfred, lucius module.Deps) {
	t.Helper()
	h := newSwapHost(t)
	c := h.config(&alfred)
	if gateOn {
		c = h.gatedConfig(&alfred, env...)
		c.ConfigOptions.Passthrough = func(p config.Path) bool { return p == "integrations.batcave.alfred.host" }
	}
	c.Modules = append(c.Modules, mod("lucius", func(r *module.Registrar) error {
		lucius = r.Deps()
		return nil
	}).with(func(m *module.Manifest) {
		m.AppConfig = &module.AppConfigSpec{Secrets: func(n secrets.Name) bool {
			return strings.HasPrefix(string(n), "integrations.batcave.")
		}}
	}))
	return h.mustBoot(c), alfred, lucius
}

// TestConfigBoot_DepsConfigNeedsTheManifestLine: a module that does not
// declare Manifest.AppConfig gets a nil Deps.Config under a gate-on boot, so
// the ambient reach is nil, and a gate-off boot hands even the declaring
// module none.
func TestConfigBoot_DepsConfigNeedsTheManifestLine(t *testing.T) {
	_, alfred, lucius := appConfigHost(t, true)
	if alfred.Config != nil {
		t.Fatalf("a module with no AppConfig got a config store: %T", alfred.Config)
	}
	if lucius.Config == nil {
		t.Fatal("the module declaring AppConfig got no config store")
	}
	_, alfred, lucius = appConfigHost(t, false)
	if alfred.Config != nil || lucius.Config != nil {
		t.Fatalf("a gate-off boot handed out a config store: alfred=%v lucius=%v", alfred.Config, lucius.Config)
	}
}

// TestConfigBoot_DepsConfigIsTheLayeredStore: the declaring module's
// Deps.Config reads the effective value, lists the keys, and its Apply
// commits, moves the generation and fires OnCommit once with it.
func TestConfigBoot_DepsConfigIsTheLayeredStore(t *testing.T) {
	_, _, lucius := appConfigHost(t, true)
	cfg := lucius.Config
	const key config.Path = "core.shutdown.budget"
	st, ok := cfg.Lookup(key)
	if !ok || len(st.Effective) == 0 {
		t.Fatalf("Lookup(%s) = %+v, %v", key, st, ok)
	}
	var seen int
	for range cfg.Keys() {
		seen++
	}
	if seen == 0 {
		t.Fatal("Keys is empty")
	}
	var fired []uint64
	cfg.OnCommit(func(gen uint64) { fired = append(fired, gen) })
	before := cfg.Generation()
	res, err := cfg.Apply(t.Context(), config.Patch{Set: map[config.Path]json.RawMessage{key: json.RawMessage(`"45s"`)}},
		config.Actor{Subject: "user:alfred", Interactive: true, Can: func(string) bool { return true }})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.Generation <= before || cfg.Generation() != res.Generation {
		t.Fatalf("generation %d -> %d, result %d", before, cfg.Generation(), res.Generation)
	}
	if len(fired) != 1 || fired[0] != res.Generation {
		t.Fatalf("OnCommit fired %v, want exactly [%d]", fired, res.Generation)
	}
}

// TestConfigBoot_DepsConfigPassthroughValues: an operator value of a passthrough
// path reaches the module raw, with where it came from.
func TestConfigBoot_DepsConfigPassthroughValues(t *testing.T) {
	_, _, lucius := appConfigHost(t, true, "GOTHAM_INTEGRATIONS_BATCAVE_ALFRED_HOST=bat.example.test")
	var got config.OperatorValue
	var found bool
	for p, v := range lucius.Config.PassthroughValues() {
		if p == "integrations.batcave.alfred.host" {
			got, found = v, true
		}
	}
	if !found || string(got.Value) != `"bat.example.test"` || got.Src != config.SourceEnv {
		t.Fatalf("passthrough value = %+v (found %v), want the env's host", got, found)
	}
}

// TestConfigBoot_DepsConfigSecretsAreTheDeclaredNames: Secret, PutSecret and
// DeleteSecret work on a name the declaration admits and refuse, with
// ErrOutOfScope, a name outside it, a name that is a registered config key
// (Apply's), and everything for a module that declares no Secrets func.
func TestConfigBoot_DepsConfigSecretsAreTheDeclaredNames(t *testing.T) {
	_, _, lucius := appConfigHost(t, true)
	ctx := t.Context()
	const in secrets.Name = "integrations.batcave.signal_key"
	if _, err := lucius.Config.PutSecret(ctx, in, secrets.NewValue(secretsFixture), "user:alfred"); err != nil {
		t.Fatalf("PutSecret of an admitted name: %v", err)
	}
	v, meta, err := lucius.Config.Secret(ctx, in)
	if err != nil || !meta.Set || v.Reveal() != secretsFixture {
		t.Fatalf("Secret = %v, %+v, %v", v, meta, err)
	}
	if err := lucius.Config.DeleteSecret(ctx, in, "user:alfred"); err != nil {
		t.Fatalf("DeleteSecret: %v", err)
	}
	for _, out := range []secrets.Name{"modules.bane.venom", "core.auth.oidc.google.client_secret", "integrations.bane.venom"} {
		if _, _, err := lucius.Config.Secret(ctx, out); !errors.Is(err, secrets.ErrOutOfScope) {
			t.Errorf("Secret(%s) outside the declaration: %v, want ErrOutOfScope", out, err)
		}
		if _, err := lucius.Config.PutSecret(ctx, out, secrets.NewValue(secretsFixture), "user:alfred"); !errors.Is(err, secrets.ErrOutOfScope) {
			t.Errorf("PutSecret(%s) outside the declaration: %v, want ErrOutOfScope", out, err)
		}
		if err := lucius.Config.DeleteSecret(ctx, out, "user:alfred"); !errors.Is(err, secrets.ErrOutOfScope) {
			t.Errorf("DeleteSecret(%s) outside the declaration: %v, want ErrOutOfScope", out, err)
		}
	}
	// Admitted by the func but a registered key: refused, Apply owns it.
	var registered secrets.Name
	for p, st := range lucius.Config.Keys() {
		if st.Secret != nil {
			registered = secrets.Name(p)
			break
		}
	}
	if registered == "" {
		t.Fatal("the boot registers no secret config key: the registered-key refusals below would be vacuous")
	}
	{
		wide := &appConfigAccess{store: lucius.Config.(*appConfigAccess).store, secrets: lucius.Config.(*appConfigAccess).secrets,
			admit: func(secrets.Name) bool { return true }}
		if _, err := wide.PutSecret(ctx, registered, secrets.NewValue(secretsFixture), "user:alfred"); !errors.Is(err, secrets.ErrOutOfScope) {
			t.Errorf("PutSecret(%s), a registered key: %v, want ErrOutOfScope", registered, err)
		}
		if err := wide.DeleteSecret(ctx, registered, "user:alfred"); !errors.Is(err, secrets.ErrOutOfScope) {
			t.Errorf("DeleteSecret(%s), a registered key: %v, want ErrOutOfScope", registered, err)
		}
		// Reading a registered key's secret is allowed: Lookup carries only metadata.
		if _, _, err := wide.Secret(ctx, registered); errors.Is(err, secrets.ErrOutOfScope) {
			t.Errorf("Secret(%s), a registered key, was refused as out of scope", registered)
		}
	}
	none := &appConfigAccess{store: lucius.Config.(*appConfigAccess).store, secrets: lucius.Config.(*appConfigAccess).secrets}
	if _, _, err := none.Secret(ctx, in); !errors.Is(err, secrets.ErrOutOfScope) {
		t.Errorf("Secret with no admit func: %v, want ErrOutOfScope", err)
	}
}

// TestConfigBoot_ResetAppliedAfterSecretsOpen (DATA7-17): under the gate a
// factory reset boot journals applied right after secrets.Open, not after
// P4a. A boot whose secrets.Open fails leaves the journal at set_aside, so the
// next boot repeats the fresh install and the keyring rewrite; the gate-off
// twin is TestResetHookDoneAfterMigrationList (applied after P4a).
func TestConfigBoot_ResetAppliedAfterSecretsOpen(t *testing.T) {
	h := newSwapHost(t)
	h.close(h.mustBoot(h.gatedConfig(nil)))
	oldHeader, err := os.ReadFile(filepath.Join(h.state, "secrets.keyring"))
	if err != nil {
		t.Fatal(err)
	}
	const id = "z-reset"
	in := h.resetIntent(id, restorejournal.ResetStore)
	if err := h.resetStager().Stage(h.ctx, in); err != nil {
		t.Fatal(err)
	}
	// A key file that is a directory: secrets.Open refuses it, after P4a.
	if err := os.Mkdir(filepath.Join(h.state, "not-a-keyring"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := h.boot(h.gatedConfig(nil, "GOTHAM_CORE_SECRETS_KEY_FILE=not-a-keyring")); err == nil {
		t.Fatal("a reset boot whose secrets.Open fails succeeded")
	}
	got := stepsOf(h.journal(id))
	if last := got[len(got)-1]; last != "set_aside" {
		t.Fatalf("after a failed secrets.Open the journal is %v, want it at set_aside", got)
	}
	k := h.mustBoot(h.gatedConfig(nil))
	got = stepsOf(h.journal(id))
	if last := got[len(got)-1]; last != "hook_done" {
		t.Fatalf("after the retry the journal is %v, want it at hook_done", got)
	}
	header, err := os.ReadFile(filepath.Join(h.state, "secrets.keyring"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(header, []byte(`"install_id": "`+k.store.db.Admin().InstallID()+`"`)) || bytes.Equal(header, oldHeader) {
		t.Fatal("the reset boot did not rewrite the keyring header with the new install marker")
	}
}

// TestConfigBoot_ClosesSecretsWithStorage: closing the kernel closes the secret
// store, and so does a boot that fails after P4b opened it (a migration that
// fails): the importer a Cutover step held refuses once the store is closed.
func TestConfigBoot_ClosesSecretsWithStorage(t *testing.T) {
	h := newSwapHost(t)
	k := h.mustBoot(h.gatedConfig(nil))
	store := k.sec.store
	if _, err := store.Put(t.Context(), "modules.alfred.signal_key", secrets.NewValue(secretsFixture), "user:alfred"); err != nil {
		t.Fatal(err)
	}
	if err := k.closeCore(context.Background(), time.Now().Add(defaultShutdownBudget)); err != nil {
		t.Fatal(err)
	}
	if k.sec != nil {
		t.Fatal("closeCore left the secret store open")
	}
	if v, _, err := store.Get(t.Context(), "modules.alfred.signal_key"); err == nil {
		t.Fatalf("a read after close returned %d bytes, want the wiped keyring to refuse it", len(v.Reveal()))
	}

	// A boot that fails after P4b opened the store closes it too.
	h2 := newSwapHost(t)
	h2.close(h2.mustBoot(h2.gatedConfig(nil)))
	var held secrets.Importer
	broken := storage.Migration{Version: 2, Name: "broken", Compat: storage.Additive,
		Up: func(context.Context, *storage.MigrationTx) error { return errors.New("bane broke it") }}
	c := testConfig(t, storageMod("alfred", nil, seedStep(1, "cave"), broken).with(func(m *module.Manifest) {
		m.Cutover = func(imp module.Importers) []datamigrate.DataMigration {
			held = imp.Secrets
			return nil
		}
	}))
	c.StoragePath, c.StateDir, c.CoreConfigBoot = h2.db, h2.state, true
	c.ConfigOptions, c.StateDirDefault = config.Options{Environ: []string{}}, h2.state
	if _, err := New(h2.ctx, c); err == nil {
		t.Fatal("a boot with a failing migration succeeded")
	}
	err := held.ImportSecret(h2.ctx, new(storage.Batch), secrets.Write{Name: "modules.alfred.signal_key", Value: secrets.NewValue(secretsFixture), By: "user:alfred"})
	if err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("an import after a failed boot: %v, want the closed-store refusal", err)
	}
}

// TestConfigBoot_P4dAttachesStoredValues: a value stored through the committer
// in one boot is the effective value of the next, read at P4d.
func TestConfigBoot_P4dAttachesStoredValues(t *testing.T) {
	var live *config.Handle[utilityBelt]
	batcave := storageMod("batcave", func(r *module.Registrar) error {
		live = module.Setting(r, beltSection)
		return r.Err()
	}).with(func(m *module.Manifest) { m.Settings = []module.SettingsSection{module.Settings(beltSection)} })
	h := newSwapHost(t)
	cfg := func() Config {
		c := h.gatedConfig(nil)
		c.Modules = append(c.Modules, batcave)
		return c
	}
	k := h.mustBoot(cfg())
	if live.Get().Grapple != 3 {
		t.Fatalf("grapple = %d, want the default 3", live.Get().Grapple)
	}
	res, err := k.cfgBoot.store.Apply(t.Context(), config.Patch{Set: map[config.Path]json.RawMessage{"modules.batcave.belt.grapple": json.RawMessage("11")}},
		config.Actor{Subject: "alfred", Interactive: true, Can: func(string) bool { return true }})
	if err != nil || len(res.Applied) != 1 {
		t.Fatalf("Apply = %+v, %v", res, err)
	}
	h.close(k)
	h.mustBoot(cfg())
	if live.Get().Grapple != 11 {
		t.Fatalf("grapple after a reboot = %d, want the stored 11", live.Get().Grapple)
	}
}

type recordedHealth struct{ last health.Result }

func (r *recordedHealth) Report(res health.Result) { r.last = res }

// TestConfigBoot_ReportsConfigHealth: core.config reports the store's and the
// kernel's tokens (F3-01's seam item 1): Up with none, Degraded with them.
func TestConfigBoot_ReportsConfigHealth(t *testing.T) {
	h := newSwapHost(t)
	k := h.mustBoot(h.gatedConfig(nil))
	if k.reporters.Config == nil {
		t.Fatal("the gate-on boot registered no core.config reporter")
	}
	if !slices.ContainsFunc(k.healthReg.Snapshot().Components, func(c health.ComponentState) bool { return c.ID == componentConfig }) {
		t.Fatal("core.config is not in the health registry")
	}
	rec := &recordedHealth{}
	k.reporters.Config = rec
	k.reportConfigHealth()
	if rec.last.Status != health.StatusUp {
		t.Fatalf("no tokens: %+v, want Up", rec.last)
	}
	k.cfgBoot.unknown = []module.ID{"scarecrow"}
	k.reportConfigHealth()
	if rec.last.Status != health.StatusDegraded || rec.last.Reason != healthUnknownModule {
		t.Fatalf("an unknown module in the boot record: %+v, want Degraded %s", rec.last, healthUnknownModule)
	}
}

// TestBootConfig_OwnerVerifierNeedsOwnerSet (F3-01's seam item 2): an app that
// configured an owner verifier must say whether an owner is set, or the boot
// fails; without a verifier a nil OwnerSet is the declared "no owner".
func TestBootConfig_OwnerVerifierNeedsOwnerSet(t *testing.T) {
	in := configIn(t)
	in.OwnerVerifier = true
	if cb, err := bootConfig(context.Background(), in); err == nil || !strings.Contains(err.Error(), "OwnerSet is nil") {
		if cb != nil {
			_ = cb.store.Close(context.Background())
		}
		t.Fatalf("bootConfig with a verifier and no OwnerSet = %v, want the refusal", err)
	}
	in = configIn(t)
	in.OwnerVerifier, in.OwnerSet = true, func() bool { return false }
	bootCfg(t, in)
	in = configIn(t)
	bootCfg(t, in)
}

// TestConfigBoot_InstrumentsBothStores: P5's metrics step registers the config
// and secrets instruments once (a second registration is refused by each).
func TestConfigBoot_InstrumentsBothStores(t *testing.T) {
	h := newSwapHost(t)
	k := h.mustBoot(h.gatedConfig(nil))
	if _, err := k.cfgBoot.configMetrics(k.provider); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("config metrics registered a second time: %v, want the refusal (the boot registered them)", err)
	}
	if _, err := secretsMetrics(k.provider, k.sec.store); err == nil {
		t.Fatalf("secrets metrics registered a second time: %v, want the refusal (the boot registered them)", err)
	}
}

// TestConfigBoot_ImporterDeletesEntrySecrets: M2's importer reaches the secret
// store (P4b's SetImportSecrets): a cutover step's import that drops a stored
// entry stages that entry's secret delete in the step's batch, and the boot
// completes. Without the stager config refuses the Stage and the boot fails.
func TestConfigBoot_ImporterDeletesEntrySecrets(t *testing.T) {
	const list = "core.auth.oidc.providers"
	h := newSwapHost(t)
	gated := func(c Config) Config {
		c.StoragePath, c.StateDir = h.db, h.state
		c.CoreConfigBoot = true
		c.ConfigOptions = config.Options{Environ: []string{}}
		c.StateDirDefault = h.state
		return c
	}
	k := h.mustBoot(gated(testConfig(t, gcpdMod(storage.ClaimReadWrite))))
	lr, _ := k.store.db.Legacy("gcpd")
	cks, ok := k.store.db.Keyspace(configstore.Owner)
	if !ok {
		t.Fatal("no config keyspace")
	}
	b := lr.NewBatch()
	if err := b.Set([]byte("case:file/001"), []byte("The Long Halloween, part 1")); err != nil {
		t.Fatal(err)
	}
	if err := k.cfgBoot.native.Stage(h.ctx, b.Bind(cks), []config.Override{{Path: list, By: "alfred", At: time.Now(),
		Value: json.RawMessage(`[{"id":"arkham","kind":"oidc","issuer":"https://id.example.com","client_id":"asylum","scopes":[],"allowed_emails":[],"allowed_domains":[],"default_role":"viewer","link_existing_by_email":false}]`)}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(h.ctx, storage.Sync); err != nil {
		t.Fatal(err)
	}
	h.close(k)

	imported := false
	c := testConfig(t, gcpdMod(storage.ClaimRetired).with(func(m *module.Manifest) {
		m.Cutover = func(imp module.Importers) []datamigrate.DataMigration {
			return []datamigrate.DataMigration{{
				ID: "gcpd.providers", Owner: "gcpd", Description: "replace the stored provider list",
				Source:      cutoverSource,
				Parallelism: tasks.Fixed(1, "test: one shard"), Mode: datamigrate.BootBlocking,
				Compat: storage.Breaking, Cutover: true,
				Step: func(_ context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
					imported = true
					if err := imp.Config.Stage(w.Batch(), list, json.RawMessage(`[{"id":"batcave","kind":"oidc","issuer":"https://id.example.com","client_id":"bat","scopes":[],"allowed_emails":[],"allowed_domains":[],"default_role":"viewer","link_existing_by_email":false}]`), "cutover:m2"); err != nil {
						return err
					}
					return w.Delete(kv.Key)
				},
			}}
		}
	}))
	if _, err := h.boot(gated(c)); err != nil || !imported {
		t.Fatalf("a cutover import that drops a stored entry: ran=%v err=%v, want its secret deleted through the stager", imported, err)
	}
}

// TestConfigBoot_QuarantineSealsIntoTheSecretStore: with the gate on a cutover
// step that quarantines a sensitive row seals it in the secret store (the
// adapter over secrets.Importer, late-bound to the store P4b opens), the
// quarantine record is sealed and the bytes are readable through the store;
// LevelStep and Preflight are bound. With the gate off none is bound.
func TestConfigBoot_QuarantineSealsIntoTheSecretStore(t *testing.T) {
	h := newSwapHost(t)
	gated := func(c Config) Config {
		c.StoragePath, c.StateDir = h.db, h.state
		c.CoreConfigBoot = true
		c.ConfigOptions = config.Options{Environ: []string{}}
		c.StateDirDefault = h.state
		return c
	}
	k := h.mustBoot(gated(testConfig(t, gcpdMod(storage.ClaimReadWrite))))
	lr, _ := k.store.db.Legacy("gcpd")
	b := lr.NewBatch()
	if err := b.Set([]byte("case:vault/001"), []byte("Joker venom formula")); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(h.ctx, storage.Sync); err != nil {
		t.Fatal(err)
	}
	h.close(k)

	c := testConfig(t, gcpdMod(storage.ClaimRetired).with(func(m *module.Manifest) {
		m.Cutover = func(module.Importers) []datamigrate.DataMigration {
			return []datamigrate.DataMigration{{
				ID: "gcpd.vault", Owner: "gcpd", Description: "quarantine the vault rows",
				Source:      cutoverSource,
				Parallelism: tasks.Fixed(1, "test: one shard"), Mode: datamigrate.BootBlocking,
				Compat: storage.Breaking, Cutover: true, Sensitive: []string{"case:vault/"},
				Step: func(_ context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
					return w.Quarantine(kv.Key, kv.Value, "a credential the importer does not carry")
				},
			}}
		}
	}))
	k = h.mustBoot(gated(c))
	if k.dataMig.cfg.LevelStep == nil || k.dataMig.cfg.Preflight == nil {
		t.Fatal("the gate-on boot left LevelStep or Preflight unbound")
	}
	ents, err := k.store.db.Quarantine(h.ctx, "gcpd.vault")
	if err != nil || len(ents) != 1 || !ents[0].Sealed() {
		t.Fatalf("quarantine = %+v, %v; want one sealed record", ents, err)
	}
	v, meta, err := k.sec.store.Get(h.ctx, secrets.Name(ents[0].Record.Secret))
	if err != nil || v.Reveal() != "Joker venom formula" {
		t.Fatalf("the sealed bytes through the store: %q, %v", v.Reveal(), err)
	}
	if meta.UpdatedBy != quarantineActor {
		t.Fatalf("the sealed record's actor is %q, want %q", meta.UpdatedBy, quarantineActor)
	}

	h.close(k)

	// Gate off: the seams stay unbound.
	h2 := newSwapHost(t)
	c2 := testConfig(t, gcpdMod(storage.ClaimReadWrite).with(func(m *module.Manifest) {
		m.DataMigrations = []datamigrate.DataMigration{{
			ID: "gcpd.noop", Owner: "gcpd", Description: "nothing to do",
			Source:      cutoverSource,
			Parallelism: tasks.Fixed(1, "test: one shard"), Mode: datamigrate.BootBlocking, Compat: storage.Additive,
			Step: func(context.Context, storage.KV, *datamigrate.StepWriter) error { return nil },
		}}
	}))
	c2.StoragePath, c2.StateDir = h2.db, h2.state
	off := h2.mustBoot(c2)
	if off.dataMig == nil {
		t.Fatal("the gate-off control has no runner")
	}
	if off.dataMig.cfg.LevelStep != nil || off.dataMig.cfg.Quarantine != nil || off.dataMig.cfg.Preflight != nil {
		t.Fatal("a gate-off boot bound the quarantine, level or preflight seam")
	}
}

// TestConfigBoot_StoresSurviveM8: M8 closes and reopens the engine inside P4b,
// after bindCoreStores took its keyspace handles. A gate-on boot that runs M8
// finishes it, and afterwards the secret store still writes and reads, the
// config store still applies, and P4d attached (gate-off twin:
// TestBoot_RewriteRunsAtP4bThroughTheDeclaredSwapper).
func TestConfigBoot_StoresSurviveM8(t *testing.T) {
	h := newSwapHost(t)
	gated := func(c Config) Config {
		c.StoragePath, c.StateDir = h.db, h.state
		c.CoreConfigBoot = true
		c.ConfigOptions = config.Options{Environ: []string{}}
		c.StateDirDefault = h.state
		return c
	}
	// The store exists, with the keyring and both keyspaces, before M8 is declared.
	h.close(h.mustBoot(gated(testConfig(t, storageMod("alfred", nil, seedStep(1, "cave"))))))

	rw := rewriteMod("alfred")
	k := h.mustBoot(gated(testConfig(t, rw)))
	state, err := k.store.db.RewriteRecord(h.ctx, "alfred.rewrite")
	if err != nil || state != storage.DataMigrationDone {
		t.Fatalf("M8's record = %q, %v; want done", state, err)
	}
	if _, err := k.sec.store.Put(h.ctx, "modules.alfred.signal_key", secrets.NewValue(secretsFixture), "user:alfred"); err != nil {
		t.Fatalf("a secret write after M8: %v", err)
	}
	if v, _, err := k.sec.store.Get(h.ctx, "modules.alfred.signal_key"); err != nil || v.Reveal() != secretsFixture {
		t.Fatalf("a secret read after M8: %v", err)
	}
	if _, err := k.cfgBoot.store.Apply(h.ctx, config.Patch{Set: map[config.Path]json.RawMessage{"core.ai.journal_retention": json.RawMessage(`"48h"`)}},
		config.Actor{Subject: "alfred", Interactive: true, Can: func(string) bool { return true }}); err != nil {
		t.Fatalf("a config apply after M8: %v", err)
	}
}

// TestConfigBoot_AttachRefusesAnOpenStoreWithNoSecrets: P4d with an open store
// and no secret store is a wiring bug (P4b did not run), not a gate-off boot,
// and it fails loudly. With no store at all it stays a no-op.
func TestConfigBoot_AttachRefusesAnOpenStoreWithNoSecrets(t *testing.T) {
	h := newSwapHost(t)
	k := h.mustBoot(h.gatedConfig(nil))
	defer h.close(k)
	sec := k.sec
	k.sec = nil
	err := k.attachCoreConfig(h.ctx)
	k.sec = sec
	if err == nil || !strings.Contains(err.Error(), "secret store") {
		t.Fatalf("attachCoreConfig with an open store and no secrets = %v, want the wiring error", err)
	}
	if err := (&Kernel{}).attachCoreConfig(h.ctx); err != nil {
		t.Fatalf("attachCoreConfig with no store = %v, want nil", err)
	}
}

// resolvable is the gated config an app hands ResolveStorage: no path, no
// state directory, so step 0 chooses both.
func (h *swapHost) resolvable(env ...string) Config {
	c := h.gatedConfig(nil, env...)
	c.StoragePath, c.StateDir = "", ""
	return c
}

// TestResolveStorage_AgreesWithTheServeBoot: the paths ResolveStorage returns
// are the paths the same Config boots on, whether the operator layers name
// core.storage.path or not.
func TestResolveStorage_AgreesWithTheServeBoot(t *testing.T) {
	for name, env := range map[string][]string{
		"default":  nil,
		"env path": {"GOTHAM_CORE_STORAGE_PATH=" + filepath.Join(t.TempDir(), "elsewhere", "main.pebble")},
	} {
		t.Run(name, func(t *testing.T) {
			h := newSwapHost(t)
			c := h.resolvable(env...)
			db, state, err := ResolveStorage(h.ctx, c, "")
			if err != nil {
				t.Fatal(err)
			}
			k := h.mustBoot(c)
			if db != k.cfg.StoragePath || state != k.cfg.StateDir {
				t.Fatalf("ResolveStorage = %q, %q; the boot opened %q, %q", db, state, k.cfg.StoragePath, k.cfg.StateDir)
			}
			if len(env) == 0 && db != filepath.Join(h.state, "storage", "main.pebble") {
				t.Fatalf("default store path = %q", db)
			}
			if len(env) > 0 && db != filepath.Clean(strings.TrimPrefix(env[0], "GOTHAM_CORE_STORAGE_PATH=")) {
				t.Fatalf("env store path not used: %q", db)
			}
		})
	}
}

// TestResolveStorage_RelativeDefaultAgreesWithTheServeBoot: a relative
// compiled default (hello's "hello-state") resolves to one absolute
// directory for ResolveStorage and for the boot of the same Config.
func TestResolveStorage_RelativeDefaultAgreesWithTheServeBoot(t *testing.T) {
	h := newSwapHost(t)
	t.Chdir(filepath.Dir(h.state))
	c := h.resolvable()
	c.StateDirDefault = filepath.Base(h.state)
	db, state, err := ResolveStorage(h.ctx, c, "")
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(state) || state != h.state {
		t.Fatalf("ResolveStorage state directory = %q, want %q (absolute)", state, h.state)
	}
	k := h.mustBoot(c)
	if db != k.cfg.StoragePath || state != k.cfg.StateDir {
		t.Fatalf("ResolveStorage = %q, %q; the boot opened %q, %q", db, state, k.cfg.StoragePath, k.cfg.StateDir)
	}
}

// TestResolveStorage_RefusesANonOwnerBeforeAnything (05-config §1.7 step 0,
// seams.d/F3-07.md): a command that writes, run as a user that does not own
// the existing state directory, is refused by ResolveStorage itself. When
// that candidate exists but is no directory, step 0 would fall back and
// create the store's directory; the refusal comes first and nothing is
// made. ResolveStorage never reads the boot record (no Boot), so an
// unparsable one is neither set aside as .bad nor touched, whoever runs it.
// A state directory that does not exist yet is compared with its nearest
// existing ancestor (TestResolveStorage_AbsentStateDirComparesTheNearestAncestor).
func TestResolveStorage_RefusesANonOwnerBeforeAnything(t *testing.T) {
	h := newSwapHost(t)
	beside := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.MkdirAll(h.state, 0o700); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(h.state, "config.boot.json")
	garbage := []byte(`{"version": 1, "modules": [`)
	if err := os.WriteFile(record, garbage, 0o600); err != nil {
		t.Fatal(err)
	}
	stranger := os.Geteuid() + 1
	ctx := bootseam.With(t.Context(), &bootseam.Seams{BuildID: swapBuild, Euid: func() int { return stranger }})
	c := h.resolvable("GOTHAM_CORE_STORAGE_PATH=" + filepath.Join(beside, "main.pebble"))

	_, _, err := ResolveStorage(ctx, c, "storage reset")
	if !errors.Is(err, storage.ErrNotStateDirOwner) || !strings.Contains(err.Error(), "storage reset") {
		t.Fatalf("ResolveStorage as a stranger = %v, want storage.ErrNotStateDirOwner naming the command", err)
	}
	if _, err := os.Stat(beside); !os.IsNotExist(err) {
		t.Fatalf("the refused resolve fell back beside the store: %v", err)
	}
	// A read-only command is not refused, and reads no boot record either.
	if _, state, err := ResolveStorage(ctx, c, ""); err != nil || state != h.state {
		t.Fatalf("a read-only ResolveStorage as a stranger = %q, %v; want %q", state, err, h.state)
	}
	if b, err := os.ReadFile(record); err != nil || !bytes.Equal(b, garbage) {
		t.Fatalf("the boot record = %q, %v; want it untouched", b, err)
	}
	if m, _ := filepath.Glob(record + ".bad*"); len(m) != 0 {
		t.Fatalf("ResolveStorage set the boot record aside: %v", m)
	}

	// The default exists and is a file: without the refusal, step 0 would
	// fall back beside the store and create that directory.
	notDir := newSwapHost(t)
	if err := os.MkdirAll(filepath.Dir(notDir.state), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(notDir.state, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	c = notDir.resolvable("GOTHAM_CORE_STORAGE_PATH=" + filepath.Join(beside, "main.pebble"))
	// The refusal is returned as itself, not as step 0's "cannot be
	// created" over it: the operator is told who to run as.
	if _, _, err := ResolveStorage(ctx, c, "storage reset"); !errors.Is(err, storage.ErrNotStateDirOwner) || strings.Contains(err.Error(), "cannot be") {
		t.Fatalf("ResolveStorage of a stranger's unusable default = %v, want storage.ErrNotStateDirOwner alone", err)
	} else if strings.Contains(err.Error(), "does not exist yet") {
		t.Fatalf("an existing candidate's refusal talks of an absent directory: %v", err)
	}
	if _, err := os.Stat(beside); !os.IsNotExist(err) {
		t.Fatalf("the refused resolve created the fallback beside the store: %v", err)
	}
}

// absentChain is a state directory several levels below one that exists: the
// first three levels do not exist, so a rule that looks only at the parent
// finds nothing to compare.
func absentChain(base string) (top, state string) {
	top = filepath.Join(base, "wayne", "manor")
	return top, filepath.Join(top, "batcave", "state")
}

// TestResolveStorage_AbsentStateDirComparesTheNearestAncestor (05-config
// section 1.7, seams.d/F3-07.md "Absent state directory"): a state directory
// that does not exist has no owner, so a command that writes is compared
// with the owner of the nearest existing ancestor, however many levels are
// missing. An ancestor this user owns: every level is created, as serve
// creates them. An ancestor another user owns: refused, with nothing created
// at any level.
func TestResolveStorage_AbsentStateDirComparesTheNearestAncestor(t *testing.T) {
	stranger := os.Geteuid() + 1
	as := func(euid int) context.Context {
		return bootseam.With(t.Context(), &bootseam.Seams{BuildID: swapBuild, Euid: func() int { return euid }})
	}
	t.Run("ancestor owned by this user: created", func(t *testing.T) {
		h := newSwapHost(t)
		top, state := absentChain(t.TempDir())
		c := h.resolvable()
		c.StateDirDefault = state
		got, err := mustResolve(as(os.Geteuid()), t, c, "storage reset")
		if err != nil || got != state {
			t.Fatalf("ResolveStorage under an ancestor this user owns = %q, %v; want %q created", got, err, state)
		}
		if fi, err := os.Stat(state); err != nil || !fi.IsDir() {
			t.Fatalf("the absent state directory %s was not created: %v", state, err)
		}
		if _, err := os.Stat(top); err != nil {
			t.Fatalf("the missing levels above it were not created: %v", err)
		}
	})
	t.Run("ancestor owned by another user: refused, nothing created", func(t *testing.T) {
		h := newSwapHost(t)
		base := t.TempDir()
		top, state := absentChain(base)
		c := h.resolvable()
		c.StateDirDefault = state
		_, _, err := ResolveStorage(as(stranger), c, "storage reset")
		if !errors.Is(err, storage.ErrNotStateDirOwner) || !strings.Contains(err.Error(), "storage reset") || !strings.Contains(err.Error(), base) {
			t.Fatalf("ResolveStorage under a stranger's ancestor = %v, want storage.ErrNotStateDirOwner naming the command and the ancestor %s", err, base)
		}
		if _, err := os.Stat(filepath.Dir(top)); !os.IsNotExist(err) {
			t.Fatalf("the refused resolve created a level of the chain: %v", err)
		}
	})
	t.Run("read-only command under a stranger's ancestor: not the rule's", func(t *testing.T) {
		h := newSwapHost(t)
		_, state := absentChain(t.TempDir())
		c := h.resolvable()
		c.StateDirDefault = state
		if got, err := mustResolve(as(stranger), t, c, ""); err != nil || got != state {
			t.Fatalf("a read-only ResolveStorage as a stranger = %q, %v; want %q", got, err, state)
		}
	})
	t.Run("an unusable default falls back beside the store, checked the same way", func(t *testing.T) {
		h := newSwapHost(t)
		base := t.TempDir()
		blocker := filepath.Join(base, "arkham")
		if err := os.WriteFile(blocker, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		beside := filepath.Join(base, "elsewhere", "deeper")
		c := h.resolvable("GOTHAM_CORE_STORAGE_PATH=" + filepath.Join(beside, "main.pebble"))
		c.StateDirDefault = filepath.Join(blocker, "state") // under a file: cannot be created
		// As this user the walk passes the file (ENOTDIR) to an ancestor this
		// user owns, the default fails to be created, and the fallback is an
		// absent chain compared the same way.
		if got, err := mustResolve(as(os.Geteuid()), t, c, "storage reset"); err != nil || got != beside {
			t.Fatalf("ResolveStorage with an unusable default = %q, %v; want the fallback %q", got, err, beside)
		}
		if fi, err := os.Stat(beside); err != nil || !fi.IsDir() {
			t.Fatalf("the fallback was not created: %v", err)
		}
	})
}

// mustResolve is ResolveStorage's state directory, or its error.
func mustResolve(ctx context.Context, t *testing.T, c Config, owner string) (string, error) {
	t.Helper()
	_, state, err := ResolveStorage(ctx, c, owner)
	return state, err
}

// TestResolveStorage_WarnsOnStandardErrorWithNoLogger: with no Config.Logger
// the fallback beside the store is still reported, on Config.LogOutput (an
// offline command's standard error).
func TestResolveStorage_WarnsOnStandardErrorWithNoLogger(t *testing.T) {
	h := newSwapHost(t)
	blocker := filepath.Join(t.TempDir(), "arkham")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	beside := filepath.Join(t.TempDir(), "elsewhere")
	c := h.resolvable("GOTHAM_CORE_STORAGE_PATH=" + filepath.Join(beside, "main.pebble"))
	c.StateDirDefault = filepath.Join(blocker, "state") // under a file: cannot be created
	var out bytes.Buffer
	c.Logger, c.LogOutput = nil, &out
	if _, state, err := ResolveStorage(h.ctx, c, ""); err != nil || state != beside {
		t.Fatalf("ResolveStorage = %q, %v; want the fallback %q", state, err, beside)
	}
	if !strings.Contains(out.String(), "keeping state files beside the database") {
		t.Fatalf("the fallback warning did not reach LogOutput: %q", out.String())
	}
}

// TestResolveStorage_GateOffPassesThrough: with the gate off the Config's own
// paths come back and nothing is created.
func TestResolveStorage_GateOffPassesThrough(t *testing.T) {
	h := newSwapHost(t)
	c := h.config(nil)
	db, state, err := ResolveStorage(h.ctx, c, "")
	if err != nil || db != h.db || state != h.state {
		t.Fatalf("ResolveStorage gate off = %q, %q, %v", db, state, err)
	}
	if _, err := os.Stat(h.state); !os.IsNotExist(err) {
		t.Fatalf("a gate-off resolve created the state directory: %v", err)
	}
}

// TestResolveStorage_NoStorageIsEmpty: an app with no storage resolves to "".
func TestResolveStorage_NoStorageIsEmpty(t *testing.T) {
	h := newSwapHost(t)
	c := testConfig(t, mod("alfred", nil))
	c.CoreConfigBoot, c.ConfigOptions, c.StateDirDefault = true, config.Options{Environ: []string{}}, h.state
	db, state, err := ResolveStorage(h.ctx, c, "")
	if err != nil || db != "" || state != "" {
		t.Fatalf("ResolveStorage with no storage = %q, %q, %v", db, state, err)
	}
}

// TestResolveStorage_RefusesTwoStores: a compiled path and a layer naming two
// stores fails the resolve, as it fails the boot, and nothing is left open.
func TestResolveStorage_RefusesTwoStores(t *testing.T) {
	h := newSwapHost(t)
	c := h.resolvable("GOTHAM_CORE_STORAGE_PATH=" + filepath.Join(t.TempDir(), "a", "main.pebble"))
	c.StoragePath = filepath.Join(t.TempDir(), "b", "main.pebble")
	if _, _, err := ResolveStorage(h.ctx, c, ""); err == nil || !strings.Contains(err.Error(), "different stores") {
		t.Fatalf("ResolveStorage with two stores = %v", err)
	}
}

// rooftopSchedules and rooftops are a module's own section holding schedule
// keys: modules.gotham.tasks.schedules.patrol is a tasks.ScheduleConfig,
// ...schedules.note is a string.
type rooftopSchedules struct {
	Patrol tasks.ScheduleConfig `json:"patrol"`
	Note   string               `json:"note"`
}

type rooftops struct {
	Schedules rooftopSchedules `json:"schedules"`
}

var rooftopSection = config.Define("modules.gotham.tasks", rooftops{
	Schedules: rooftopSchedules{Patrol: tasks.ScheduleConfig{Enabled: true, Every: dur(time.Hour)}, Note: "signal"},
})

// TestScheduleKeys_ModuleKeysDeclared (F3-07 b, the a5 seam): with the config
// boot on, a module kind's ScheduleFromConfig key is declared by the section
// its own Manifest.Settings registered, owned by the module ID, so P7's
// ValidateSchedules accepts it and the scheduler reads its stored value. A key
// no section declares, a key of a non-schedule type, and a key in another
// module's namespace are still refused, and with the gate off no module
// section is registered, so the key stays refused (ABO's boot).
func TestScheduleKeys_ModuleKeysDeclared(t *testing.T) {
	const patrolKey = "modules.gotham.tasks.schedules.patrol"
	withKey := func(key string) tasks.Definition {
		return patrol(func(s *tasks.TaskSpec[struct{}, struct{}]) { s.Schedule = tasks.ScheduleFromConfig(key) })
	}
	gothamWith := func(key string) testModule {
		return gotham([]tasks.Kind{patrolKind}, withKey(key)).with(func(m *module.Manifest) {
			m.Settings = []module.SettingsSection{module.Settings(rooftopSection)}
		})
	}
	boot := func(t *testing.T, key string) (*Kernel, error) {
		h := newSwapHost(t)
		c := h.gatedConfig(nil)
		c.Modules = append(c.Modules, gothamWith(key))
		return h.boot(c)
	}

	t.Run("a declared schedule key is accepted and read", func(t *testing.T) {
		k, err := boot(t, patrolKey)
		if err != nil {
			t.Fatalf("New = %v, want the module's declared key accepted", err)
		}
		cfg, err := k.coreConfig()
		if err != nil {
			t.Fatal(err)
		}
		if isSched, owner, ok := cfg.declared(patrolKey); !ok || !isSched || owner != "gotham" {
			t.Errorf("declared(%s) = %v %q %v, want a schedule key owned by gotham", patrolKey, isSched, owner, ok)
		}
		if isSched, owner, ok := cfg.declared("modules.gotham.tasks.schedules.note"); !ok || isSched || owner != "gotham" {
			t.Errorf("declared(note) = %v %q %v, want a declared non-schedule key owned by gotham", isSched, owner, ok)
		}
		got, err := cfg.schedule(patrolKey)
		if err != nil || !got.Enabled || time.Duration(got.Every) != time.Hour {
			t.Errorf("schedule(%s) = %+v, %v; want the section's default, every hour", patrolKey, got, err)
		}
		if isSched, owner, ok := cfg.declared("core.backup.schedules.create"); !ok || !isSched || owner != "core" {
			t.Errorf("a core key = %v %q %v, want core's", isSched, owner, ok)
		}
	})
	for _, tc := range []struct{ name, key, want string }{
		{"a key no section declares", "modules.gotham.tasks.schedules.stakeout", "is not declared"},
		{"a declared key of a non-schedule type", "modules.gotham.tasks.schedules.note", "not of type tasks.ScheduleConfig"},
		{"a key in another module's namespace", "modules.arkham.tasks.schedules.patrol", "belongs to module arkham"},
	} {
		t.Run(tc.name+" is refused", func(t *testing.T) {
			_, err := boot(t, tc.key)
			if !hasKind(err, module.ProblemInvalidRegistration) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("New = %v, want an invalid registration naming %q", err, tc.want)
			}
		})
	}
	t.Run("the gate off declares no module key", func(t *testing.T) {
		h := newSwapHost(t)
		c := h.config(nil)
		c.Modules = append(c.Modules, gothamWith(patrolKey))
		_, err := New(h.ctx, c)
		if !hasKind(err, module.ProblemInvalidRegistration) || !strings.Contains(err.Error(), "is not declared") {
			t.Fatalf("New = %v, want the key refused as undeclared", err)
		}
	})
}
