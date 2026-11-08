// file: core/kernel/storage_test.go
// version: 1.0.0
// guid: 36b3a2eb-1f55-4a42-83bc-47c859649615
// last-edited: 2026-11-08

package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	cpebble "github.com/cockroachdb/pebble/v2"

	"github.com/falkcorp/mediacore/coretest/logcapture"
	"github.com/falkcorp/mediacore/httpapi"
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

// readySignal is the test notifier with Ready closing a channel, so a test
// outside a synctest bubble can cancel Run once the app is ready.
type readySignal struct {
	notifier
	ready chan struct{}
}

func (n readySignal) Ready() error { close(n.ready); return nil }

// lateStopKernel boots gcpd with two hooks and a shutdown budget of budget.
// Stops run in reverse registration order: gordon's Stop holds until the
// budget is spent, so bullock's is reached after it and stopHooks hands it to
// the background with an expired ctx. bullock's Stop calls hold, then writes
// its keyspace and sends the result to wrote.
func lateStopKernel(t *testing.T, path string, budget time.Duration, hold func(), wrote chan<- error) (*Kernel, *module.Deps, *logcapture.Capture, chan struct{}) {
	t.Helper()
	deps := new(module.Deps)
	gcpd := storageMod("gcpd", func(r *module.Registrar) error {
		*deps = r.Deps()
		r.Hook(module.Hook{Name: "gcpd.bullock", Stop: func(context.Context) error {
			hold()
			err := deps.Storage.Keyspace.Set(context.Background(), []byte("report"), []byte("filed"))
			wrote <- err
			return err
		}})
		r.Hook(module.Hook{Name: "gcpd.gordon", Stop: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		}})
		return nil
	})
	rec := &recorder{}
	capture := logcapture.New(t)
	c := supervised(t, rec, gcpd)
	c.Logger = capture.Logger()
	ready := make(chan struct{})
	c.Notifier = readySignal{notifier: notifier{rec}, ready: ready}
	c.R1Values = func() R1Values { return R1Values{Listen: "localhost:0", ShutdownBudget: budget} }
	c.StoragePath = path
	return mustNew(t, c), deps, capture, ready
}

// TestCloseCoreAwaitsLateStops (R1-13 seams, verification review MINOR-2;
// 06 §6.5.2): a hook Stop that runs in the background after the shutdown
// budget still reaches its storage when it finishes within closeCore's
// floor, because closeCore waits for it before the store closes. One still
// running at the floor is logged at ERROR, and the store closes regardless.
func TestCloseCoreAwaitsLateStops(t *testing.T) {
	const budget = 300 * time.Millisecond

	t.Run("finishes-within-the-floor", func(t *testing.T) {
		path := tempStoragePath(t)
		wrote := make(chan error, 1)
		// bullock writes a fifth of a floor after it is called: long after
		// a close that did not wait, well inside one that does.
		k, _, _, ready := lateStopKernel(t, path, budget, func() { time.Sleep(lateStopFloor / 5) }, wrote)
		ctx, cancel := context.WithCancel(t.Context())
		done := run(ctx, k)
		<-ready
		cancel()
		<-done
		if err := <-wrote; err != nil {
			t.Fatalf("the late Stop's write = %v, want it to land before the store closed", err)
		}
		// Run closed the store, so the same directory boots again.
		re := mustNew(t, Config{AppID: "gotham", Logger: logcapture.New(t).Logger(),
			Modules: []module.Module{storageMod("gcpd", nil)}, StoragePath: path})
		ks, _ := re.store.db.Keyspace("gcpd")
		if v, err := ks.Get(t.Context(), []byte("report")); err != nil || string(v) != "filed" {
			t.Fatalf("the late Stop's row after a reboot = %q, %v; want filed", v, err)
		}
	})

	t.Run("still-running-at-the-floor", func(t *testing.T) {
		release, wrote := make(chan struct{}), make(chan error, 1)
		k, deps, capture, ready := lateStopKernel(t, tempStoragePath(t), budget, func() { <-release }, wrote)
		ctx, cancel := context.WithCancel(t.Context())
		done := run(ctx, k)
		<-ready
		begin := time.Now()
		cancel()
		<-done
		took := time.Since(begin)
		if took < budget+lateStopFloor || took > budget+lateStopFloor+5*time.Second {
			t.Fatalf("Run returned after %s, want the budget %s plus one floor %s", took, budget, lateStopFloor)
		}
		if _, err := deps.Storage.Keyspace.Get(t.Context(), []byte("report")); !errors.Is(err, storage.ErrClosed) {
			t.Fatalf("a read after Run returned = %v, want storage.ErrClosed: the store closes regardless", err)
		}
		if !slices.ContainsFunc(capture.Records(), func(r slog.Record) bool { return strings.Contains(r.Message, "still running at the close floor") }) {
			t.Fatal("a late Stop left running at the floor logged nothing")
		}
		close(release)
		if err := <-wrote; !errors.Is(err, storage.ErrClosed) {
			t.Fatalf("the late Stop's write after the close = %v, want storage.ErrClosed", err)
		}
	})
}

// failedUpgrade boots a fresh store at a new path with alfred's seed, then an
// upgrade whose step fails, and returns the path and the open checkpoint's
// name.
func failedUpgrade(t *testing.T) (path, name string) {
	t.Helper()
	path = tempStoragePath(t)
	c := testConfig(t, storageMod("alfred", nil, seedStep(1, "cave")))
	c.StoragePath = path
	k := mustNew(t, c)
	if err := k.closeCore(t.Context(), time.Now().Add(defaultShutdownBudget)); err != nil {
		t.Fatal(err)
	}
	c.Modules = []module.Module{storageMod("alfred", nil, seedStep(1, "cave"), storage.Migration{
		Version: 2, Name: "broken", Compat: storage.Additive,
		Up: func(context.Context, *storage.MigrationTx) error { return errors.New("bane broke it") }})}
	var failed *storage.ErrMigrationFailed
	if _, err := New(t.Context(), c); !errors.As(err, &failed) {
		t.Fatalf("the failing upgrade = %v, want *storage.ErrMigrationFailed", err)
	}
	raw, err := os.ReadFile(path + ".migration-checkpoint")
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		Name  string `json:"name"`
		State string `json:"state"`
	}
	if err := json.Unmarshal(raw, &p); err != nil || p.Name == "" || p.State != "open" {
		t.Fatalf("pointer = %s (%v), want an open checkpoint", raw, err)
	}
	return path, p.Name
}

// checkpointStatus reads a checkpoint record's status and ready_at.
func checkpointStatus(path, name string) (status string, readyAt *time.Time, err error) {
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(path), ".mediacore-checkpoints", name+".json"))
	if err != nil {
		return "", nil, err
	}
	var rec struct {
		Status  string     `json:"status"`
		ReadyAt *time.Time `json:"ready_at"`
	}
	err = json.Unmarshal(raw, &rec)
	return rec.Status, rec.ReadyAt, err
}

// readyProbe is the test notifier whose Ready runs probe first, so a test
// sees the store as it was when READY=1 was sent.
type readyProbe struct {
	notifier
	probe func()
	ready chan struct{}
}

func (n readyProbe) Ready() error { n.probe(); close(n.ready); return nil }

// previousBuild is the build before the failed upgrade's: alfred with its
// seed only, so its boot runs no step and takes no checkpoint, then serves.
func previousBuild(t *testing.T, path string, rec *recorder, probe func()) (*Kernel, chan struct{}) {
	t.Helper()
	c := supervised(t, rec, storageMod("alfred", nil, seedStep(1, "cave")))
	ready := make(chan struct{})
	c.Notifier = readyProbe{notifier: notifier{rec}, probe: probe, ready: ready}
	c.StoragePath = path
	return mustNew(t, c), ready
}

// TestReadinessMarksCheckpointServed (05-storage §5S.3.12 "Upgrade attempt
// and reuse"; review BLOCKER-1): after a failed upgrade the previous build
// boots and serves; the open record carries ready_at and status served
// before the first start hook runs, and so by READY=1, so the next upgrade
// takes its own checkpoint. A boot that writes from a start hook and dies
// before READY=1 (its listener fails) has still marked the record, and the
// next upgrade's fresh checkpoint holds that write. A record that cannot be
// written stops the app before anything starts.
func TestReadinessMarksCheckpointServed(t *testing.T) {
	t.Run("served", func(t *testing.T) {
		path, name := failedUpgrade(t)
		var status string
		var readyAt *time.Time
		var readErr error
		k, ready := previousBuild(t, path, &recorder{}, func() { status, readyAt, readErr = checkpointStatus(path, name) })
		ctx, cancel := context.WithCancel(t.Context())
		done := run(ctx, k)
		<-ready
		cancel()
		if err := <-done; err != nil {
			t.Fatalf("Run = %v", err)
		}
		if readErr != nil || status != "served" || readyAt == nil {
			t.Fatalf("the record at READY=1: status %q, ready_at %v (%v); want served with ready_at", status, readyAt, readErr)
		}
	})
	t.Run("unwritable", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root writes through a read-only directory")
		}
		path, name := failedUpgrade(t)
		dir := filepath.Join(filepath.Dir(path), ".mediacore-checkpoints")
		rec := &recorder{}
		k, _ := previousBuild(t, path, rec, func() {})
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		// A Run that wrongly reaches readiness serves until ctx ends; the
		// deadline turns that into a failure instead of a hang.
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		err := k.Run(ctx)
		if err == nil || !strings.Contains(err.Error(), "ready_at") {
			t.Fatalf("Run with an unwritable record = %v, want the readiness write's error", err)
		}
		if slices.Contains(rec.list(), "ready") {
			t.Fatal("READY=1 was sent although ready_at was not written")
		}
		if got := rec.matching("listen:"); len(got) != 0 {
			t.Fatalf("Run went on to the listener (%v) although ready_at was not written", got)
		}
		if status, readyAt, err := checkpointStatus(path, name); err != nil || status != "open" || readyAt != nil {
			t.Fatalf("the record = %q, %v (%v); want it still open", status, readyAt, err)
		}
	})
	t.Run("start-hook-write-then-death", func(t *testing.T) {
		path, old := failedUpgrade(t)
		var deps module.Deps
		seen := make(chan string, 1)
		alfred := storageMod("alfred", func(r *module.Registrar) error {
			deps = r.Deps()
			r.Hook(module.Hook{Name: "alfred.lamp", Start: func(ctx context.Context) error {
				status, _, err := checkpointStatus(path, old)
				if err != nil {
					status = err.Error()
				}
				seen <- status
				return deps.Storage.Keyspace.Set(ctx, []byte("batcave"), []byte("lit"))
			}})
			return nil
		}, seedStep(1, "cave"))
		rec := &recorder{}
		c := supervised(t, rec, alfred)
		c.Listen = func(httpapi.ListenOptions, http.Handler, *slog.Logger) (Server, error) {
			return nil, errors.New("arkham is locked down")
		}
		c.StoragePath = path
		k := mustNew(t, c)
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		if err := k.Run(ctx); err == nil || !strings.Contains(err.Error(), "arkham is locked down") {
			t.Fatalf("Run with a failing listener = %v, want the listener's error", err)
		}
		if slices.Contains(rec.list(), "ready") {
			t.Fatal("READY=1 was sent although the listener failed")
		}
		select {
		case status := <-seen:
			if status != "served" {
				t.Fatalf("the record when the start hook ran = %q, want served", status)
			}
		default:
			t.Fatal("the start hook never ran")
		}
		// The fixed build's upgrade takes its own checkpoint, which holds the
		// row the start hook wrote before the boot died.
		c = testConfig(t, storageMod("alfred", nil, seedStep(1, "cave"), storage.Migration{
			Version: 2, Name: "fixed", Compat: storage.Additive,
			Up: func(_ context.Context, tx *storage.MigrationTx) error { return tx.Set([]byte("fixed"), []byte("1")) }}))
		c.StoragePath = path
		got := mustNew(t, c).store.db.CheckpointName()
		if got == "" || got == old {
			t.Fatalf("the upgrade after the dead boot used checkpoint %q, want a fresh one (not %q)", got, old)
		}
		if !checkpointHas(t, path, got, "m/alfred/batcave") {
			t.Fatalf("checkpoint %s lacks the row the start hook wrote", got)
		}
	})
}

// checkpointHas reports whether checkpoint name's copy of the store holds the
// raw key.
func checkpointHas(t *testing.T, path, name, key string) bool {
	t.Helper()
	dir := filepath.Join(filepath.Dir(path), ".mediacore-checkpoints", name, "db")
	db, err := cpebble.Open(dir, &cpebble.Options{ReadOnly: true, Logger: quietPebble{}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	_, closer, err := db.Get([]byte(key))
	if errors.Is(err, cpebble.ErrNotFound) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	_ = closer.Close()
	return true
}

// quietPebble drops Pebble's informational logging in checkpointHas.
type quietPebble struct{}

func (quietPebble) Infof(string, ...any)  {}
func (quietPebble) Errorf(string, ...any) {}
func (quietPebble) Fatalf(format string, args ...any) {
	panic(fmt.Sprintf(format, args...))
}
