// file: core/kernel/cutover_importers_test.go
// version: 1.1.0
// guid: c41e90b7-3d58-4f26-a7b1-0e5d8c92f163
// last-edited: 2026-11-19

package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
	"github.com/falkcorp/mediacore/tasks"
)

// fakeConfigImporter and fakeSecretsImporter count the calls that reach them.
type fakeConfigImporter struct{ stages, defaults atomic.Int64 }

func (f *fakeConfigImporter) Stage(*storage.Batch, config.Path, json.RawMessage, string) error {
	f.stages.Add(1)
	return nil
}

func (f *fakeConfigImporter) Default(config.Path) (json.RawMessage, error) {
	f.defaults.Add(1)
	return json.RawMessage(`"batsignal"`), nil
}

type fakeSecretsImporter struct{ secrets, unreadable atomic.Int64 }

func (f *fakeSecretsImporter) ImportSecret(context.Context, *storage.Batch, secrets.Write) error {
	f.secrets.Add(1)
	return nil
}

func (f *fakeSecretsImporter) ImportUnreadable(context.Context, *storage.Batch, secrets.Name, string, string) error {
	f.unreadable.Add(1)
	return nil
}

// callAll calls every method of both importers and returns their errors.
func callAll(imp module.Importers) []error {
	ctx := context.Background()
	_, derr := imp.Config.Default("core.http.listen")
	return []error{
		imp.Config.Stage(nil, "core.http.listen", json.RawMessage(`"x"`), "test"),
		derr,
		imp.Secrets.ImportSecret(ctx, nil, secrets.Write{}),
		imp.Secrets.ImportUnreadable(ctx, nil, "", "unreadable", "test"),
	}
}

// An unbound importer refuses every method with ErrImporterNotBound, so a
// step that runs before the kernel bound the stores writes nowhere.
func TestLateImporters_RefuseUntilBound(t *testing.T) {
	var l lateImporters
	for i, err := range callAll(l.values()) {
		if !errors.Is(err, module.ErrImporterNotBound) {
			t.Errorf("unbound call %d = %v, want ErrImporterNotBound", i, err)
		}
	}
	// Binding only one importer leaves the other refusing.
	c := &fakeConfigImporter{}
	l.bind(c, nil)
	errs := callAll(l.values())
	if errs[0] != nil || errs[1] != nil || !errors.Is(errs[2], module.ErrImporterNotBound) || !errors.Is(errs[3], module.ErrImporterNotBound) {
		t.Errorf("config bound, secrets not: %v", errs)
	}
	if c.stages.Load() != 1 || c.defaults.Load() != 1 {
		t.Errorf("config importer saw %d stages, %d defaults; want 1 each", c.stages.Load(), c.defaults.Load())
	}
	s := &fakeSecretsImporter{}
	l.bind(nil, s)
	errs = callAll(l.values())
	if !errors.Is(errs[0], module.ErrImporterNotBound) || !errors.Is(errs[1], module.ErrImporterNotBound) || errs[2] != nil || errs[3] != nil {
		t.Errorf("secrets bound, config not: %v", errs)
	}
	if s.secrets.Load() != 1 || s.unreadable.Load() != 1 {
		t.Errorf("secrets importer saw %d secrets, %d unreadable; want 1 each", s.secrets.Load(), s.unreadable.Load())
	}
}

// Values handed out before the bind see it: the Cutover func ran at P2.
func TestLateImporters_BindReachesValuesHandedOutEarlier(t *testing.T) {
	var l lateImporters
	imp := l.values()
	c, s := &fakeConfigImporter{}, &fakeSecretsImporter{}
	l.bind(c, s)
	for i, err := range callAll(imp) {
		if err != nil {
			t.Errorf("call %d after the bind = %v", i, err)
		}
	}
}

// cutoverSource is the legacy range the fixture's cutover step reads.
var cutoverSource = []storage.Range{{Start: []byte("case:"), End: []byte("case;")}}

// importerStep is a migration over the gcpd rows whose Step calls the
// config importer (the way a cutover step reads a default) and ends the
// migration with that call's error.
func importerStep(owner storage.Owner, imp module.Importers, seen *atomic.Int64) datamigrate.DataMigration {
	source := cutoverSource
	if owner != "gcpd" {
		source = []storage.Range{{Start: []byte("m/" + string(owner) + "/item/"), End: []byte("m/" + string(owner) + "/item0")}}
	}
	return datamigrate.DataMigration{
		ID: string(owner) + ".import-defaults", Owner: owner, Description: "read a config default through the importer",
		Source:      source,
		Parallelism: tasks.Fixed(1, "test: one shard"), Mode: datamigrate.BootBlocking, Compat: storage.Additive,
		Step: func(_ context.Context, _ storage.KV, _ *datamigrate.StepWriter) error {
			seen.Add(1)
			_, err := imp.Config.Default("core.http.listen")
			return err
		},
	}
}

// P4b binds the kernel's importers before the first step runs: with a store
// wired (the kernel's importerCfg) the same step reads through it.
func TestMigrateStorage_BindsTheImportersBeforeTheSteps(t *testing.T) {
	h, _ := dryRunHost(t, map[string]string{"case:file/001": "The Long Halloween, part 1"})
	var seen atomic.Int64
	c := testConfig(t, gcpdMod(storage.ClaimReadWrite).with(func(m *module.Manifest) {
		m.Cutover = func(imp module.Importers) []datamigrate.DataMigration {
			return []datamigrate.DataMigration{importerStep("gcpd", imp, &seen)}
		}
	}))
	c.StoragePath, c.StateDir = h.db, h.state
	k, err := offlineKernel(c)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = k.closeOffline(context.Background()) }()
	fake := &fakeConfigImporter{}
	k.importerCfg = fake
	opts, err := k.storageOptions(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	o, err := storage.Open(h.ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	db, err := o.Prepare(h.ctx)
	if err != nil {
		t.Fatal(errors.Join(err, o.Close()))
	}
	defer func() { _ = db.Close() }()
	k.store = &kernelStore{opened: o, db: db}
	if err := k.migrateStorage(h.ctx); err != nil {
		t.Fatalf("migrateStorage = %v", err)
	}
	if seen.Load() != 1 || fake.defaults.Load() != 1 {
		t.Fatalf("step ran %d times and reached the importer %d times, want 1 and 1", seen.Load(), fake.defaults.Load())
	}
}

// A Cutover step is held to the same ownership rule as DataMigrations.
func TestCatalog_CutoverStepOfAnotherOwnerIsAProblem(t *testing.T) {
	var seen atomic.Int64
	for _, enabled := range []bool{true, false} {
		m := gcpdMod(storage.ClaimReadWrite).with(func(m *module.Manifest) {
			m.DefaultEnabled = enabled
			m.Cutover = func(imp module.Importers) []datamigrate.DataMigration {
				return []datamigrate.DataMigration{importerStep("arkham", imp, &seen)}
			}
		})
		k, err := newK(t, testConfig(t, m))
		if k != nil || !hasKind(err, module.ProblemMigrationOrder) {
			t.Fatalf("enabled=%v: New = %v, %v; want a ProblemMigrationOrder", enabled, k, err)
		}
	}
	if seen.Load() != 0 {
		t.Fatalf("a step ran %d times during the catalog check", seen.Load())
	}
}

// legacyProbe is a Cutover step over the gcpd rows that records what the
// Importers' late-bound Legacy and AllowKeyRegen seams hand it, the way
// books' M2 reads its legacy range and the key-regen switch (F3-07 d).
type legacyProbe struct {
	reads    atomic.Int64
	value    atomic.Value // string: the legacy row the step read
	nilOther atomic.Bool  // an owner with no claims got a nil reader
	regen    atomic.Bool
}

func (p *legacyProbe) step(imp module.Importers) datamigrate.DataMigration {
	return datamigrate.DataMigration{
		ID: "gcpd.probe", Owner: "gcpd", Description: "read the legacy range through the late-bound seam",
		Source:      cutoverSource,
		Parallelism: tasks.Fixed(1, "test: one shard"), Mode: datamigrate.BootBlocking,
		Compat: storage.Additive,
		Step: func(ctx context.Context, _ storage.KV, _ *datamigrate.StepWriter) error {
			p.reads.Add(1)
			if r := imp.Legacy("gcpd"); r != nil {
				v, err := r.Get(ctx, []byte("case:file/001"))
				if err != nil {
					return err
				}
				p.value.Store(string(v))
			}
			p.nilOther.Store(imp.Legacy("arkham") == nil)
			p.regen.Store(imp.AllowKeyRegen())
			return nil
		},
	}
}

// Before P4b binds them the Importers' Legacy hands a nil reader for every
// owner and AllowKeyRegen is false; after migrateStorage binds, Legacy hands
// the owner's legacy range (nil for an owner with no claims) and
// AllowKeyRegen the resolved core.secrets.allow_key_regen.
func TestMigrateStorage_BindsLegacyAndAllowKeyRegen(t *testing.T) {
	h, _ := dryRunHost(t, map[string]string{"case:file/001": "The Long Halloween, part 1"})
	probe := &legacyProbe{}
	c := testConfig(t, gcpdMod(storage.ClaimReadWrite).with(func(m *module.Manifest) {
		m.Cutover = func(imp module.Importers) []datamigrate.DataMigration {
			if imp.Legacy("gcpd") != nil || imp.AllowKeyRegen() {
				t.Error("an unbound Importers handed a reader or a true AllowKeyRegen")
			}
			return []datamigrate.DataMigration{probe.step(imp)}
		}
	}))
	c.StoragePath, c.StateDir = h.db, h.state
	k, err := offlineKernel(c)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = k.closeOffline(context.Background()) }()
	k.importerAllowRegen = true
	opts, err := k.storageOptions(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	o, err := storage.Open(h.ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	db, err := o.Prepare(h.ctx)
	if err != nil {
		t.Fatal(errors.Join(err, o.Close()))
	}
	defer func() { _ = db.Close() }()
	k.store = &kernelStore{opened: o, db: db}
	if err := k.migrateStorage(h.ctx); err != nil {
		t.Fatalf("migrateStorage = %v", err)
	}
	if probe.reads.Load() != 1 {
		t.Fatalf("step ran %d times, want 1", probe.reads.Load())
	}
	if v, _ := probe.value.Load().(string); v != "The Long Halloween, part 1" {
		t.Errorf("the step read %q through Importers.Legacy, want the legacy row", v)
	}
	if !probe.nilOther.Load() {
		t.Error("Importers.Legacy handed a reader for an owner that declared no claims")
	}
	if !probe.regen.Load() {
		t.Error("Importers.AllowKeyRegen did not report the bound value")
	}
}

// With the gate on, bindCoreStores reads core.secrets.allow_key_regen and P4b
// hands it to the Importers a Cutover func holds: true from the environment,
// false by default.
func TestConfigBoot_AllowKeyRegenReachesTheImporters(t *testing.T) {
	for _, tc := range []struct {
		env  []string
		want bool
	}{{[]string{}, false}, {[]string{"GOTHAM_CORE_SECRETS_ALLOW_KEY_REGEN=true"}, true}} {
		h := newSwapHost(t)
		c := testConfig(t, gcpdMod(storage.ClaimReadWrite))
		c.StoragePath, c.StateDir = h.db, h.state
		c.ConfigOptions = config.Options{Environ: tc.env}
		c.StateDirDefault = h.state
		k := h.mustBoot(c)
		if got := k.importers().values().AllowKeyRegen(); got != tc.want {
			t.Errorf("env %v: Importers.AllowKeyRegen = %v, want %v", tc.env, got, tc.want)
		}
		h.close(k)
	}
}

// Importers.OperatorSet answers false until the config store's Lookup is
// bound, then reports the lock of a key an operator layer sets, and false for
// a free key or an unregistered one.
func TestLateImporters_OperatorSet(t *testing.T) {
	l := &lateImporters{}
	imp := l.values()
	if by, set := imp.OperatorSet("core.auth.oidc.providers"); set || by != "" {
		t.Fatalf("unbound OperatorSet = %q, %v; want \"\", false", by, set)
	}
	l.bindLookup(func(p config.Path) (config.KeyState, bool) {
		switch p {
		case "core.auth.oidc.providers":
			return config.KeyState{Path: p, LockedBy: "env ABO_CORE_AUTH_OIDC_PROVIDERS"}, true
		case "core.log.level":
			return config.KeyState{Path: p}, true
		}
		return config.KeyState{}, false
	})
	if by, set := imp.OperatorSet("core.auth.oidc.providers"); !set || by != "env ABO_CORE_AUTH_OIDC_PROVIDERS" {
		t.Errorf("locked key: OperatorSet = %q, %v", by, set)
	}
	if _, set := imp.OperatorSet("core.log.level"); set {
		t.Error("a free key reads as operator-set")
	}
	if _, set := imp.OperatorSet("core.batcave.unregistered"); set {
		t.Error("an unregistered key reads as operator-set")
	}
}
