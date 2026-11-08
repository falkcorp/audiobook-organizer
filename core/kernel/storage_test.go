// file: core/kernel/storage_test.go
// version: 1.0.0
// guid: 36b3a2eb-1f55-4a42-83bc-47c859649615
// last-edited: 2026-11-07

package kernel

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/coretest/logcapture"
	"github.com/falkcorp/mediacore/metrics/metricstest"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
)

// tempStoragePath is core.storage.path under a fresh temporary directory.
func tempStoragePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "storage", "main.pebble")
}

// storageMod is a module with hello's storage shape: a keyspace and the
// given migrations.
func storageMod(id string, register func(*module.Registrar) error, migs ...storage.Migration) testModule {
	return mod(id, register).with(func(m *module.Manifest) {
		m.Storage = storage.Spec{Keyspace: true, Migrations: migs}
	})
}

// seedStep is a Seed step that writes key once, like hello's seed-names.
func seedStep(v int, key string) storage.Migration {
	return storage.Migration{Version: v, Name: "seed-" + key, Compat: storage.Additive, Seed: true,
		Up: func(ctx context.Context, tx *storage.MigrationTx) error {
			if _, err := tx.Keyspace().Get(ctx, []byte(key)); !errors.Is(err, storage.ErrNotFound) {
				return err
			}
			return tx.Set([]byte(key), []byte("1"))
		}}
}

// assertSeries compares the series of every scope with one golden.
func assertSeries(t *testing.T, k *Kernel, golden string, scopes ...interface{ Prefix() string }) {
	t.Helper()
	want, err := metricstest.ReadGolden(golden)
	if err != nil {
		t.Fatal(err)
	}
	var got []metricstest.Series
	for _, s := range scopes {
		ss, err := metricstest.Gather(k.provider.Gatherer(), strings.ReplaceAll(s.Prefix(), ".", "_")+"_")
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, ss...)
	}
	slices.SortFunc(got, func(a, b metricstest.Series) int { return strings.Compare(a.Family, b.Family) })
	if d := metricstest.Diff(want, got); len(d) > 0 {
		t.Errorf("series do not match %s:\n\t%s", golden, strings.Join(d, "\n\t"))
	}
}

// depsScopingStorage is TestDepsScoping's storage cell (06 §6.10): a module's
// Deps.Storage reaches only m/<ID>/; a key that spells another module's
// prefix stays inside it; the legacy range of a module without claims
// refuses every write; a module that declared no storage gets handles whose
// every write fails.
func depsScopingStorage(t *testing.T) {
	ctx := t.Context()
	got := map[module.ID]module.Deps{}
	reg := func(r *module.Registrar) error { got[r.Deps().Module] = r.Deps(); return nil }
	c := testConfig(t, storageMod("alfred", reg), storageMod("bane", reg), mod("joker", reg))
	c.StoragePath = tempStoragePath(t)
	mustNew(t, c)
	alfred, bane, joker := got["alfred"].Storage, got["bane"].Storage, got["joker"].Storage
	if p := alfred.Keyspace.Prefix(); p != "m/alfred/" {
		t.Fatalf("alfred's keyspace prefix = %q, want m/alfred/", p)
	}
	if err := alfred.Keyspace.Set(ctx, []byte("m/bane/cowl"), []byte("stolen")); err != nil {
		t.Fatal(err)
	}
	if _, err := bane.Keyspace.Get(ctx, []byte("cowl")); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("bane reads alfred's write as its own: %v", err)
	}
	if v, err := alfred.Keyspace.Get(ctx, []byte("m/bane/cowl")); err != nil || string(v) != "stolen" {
		t.Fatalf("alfred's own key = %q, %v", v, err)
	}
	if err := alfred.Legacy.Set(ctx, []byte("m/bane/cowl"), []byte("x")); err == nil {
		t.Fatal("a legacy range without claims accepted a write into m/bane/")
	}
	if err := alfred.Legacy.Set(ctx, []byte("joker:1"), []byte("x")); err == nil {
		t.Fatal("a legacy range without claims accepted a legacy write")
	}
	if err := joker.Keyspace.Set(ctx, []byte("cowl"), []byte("x")); !errors.Is(err, storage.ErrInvalidKeyspace) {
		t.Fatalf("a module without storage wrote through Deps.Storage: %v", err)
	}
}

// TestStoragePhasesFreshInstallRunsSeed (06 §6.5.1 P2, P4a, P4b): with a
// path set the boot opens the store, stamps a fresh install without a
// checkpoint and runs the Seed step before Register, whose Deps.Storage sees
// the seeded row; the next boot of the same build runs nothing and takes no
// checkpoint, and the row is unchanged.
func TestStoragePhasesFreshInstallRunsSeed(t *testing.T) {
	ctx := t.Context()
	var deps module.Deps
	reg := func(r *module.Registrar) error {
		deps = r.Deps()
		return nil
	}
	path := tempStoragePath(t)
	c := testConfig(t, storageMod("alfred", reg, seedStep(1, "cave")))
	c.StoragePath = path
	k := mustNew(t, c)
	seen, err := deps.Storage.Keyspace.Get(ctx, []byte("cave"))
	if err != nil || string(seen) != "1" {
		t.Fatalf("seeded row = %q, %v; want 1", seen, err)
	}
	if name := k.store.db.CheckpointName(); name != "" {
		t.Fatalf("a fresh install took checkpoint %q", name)
	}
	if v := k.store.db.AppliedVersion("alfred"); v != 1 {
		t.Fatalf("alfred's applied version = %d, want 1", v)
	}
	if err := deps.Storage.Keyspace.Set(ctx, []byte("cave"), []byte("7")); err != nil {
		t.Fatal(err)
	}
	if err := k.closeCore(ctx, time.Now().Add(defaultShutdownBudget)); err != nil {
		t.Fatal(err)
	}
	k = mustNew(t, c)
	if name := k.store.db.CheckpointName(); name != "" {
		t.Fatalf("a boot with nothing to run took checkpoint %q", name)
	}
	if v, err := deps.Storage.Keyspace.Get(ctx, []byte("cave")); err != nil || string(v) != "7" {
		t.Fatalf("row after the second boot = %q, %v; want 7 (a seed leaves existing rows)", v, err)
	}
}

// TestStorageNeedsPath: an enabled module that declares storage refuses a
// boot with no core.storage.path, naming the option; a disabled one does
// not, and nothing is opened.
func TestStorageNeedsPath(t *testing.T) {
	_, err := New(t.Context(), testConfig(t, storageMod("alfred", nil)))
	if err == nil || !strings.Contains(err.Error(), "core.storage.path") {
		t.Fatalf("boot without a path = %v, want a refusal naming core.storage.path", err)
	}
	off := storageMod("alfred", nil).with(func(m *module.Manifest) { m.DefaultEnabled = false })
	if k := mustNew(t, testConfig(t, off)); k.store != nil {
		t.Fatal("a boot without a path opened a store")
	}
}

// TestDescribeOpensNoStore (06 §6.4.5, COR2-13): Describe skips P2 to P4
// whatever the path says, so the store's directory is never created.
func TestDescribeOpensNoStore(t *testing.T) {
	dir := t.TempDir()
	c := testConfig(t, storageMod("alfred", nil, seedStep(1, "cave")))
	c.StoragePath = filepath.Join(dir, "storage", "main.pebble")
	if _, err := Describe(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("Describe left %v (%v) beside the store path", entries, err)
	}
}

// TestFailedMigrationClosesStore (COR-24): a failing step halts boot, New
// returns the step's error and closes the store, so the next boot (a fixed
// build) opens the same directory and completes. The step is a Seed step: a
// fresh install records every step below the lowest seed as applied without
// running it, so only a seed runs on an empty store.
func TestFailedMigrationClosesStore(t *testing.T) {
	broken := storage.Migration{Version: 1, Name: "broken", Compat: storage.Additive, Seed: true,
		Up: func(context.Context, *storage.MigrationTx) error { return errors.New("bane broke it") }}
	path := tempStoragePath(t)
	c := testConfig(t, storageMod("alfred", nil, broken))
	c.StoragePath = path
	_, err := New(t.Context(), c)
	var failed *storage.ErrMigrationFailed
	if !errors.As(err, &failed) || !strings.Contains(err.Error(), "bane broke it") {
		t.Fatalf("boot with a failing step = %v, want *storage.ErrMigrationFailed", err)
	}
	c.Modules = []module.Module{storageMod("alfred", nil, seedStep(1, "cave"))}
	mustNew(t, c)
}

// TestUnservedModuleHeld (05-storage §5S.3.5 "Barrier and serving"): an
// enabled module whose seed waits on the seed of a disabled module is not
// started: no Register, Modules reports it disabled with reason "storage".
func TestUnservedModuleHeld(t *testing.T) {
	registered := false
	wayne := storageMod("wayne", func(*module.Registrar) error { registered = true; return nil },
		storage.Migration{Version: 1, Name: "after-bane", Compat: storage.Additive, Seed: true,
			After: []storage.StepRef{{Owner: "bane", Version: 1}},
			Up:    func(context.Context, *storage.MigrationTx) error { return nil }})
	bane := storageMod("bane", nil, seedStep(1, "venom")).with(func(m *module.Manifest) { m.DefaultEnabled = false })
	capture := logcapture.New(t)
	c := Config{AppID: "gotham", Logger: capture.Logger(), Modules: []module.Module{wayne, bane}, StoragePath: tempStoragePath(t)}
	k := mustNew(t, c)
	if registered {
		t.Fatal("a module storage does not serve was registered")
	}
	var state ModuleState
	for _, s := range k.Modules() {
		if s.ID == "wayne" {
			state = s
		}
	}
	if state.Enabled || state.Disabled != disabledByStorage {
		t.Fatalf("wayne's state = %+v, want disabled for storage", state)
	}
	if !slices.ContainsFunc(capture.Records(), func(r slog.Record) bool { return strings.Contains(r.Message, "module not started") }) {
		t.Fatal("holding a module logged nothing")
	}
}
