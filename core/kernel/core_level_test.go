// file: core/kernel/core_level_test.go
// version: 1.0.0
// guid: c41e7a08-5b93-4d2f-8e60-17fa92b3d5c4
// last-edited: 2026-11-19

package kernel

import (
	"context"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
	"github.com/falkcorp/mediacore/tasks"
)

// noRowsCutover is a Manifest.Cutover whose one step has nothing to move: the
// level step is what these tests look at.
func noRowsCutover(owner storage.Owner) func(module.Importers) []datamigrate.DataMigration {
	return func(module.Importers) []datamigrate.DataMigration {
		return []datamigrate.DataMigration{{
			ID: string(owner) + ".gotham-cutover", Owner: owner, Description: "a cutover with no rows to move",
			Source:      cutoverSource,
			Parallelism: tasks.Fixed(1, "test: one shard"), Mode: datamigrate.BootBlocking,
			Compat: storage.Breaking, Cutover: true,
			Step: func(context.Context, storage.KV, *datamigrate.StepWriter) error { return nil },
		}}
	}
}

// TestCoreLevel_RegisteredWhenAManifestDeclaresACutover (03 section 3.8 row M7;
// F3-04 "Flip mechanism"; F3-07 d): core's half of M7 is one level step,
// core.level-1 at version 1 on core/levels, with core's Level declared to match;
// a build whose manifests declare no cutover registers neither, and a disabled
// module's cutover counts (it still runs).
func TestCoreLevel_RegisteredWhenAManifestDeclaresACutover(t *testing.T) {
	h := newSwapHost(t)
	owners := func(c Config) []storage.OwnerSpec {
		t.Helper()
		c.StoragePath, c.StateDir = h.db, h.state
		k, err := newKCtx(h.t, h.ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		// Closed before the next boot: a kernel holds the swap lock while open.
		defer func() { _ = k.closeCore(context.Background(), time.Now().Add(defaultShutdownBudget)) }()
		return k.storageOwners()
	}
	find := func(os []storage.OwnerSpec) (storage.OwnerSpec, bool) {
		for _, o := range os {
			if o.Owner == coreLevelsOwner {
				return o, true
			}
		}
		return storage.OwnerSpec{}, false
	}

	if _, ok := find(owners(testConfig(t, gcpdMod(storage.ClaimReadWrite)))); ok {
		t.Fatal("a build that declares no cutover registered core's level step")
	}
	for _, enabled := range []bool{true, false} {
		m := gcpdMod(storage.ClaimRetired).with(func(m *module.Manifest) {
			m.DefaultEnabled = enabled
			m.Cutover = noRowsCutover("gcpd")
		})
		o, ok := find(owners(testConfig(t, m)))
		if !ok {
			t.Fatalf("enabled=%v: a build that declares a cutover registered no core level step", enabled)
		}
		if len(o.Spec.Migrations) != 1 || o.Spec.Migrations[0].Name != "core.level-1" || o.Spec.Migrations[0].Version != 1 ||
			o.Spec.Migrations[0].RaisesLevel != CoreCutoverLevel || o.Spec.Level != CoreCutoverLevel || !o.Enabled || !o.Spec.Keyspace {
			t.Errorf("enabled=%v: core/levels = %+v, want core.level-1 at version 1 raising core to %d with Level %d", enabled, o, CoreCutoverLevel, CoreCutoverLevel)
		}
	}
}

// TestCoreLevel_RaisesCoreOnABootThatDeclaresACutover: booted, core's stamp
// and its supported level are 1 after the level step; the boot before the
// cutover was declared leaves core at 0.
func TestCoreLevel_RaisesCoreOnABootThatDeclaresACutover(t *testing.T) {
	h := newSwapHost(t)
	gated := func(c Config) Config {
		c.StoragePath, c.StateDir = h.db, h.state
		c.ConfigOptions = config.Options{Environ: []string{}}
		c.StateDirDefault = h.state
		return c
	}
	levels := func(k *Kernel) (store, supported int) {
		s, sup := k.store.db.Levels()
		return s["core"], sup["core"]
	}
	k := h.mustBoot(gated(testConfig(t, gcpdMod(storage.ClaimReadWrite))))
	if s, sup := levels(k); s != 0 || sup != 0 {
		t.Fatalf("no cutover declared: core stamp %d, supported %d, want 0 and 0", s, sup)
	}
	h.close(k)

	c := testConfig(t, gcpdMod(storage.ClaimRetired).with(func(m *module.Manifest) { m.Cutover = noRowsCutover("gcpd") }))
	k = h.mustBoot(gated(c))
	if s, sup := levels(k); s != CoreCutoverLevel || sup != CoreCutoverLevel {
		t.Fatalf("a cutover declared: core stamp %d, supported %d, want %d and %d", s, sup, CoreCutoverLevel, CoreCutoverLevel)
	}
	if _, ok := k.store.db.Keyspace(coreLevelsOwner); !ok {
		t.Error("core/levels has no keyspace after the boot")
	}
}
