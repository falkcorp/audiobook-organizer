// file: core/kernel/cutover_importers_test.go
// version: 1.0.0
// guid: c41e90b7-3d58-4f26-a7b1-0e5d8c92f163
// last-edited: 2026-11-18

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

// Manifest.Cutover's steps join the plan, and the importers they hold refuse
// when P4b runs with no importer bound: the boot fails with
// ErrImporterNotBound and the importer's store sees no call.
func TestKernel_CutoverStepsRunWithUnboundImportersRefused(t *testing.T) {
	h, _ := dryRunHost(t, map[string]string{"case:file/001": "The Long Halloween, part 1"})
	var seen atomic.Int64
	c := testConfig(t, gcpdMod(storage.ClaimReadWrite).with(func(m *module.Manifest) {
		m.Cutover = func(imp module.Importers) []datamigrate.DataMigration {
			return []datamigrate.DataMigration{importerStep("gcpd", imp, &seen)}
		}
	}))
	c.StoragePath, c.StateDir = h.db, h.state
	_, err := New(h.ctx, c)
	if !errors.Is(err, module.ErrImporterNotBound) {
		t.Fatalf("New = %v, want ErrImporterNotBound", err)
	}
	if seen.Load() == 0 {
		t.Fatal("the Cutover step never ran: Manifest.Cutover's steps are not in the plan")
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
		k, err := New(t.Context(), testConfig(t, m))
		if k != nil || !hasKind(err, module.ProblemMigrationOrder) {
			t.Fatalf("enabled=%v: New = %v, %v; want a ProblemMigrationOrder", enabled, k, err)
		}
	}
	if seen.Load() != 0 {
		t.Fatalf("a step ran %d times during the catalog check", seen.Load())
	}
}
