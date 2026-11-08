// file: core/examples/hello/crash_test.go
// version: 1.0.1
// guid: d2573b3f-2985-4783-baa2-5b0ee3888984
// last-edited: 2026-11-08

package hello_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/coretest/crashmatrix"
	"github.com/falkcorp/mediacore/examples/hello"
	ipebble "github.com/falkcorp/mediacore/internal/pebble"
	"github.com/falkcorp/mediacore/kernel"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// TestBootCrashMatrix is core's instance of the boot crash matrix (05-storage
// §5S.3.16, §5S.10; 06 §6.5.1; 09 §9.4): hello's boot broken at every
// durable write, rename and fsync of every kernel writer that exists at R1,
// by a kill and by an error return, over every starting state below, then
// rebooted to readiness with I2, I3, I5 and I6 checked.
func TestBootCrashMatrix(t *testing.T) {
	for _, s := range crashScenarios(t) {
		t.Run(s.name, func(t *testing.T) {
			t.Parallel()
			crashmatrix.Run(t, s.Scenario)
		})
	}
}

// TestBootCrashMatrixCoversEveryWriter: every row of 06 §6.5.1's writer
// table that exists in this build is exercised by a fixture's fault-free
// boot, every durable operation carries a registered label, and no boot
// writes past the boot filesystem. A writer added without a row, or a row
// no fixture reaches, fails here.
func TestBootCrashMatrixCoversEveryWriter(t *testing.T) {
	observed := map[string]bool{}
	for _, s := range crashScenarios(t) {
		for l := range crashmatrix.Observed(t, s.Scenario) {
			observed[l] = true
		}
	}
	for _, r := range crashmatrix.Uncovered(crashmatrix.Rows(), observed) {
		t.Errorf("writer-table row %s %q: no fixture reaches any of %v", r.Phase, r.Writer, r.Labels)
	}
}

type namedScenario struct {
	name string
	crashmatrix.Scenario
}

// crashScenarios are the matrix's boots and their starting states. Each
// fixture is written on the real filesystem by booting a hello variant
// (crashmatrix.Boot) and staged with crashmatrix.Stage; Run copies it into
// each run's in-memory filesystem.
func crashScenarios(t *testing.T) []namedScenario {
	t.Helper()
	// A checkpoint restore whose P4b fails journals failed and refuses this
	// binary (05-backup §5B.3.4's error table, DATA7-12): a legal end of an
	// error injected into its migration list as well as of a failing step.
	refused := func(f crashmatrix.Fixture) crashmatrix.Fixture {
		f.Refusal = kernel.ErrCheckpointRestoreFailedHere
		return f
	}
	plain := []crashmatrix.Fixture{
		{Name: "fresh", StateDir: t.TempDir()},
		markerless(t),
		upgradePendingStep(t),
		checkpointGarbage(t),
		upgradeFailed(t),
		restore(t, "restore-archive", restorejournal.KindArchive, true, withTag("staged")),
		restore(t, "restore-revert", restorejournal.KindRevert, false, withTag("staged")),
		refused(restore(t, "restore-checkpoint", restorejournal.KindCheckpoint, false, withTag("staged"))),
		restore(t, "restore-bad-level", restorejournal.KindArchive, false, levelVariant()),
		reset(t, restorejournal.ResetStore),
		reset(t, restorejournal.ResetFactory),
		rejected(t, "intent-stale", func(in *restorejournal.Intent) {
			in.RequestedAt, in.ExpiresAt = crashmatrix.Now().Add(-2*time.Hour), crashmatrix.Now().Add(-time.Hour)
		}),
		rejected(t, "intent-foreign", func(in *restorejournal.Intent) { in.StagedByBuild = "joker-build" }),
	}
	dag := refused(restore(t, "restore-checkpoint-dag-fails", restorejournal.KindCheckpoint, false, withTag("poisoned")))
	return []namedScenario{
		{"hello", crashmatrix.Scenario{Build: build(hello.New()), Fixtures: plain}},
		{"level-step", crashmatrix.Scenario{Build: build(levelVariant()), Fixtures: []crashmatrix.Fixture{levelStep(t)}}},
		{"poisoned-step", crashmatrix.Scenario{Build: build(poisonVariant()), Fixtures: []crashmatrix.Fixture{dag}}},
	}
}

// build boots mod as the crash matrix's Build.
func build(mod module.Module) func(testing.TB, *crashmatrix.Injector) error {
	return func(_ testing.TB, inj *crashmatrix.Injector) error {
		_, err := inj.New(context.Background(), app.Options{AppID: "hello", Logger: slog.New(slog.DiscardHandler)}, mod)
		return err
	}
}

// variant is hello with another storage declaration, and optionally a row
// its Register writes, name/<tag> = 1, so a fixture's stores are told apart
// (fixture building only: the matrix never boots a tagging variant).
type variant struct {
	*hello.Module
	spec storage.Spec
	tag  string
}

func (v variant) Manifest() module.Manifest {
	m := v.Module.Manifest()
	m.Storage = v.spec
	return m
}

func (v variant) Register(r *module.Registrar) error {
	if err := v.Module.Register(r); err != nil {
		return err
	}
	if v.tag == "" {
		return nil
	}
	return r.Deps().Storage.Keyspace.Set(context.Background(), []byte("name/"+v.tag), []byte("1"))
}

// seed is hello's one migration.
func seed() storage.Migration { return hello.New().Manifest().Storage.Migrations[0] }

func withTag(tag string) module.Module {
	return variant{Module: hello.New(), spec: hello.New().Manifest().Storage, tag: tag}
}

// levelVariant raises hello's level to 1 with a Breaking step (05-storage
// §5S.3.7: RaisesLevel goes with Breaking, and the guard refuses a stamp
// above the declared level, so the build declares Level 1).
func levelVariant() module.Module {
	return variant{Module: hello.New(), spec: storage.Spec{Keyspace: true, Level: 1, Migrations: []storage.Migration{
		seed(),
		{Version: 2, Name: "raise-level", Compat: storage.Breaking, RaisesLevel: 1,
			Up: func(context.Context, *storage.MigrationTx) error { return nil }},
	}}}
}

// poisonVariant's step 2 fails on a store holding name/poisoned: the
// checkpoint restore of such a store fails its migrations (DATA7-12).
func poisonVariant() module.Module {
	return variant{Module: hello.New(), spec: storage.Spec{Keyspace: true, Migrations: []storage.Migration{
		seed(),
		{Version: 2, Name: "refuse-poison", Compat: storage.Additive, Up: func(ctx context.Context, tx *storage.MigrationTx) error {
			_, err := tx.Keyspace().Get(ctx, []byte("name/poisoned"))
			switch {
			case err == nil:
				return errors.New("hello: name/poisoned is the Joker's")
			case errors.Is(err, storage.ErrNotFound):
				return nil
			}
			return err
		}},
	}}}
}

// boot writes f's store by booting mod over it on the real filesystem.
func boot(t *testing.T, f crashmatrix.Fixture, mod module.Module) {
	t.Helper()
	if err := crashmatrix.Boot(t, build(mod), f); err != nil {
		t.Fatalf("fixture %s: %v", f.Name, err)
	}
}

func dbPath(f crashmatrix.Fixture) string {
	if f.DBDir != "" {
		return f.DBDir
	}
	return filepath.Join(f.StateDir, "storage", "main.pebble")
}

// markerless is a store an older binary wrote: rows outside c/ and m/ and
// no install marker, so the first boot checkpoints it and writes the marker
// batch (05-storage §5S.4 step 5).
func markerless(t *testing.T) crashmatrix.Fixture {
	f := crashmatrix.Fixture{Name: "markerless", StateDir: t.TempDir()}
	db := dbPath(f)
	if err := os.MkdirAll(filepath.Dir(db), 0o700); err != nil {
		t.Fatal(err)
	}
	eng, err := ipebble.Open(db, ipebble.Options{FS: vfs.Default})
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.DB().Set([]byte("arkham/inmate/joker"), []byte("1"), nil); err != nil {
		t.Fatal(err)
	}
	if err := eng.Close(10 * time.Second); err != nil {
		t.Fatal(err)
	}
	return f
}

// upgradePendingStep is a store hello's step 1 has not run on (written by a
// build with no hello step), so the boot checkpoints it before the step.
func upgradePendingStep(t *testing.T) crashmatrix.Fixture {
	f := crashmatrix.Fixture{Name: "upgrade-pending-step", StateDir: t.TempDir()}
	boot(t, f, variant{Module: hello.New(), spec: storage.Spec{Keyspace: true}})
	return f
}

// checkpointGarbage is upgradePendingStep with a checkpoint directory that
// has no record (a crash before its record): P4a's garbage sweep deletes
// it before the headroom check (05-storage §5S.3.12 "How").
func checkpointGarbage(t *testing.T) crashmatrix.Fixture {
	f := upgradePendingStep(t)
	f.Name = "checkpoint-garbage"
	dir := filepath.Join(filepath.Dir(dbPath(f)), ".mediacore-checkpoints", "20260101T000000Z-deadbeef")
	if err := os.MkdirAll(filepath.Join(dir, "db"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "db", "OPTIONS-000001"), []byte("riddler"), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

// upgradeFailed is a store whose upgrade failed: the poisoned variant's
// step 2 fails after its checkpoint, which stays open. hello, the previous
// build, then boots it with nothing to run and marks the checkpoint served
// at readiness (ready_at, 05-storage §5S.3.12).
func upgradeFailed(t *testing.T) crashmatrix.Fixture {
	f := crashmatrix.Fixture{Name: "upgrade-failed", StateDir: t.TempDir()}
	boot(t, f, withTag("poisoned"))
	if err := crashmatrix.Boot(t, build(poisonVariant()), f); err == nil {
		t.Fatal("fixture upgrade-failed: the poisoned step succeeded")
	}
	return f
}

// levelStep is hello's store at level 0, booted by the level variant: the
// level step writes its sidecar before its stamp (machine 2).
func levelStep(t *testing.T) crashmatrix.Fixture {
	f := crashmatrix.Fixture{Name: "level-step", StateDir: t.TempDir()}
	boot(t, f, hello.New())
	return f
}

// restore is a live hello store tagged name/live and a staged store written
// by staged (tagged, or at a level hello refuses), staged as a kind
// restore; with location, the state directory holds backup.location.json,
// which step c copies and step x puts back (05-backup §5B.3.6).
func restore(t *testing.T, name string, kind restorejournal.Kind, location bool, staged module.Module) crashmatrix.Fixture {
	f := crashmatrix.Fixture{Name: name, StateDir: t.TempDir()}
	boot(t, f, withTag("live"))
	id := "r-" + name
	g := f
	g.DBDir = filepath.Join(restorejournal.StagingDir(dbPath(f), id), "main.pebble")
	boot(t, g, staged)
	if location {
		if err := os.WriteFile(filepath.Join(f.StateDir, "backup.location.json"), []byte(`{"generation":"gotham-1"}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	in := restorejournal.Intent{ID: id, Kind: kind, Targets: []restorejournal.Target{{From: g.DBDir, To: dbPath(f)}}}
	if err := crashmatrix.Stage(f, in); err != nil {
		t.Fatalf("fixture %s: stage: %v", name, err)
	}
	return f
}

// reset is a live hello store and a staged reset of kind: the store and its
// level sidecar set aside, and for a factory reset an auxiliary directory
// deleted (the intent's delete entries, COR8-4).
func reset(t *testing.T, kind restorejournal.ResetKind) crashmatrix.Fixture {
	name := "reset-" + string(kind)
	f := crashmatrix.Fixture{Name: name, StateDir: t.TempDir()}
	boot(t, f, withTag("live"))
	id := "z-" + name
	db := dbPath(f)
	in := restorejournal.Intent{ID: id, Kind: restorejournal.KindReset, Reset: kind}
	for _, p := range []string{db, db + restorejournal.LevelsSuffix} {
		in.Targets = append(in.Targets, restorejournal.Target{From: p, To: restorejournal.SetAsideDest(f.StateDir, db, id, p)})
	}
	if kind == restorejournal.ResetFactory {
		aux := filepath.Join(f.StateDir, "batcave-index")
		if err := os.MkdirAll(aux, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(aux, "000001.sst"), []byte("penguin"), 0o600); err != nil {
			t.Fatal(err)
		}
		in.Delete = []string{aux}
	}
	// The delete entry is a Recreatable auxiliary DB off the DB's
	// filesystem (05-storage §5S.3.15); the test host has one, so the probe
	// says the entry is on another.
	if err := crashmatrix.StageWith(f, in, otherDevice{Probe: restorejournal.OSProbe(), dirs: in.Delete}); err != nil {
		t.Fatalf("fixture %s: stage: %v", name, err)
	}
	return f
}

// otherDevice is a probe that reports every path under dirs on a device of
// its own.
type otherDevice struct {
	restorejournal.Probe
	dirs []string
}

func (p otherDevice) shift(path string, dev uint64, err error) (uint64, error) {
	for _, d := range p.dirs {
		if err == nil && restorejournal.InTree(path, d) {
			return dev + 1, nil
		}
	}
	return dev, err
}

func (p otherDevice) ItemDev(path string) (uint64, error) {
	dev, err := p.Probe.ItemDev(path)
	return p.shift(path, dev, err)
}

func (p otherDevice) DirDev(path string) (uint64, error) {
	dev, err := p.Probe.DirDev(path)
	return p.shift(path, dev, err)
}

// rejected is a restore intent P0a must reject (step a), edited by edit.
func rejected(t *testing.T, name string, edit func(*restorejournal.Intent)) crashmatrix.Fixture {
	f := crashmatrix.Fixture{Name: name, StateDir: t.TempDir()}
	boot(t, f, withTag("live"))
	id := "x-" + name
	g := f
	g.DBDir = filepath.Join(restorejournal.StagingDir(dbPath(f), id), "main.pebble")
	boot(t, g, withTag("staged"))
	in := restorejournal.Intent{ID: id, Kind: restorejournal.KindArchive, Targets: []restorejournal.Target{{From: g.DBDir, To: dbPath(f)}}}
	edit(&in)
	if err := crashmatrix.Stage(f, in); err != nil {
		t.Fatalf("fixture %s: stage: %v", name, err)
	}
	return f
}
