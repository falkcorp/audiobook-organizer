// file: core/kernel/rewrite_wiring_test.go
// version: 1.0.0
// guid: 0a7d5e32-b6c4-4182-9f3e-d81c4b7a2e59
// last-edited: 2026-11-18

package kernel

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
)

func rewriteMod(id string) testModule {
	rw := storage.NewRewriteStep(storage.Owner(id), id+".rewrite")
	return storageMod(id, nil, seedStep(1, "cave")).with(func(m *module.Manifest) { m.Rewrite = &rw })
}

// A manifest that declares Rewrite and no migration still gets the runner:
// M8 is the plan's last step, a cutover that marks the extra free space.
func TestDataSteps_RewriteAloneBuildsTheRunnerAndIsLast(t *testing.T) {
	h := newSwapHost(t)
	c := testConfig(t, rewriteMod("alfred"))
	c.StoragePath, c.StateDir = h.db, h.state
	k, err := offlineKernel(c)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = k.closeOffline(context.Background()) }()
	opts, err := k.storageOptions(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if k.dataMig == nil || len(opts.DataSteps) != 1 {
		t.Fatalf("runner %v, steps %d; want the runner and M8 alone", k.dataMig, len(opts.DataSteps))
	}
	if s := opts.DataSteps[0]; s.Owner != "alfred" || s.ID != "alfred.rewrite" || !s.Rewrite || !s.Cutover {
		t.Fatalf("M8 step = %+v", s)
	}
}

// No manifest declares a migration or a Rewrite: no runner, nothing added to
// the plan (ABO's shape until F3-07's later slices).
func TestDataSteps_NoMigrationNoRewriteIsNoRunner(t *testing.T) {
	h := newSwapHost(t)
	c := h.config(nil)
	k, err := offlineKernel(c)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = k.closeOffline(context.Background()) }()
	opts, err := k.storageOptions(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if k.dataMig != nil || len(opts.DataSteps) != 0 {
		t.Fatalf("runner %v, steps %d; want none", k.dataMig, len(opts.DataSteps))
	}
}

// Rewrite needs the state directory, where M8 swaps the store's directory: a
// build that declares it with none refuses at the store's options.
func TestDataSteps_RewriteWithoutStateDirRefuses(t *testing.T) {
	c := testConfig(t, rewriteMod("alfred"))
	c.StoragePath = t.TempDir() + "/main.pebble"
	k := &Kernel{cfg: c, lifecycle: newLifecycle()}
	if err := k.catalog(false); err != nil {
		t.Fatal(err)
	}
	_, err := k.storageOptions(t.Context())
	if err == nil || !strings.Contains(err.Error(), "declares Rewrite") {
		t.Fatalf("storageOptions = %v, want the Rewrite-needs-a-state-directory refusal", err)
	}
}

// M8 runs through a real boot: P0a's swapper serves the declared step ID, the
// copy swaps in, the second boot finds M8 done and runs nothing again, and the
// store's data survived.
func TestBoot_RewriteRunsAtP4bThroughTheDeclaredSwapper(t *testing.T) {
	h := newSwapHost(t)
	// The store exists before the build that declares M8, so M8 is pending
	// for the boot (a fresh install marks it done without running it).
	seed := testConfig(t, storageMod("alfred", nil, seedStep(1, "cave")))
	seed.StoragePath, seed.StateDir = h.db, h.state
	h.close(h.mustBoot(seed))

	c := testConfig(t, rewriteMod("alfred"))
	c.StoragePath, c.StateDir = h.db, h.state
	k := h.mustBoot(c)
	state, err := k.store.db.RewriteRecord(h.ctx, "alfred.rewrite")
	if err != nil || state != storage.DataMigrationDone {
		t.Fatalf("after the first boot M8's record = %q, %v; want done", state, err)
	}
	ks, _ := k.store.db.Keyspace("alfred")
	if v, err := ks.Get(h.ctx, []byte("cave")); err != nil || string(v) != "1" {
		t.Fatalf("seed row after M8 = %q, %v", v, err)
	}
	h.close(k)

	k = h.mustBoot(c)
	state, err = k.store.db.RewriteRecord(h.ctx, "alfred.rewrite")
	if err != nil || state != storage.DataMigrationDone {
		t.Fatalf("after the second boot M8's record = %q, %v", state, err)
	}
	h.close(k)
}

// With no P0a (an offline kernel has k.swap nil) the runner gets a swapper
// built for it, and it serves the declared M8: Migrate over the offline
// kernel's own options finishes the rewrite.
func TestDataSteps_OfflineSwapperServesTheDeclaredRewrite(t *testing.T) {
	h := newSwapHost(t)
	seed := testConfig(t, storageMod("alfred", nil, seedStep(1, "cave")))
	seed.StoragePath, seed.StateDir = h.db, h.state
	h.close(h.mustBoot(seed))
	c := testConfig(t, rewriteMod("alfred"))
	c.StoragePath, c.StateDir = h.db, h.state
	k, err := offlineKernel(c)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = k.closeOffline(context.Background()) }()
	if got := k.rewriteID(); got != "alfred.rewrite" {
		t.Fatalf("rewriteID = %q", got)
	}
	if got := (&Kernel{}).rewriteID(); got != "" {
		t.Fatalf("rewriteID of a kernel with no manifests = %q", got)
	}
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
	if err := db.Migrate(h.ctx); err != nil {
		t.Fatal(errors.Join(err, db.Close()))
	}
	state, err := db.RewriteRecord(h.ctx, "alfred.rewrite")
	if cerr := db.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	if err != nil || state != storage.DataMigrationDone {
		t.Fatalf("M8's record after the offline migrate = %q, %v; want done", state, err)
	}
}

// A disabled module's migrations and its Rewrite stay in the plan: its owner's
// later steps must wait behind them (05-storage 5S.3.5), and M8 is the App's.
func TestDataSteps_DisabledModulesStayInThePlan(t *testing.T) {
	h := newSwapHost(t)
	var seen atomic.Int64
	m := rewriteMod("alfred").with(func(m *module.Manifest) {
		m.DefaultEnabled = false
		m.Cutover = func(imp module.Importers) []datamigrate.DataMigration {
			return []datamigrate.DataMigration{importerStep("alfred", imp, &seen)}
		}
	})
	c := testConfig(t, m)
	c.StoragePath, c.StateDir = h.db, h.state
	k, err := offlineKernel(c)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = k.closeOffline(context.Background()) }()
	opts, err := k.storageOptions(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, s := range opts.DataSteps {
		ids = append(ids, s.ID)
	}
	if want := []string{"alfred.import-defaults", "alfred.rewrite"}; !slices.Equal(ids, want) {
		t.Fatalf("plan = %v, want %v", ids, want)
	}
	if k.rewriteID() != "alfred.rewrite" {
		t.Fatalf("rewriteID = %q for a disabled module's Rewrite", k.rewriteID())
	}
}
