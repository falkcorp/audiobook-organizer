// file: core/examples/hello/crash_test.go
// version: 1.0.6
// guid: d2573b3f-2985-4783-baa2-5b0ee3888984
// last-edited: 2026-11-10

package hello_test

import (
	"context"
	"errors"
	"fmt"
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
	"github.com/falkcorp/mediacore/storage/restorejournal/restorejournaltest"
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
	for _, c := range dataMigrationCells() {
		t.Run("data-migration/"+c.label, func(t *testing.T) {
			t.Parallel()
			crashmatrix.RunChunks(t, c.ChunkScenario)
		})
	}
}

type dataMigrationCell struct {
	label string
	crashmatrix.ChunkScenario
}

// dataMigrationCells are the cells of the data-migration runner's three
// boot writers (06 §6.5.1 "Boot writers"; F2-06): the plan with the
// running state, each chunk's batch with its shard cursor, and the
// verifying and done states. No hello boot writes them: hello declares no
// data migration, and the boot call and Manifest.DataMigrations are
// F2-16's. So each cell drives the kernel's P4b entry point directly
// (kernel.NewDataMigrations, its Steps in storage.Open's DataSteps, then
// Prepare and Migrate), over a store opened as hello's boot opens it
// (hello's storage spec, plus the synthetic family's claim), with the
// synthetic cutover migration of crashmatrix (2 400 rows, tasks.Fixed(4)):
// it kills at that writer, boots again through the same entry point and
// asserts no row lost or doubled and the migration done. These cells
// exercise the entry point, not a full boot, until a manifest declares a
// data migration (F3-07 registers M2, whose TestBootCrashMatrix_ABO cells
// cover the boot path).
func dataMigrationCells() []dataMigrationCell {
	var out []dataMigrationCell
	for _, l := range crashmatrix.DataMigrationLabels() {
		s := crashmatrix.SyntheticCutover{Owner: "hello", Spec: hello.New().Manifest().Storage}.Scenario()
		s.Labels = []string{l}
		// The chunk's cell breaks every chunk boundary of every shard and a
		// point inside each chunk; the plan's and the state's break their
		// own writes only.
		s.Inside = l == crashmatrix.DataMigrationLabels()[1]
		s.NoChunkStarts = !s.Inside
		out = append(out, dataMigrationCell{label: l, ChunkScenario: s})
	}
	return out
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
	for _, c := range dataMigrationCells() {
		for l := range crashmatrix.ObservedChunks(t, c.ChunkScenario) {
			observed[l] = true
		}
	}
	missing, stale := crashmatrix.Unobserved(crashmatrix.Rows(), observed, unreachedLabels())
	for _, l := range missing {
		t.Errorf("label %s: no fixture's fault-free boot reaches it, so the matrix never breaks that writer", l)
	}
	for _, l := range stale {
		t.Errorf("allowlist entry %s: a fixture reaches it, or no writer-table row lists it; drop it", l)
	}
}

// unreachedLabels are the writer-table labels no hello fixture can reach,
// each with the reason (R1-21 review M4).
func unreachedLabels() map[string]string {
	return map[string]string{
		// storage/levels.go: the rollback rewrites the sidecars from the
		// committed stamps only after a level step's sidecar write fails
		// (a stamp commit's failure is fatal in Pebble). The level-step
		// scenario's error runs at storage.levelstep.sidecars run it.
		"storage.levelstep.rollback": "runs only after a level step's sidecar write fails",
		// The kit's clock is fixed (crashmatrix.Now) and a fixture's files
		// are copied into memory with the host's current time as their
		// mtime, so no scratch entry is 7 days old at a kit boot. Storage's
		// TestDryRunScratchModeAndSweep deletes one on a fake clock.
		"storage.sweeper.scratch": "a copied fixture file's mtime is the host's clock, never 7 days before the kit's fixed clock",
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
	// A checkpoint restore whose migration step fails journals failed and
	// refuses this binary (05-backup §5B.3.4's error table, DATA7-12). Only
	// the step's own failure does (R1-21 review (f)): an I/O error in the
	// migration bookkeeping halts at opened and the next boot retries, so
	// the refusal is declared only on the fixture whose step fails, and I5
	// accepts it only where the fault-free boot refuses too (review (d)).
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
		restore(t, "restore-checkpoint", restorejournal.KindCheckpoint, false, withTag("staged")),
		restore(t, "restore-bad-level", restorejournal.KindArchive, false, levelVariant()),
		restore(t, "restore-bad-level-location", restorejournal.KindArchive, true, levelVariant()),
		unjournaled(t),
		slotBothNames(t),
		reset(t, restorejournal.ResetStore),
		reset(t, restorejournal.ResetFactory),
		rejected(t, "intent-stale", func(in *restorejournal.Intent) {
			in.RequestedAt, in.ExpiresAt = crashmatrix.Now().Add(-2*time.Hour), crashmatrix.Now().Add(-time.Hour)
		}),
		rejected(t, "intent-foreign", func(in *restorejournal.Intent) { in.StagedByBuild = "joker-build" }),
		undecodable(t),
		checkpointReused(t),
		attemptStranded(t),
		checkpointsToSweep(t),
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
	if err := crashmatrix.StageWith(f, in, restorejournaltest.OtherDevice{Probe: restorejournal.OSProbe(), Dirs: in.Delete}); err != nil {
		t.Fatalf("fixture %s: stage: %v", name, err)
	}
	return f
}

// unjournaled is a staged restore whose staged record a kill between
// Stage's create and its record lost: pending.json beside an empty journal
// directory, so P0a writes the record first (swap.journal.staged).
func unjournaled(t *testing.T) crashmatrix.Fixture {
	f := restore(t, "intent-unjournaled", restorejournal.KindArchive, false, withTag("staged"))
	journal := filepath.Join(restorejournal.JournalDir(f.StateDir, "r-intent-unjournaled"), restorejournal.JournalName)
	f.Mutate = func(mem *vfs.MemFS) (*vfs.MemFS, error) { return mem, mem.Remove(journal) }
	return f
}

// slotBothNames is a restore killed inside its in-flight rename, after the
// journal directory's sync and before the slot directory's: pending.json
// and inflight.json both name the intent, and the next P0a removes the
// former (swap.slot.cleanup).
func slotBothNames(t *testing.T) crashmatrix.Fixture {
	f := restore(t, "slot-both-names", restorejournal.KindArchive, false, withTag("staged"))
	// The rename, the journal directory's sync, the slot directory's sync.
	f.Mutate = crashmatrix.KillAt(t, f, build(hello.New()), "swap.slot.inflight", 2)
	return f
}

// undecodable is a live store and a pending.json that is no intent: P0a
// moves it to restore/rejected-<time>.json and boots plain
// (swap.slot.invalid).
func undecodable(t *testing.T) crashmatrix.Fixture {
	f := crashmatrix.Fixture{Name: "intent-undecodable", StateDir: t.TempDir()}
	boot(t, f, withTag("live"))
	pending := restorejournal.PendingPath(f.StateDir)
	if err := os.MkdirAll(filepath.Dir(pending), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pending, []byte(`{"id": "r-riddler", "kind": `), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

// checkpointReused is upgradePendingStep after a boot killed before hello's
// step committed: its checkpoint is open, so the next boot reuses it
// instead of taking another (storage.checkpoint.reuse, a crash loop).
func checkpointReused(t *testing.T) crashmatrix.Fixture {
	f := upgradePendingStep(t)
	f.Name = "checkpoint-reused"
	f.Mutate = crashmatrix.KillAt(t, f, build(hello.New()), "storage.step.batch", 0)
	return f
}

// attemptStranded is upgradePendingStep after a boot killed past step 9's
// batch and before its checkpoint was completed: the next boot completes
// the stranded attempt first (storage.checkpoint.stranded).
func attemptStranded(t *testing.T) crashmatrix.Fixture {
	f := upgradePendingStep(t)
	f.Name = "attempt-stranded"
	f.Mutate = crashmatrix.KillAt(t, f, build(hello.New()), "storage.checkpoint.complete", 0)
	return f
}

// checkpointsToSweep is a store with nothing to run (so P4a takes no
// checkpoint and runs no garbage sweep) beside three things the checkpoint
// sweeper's first sweep after readiness handles (05-storage §5S.3.12
// "Retention"; §5S.4 step 15): a completed checkpoint past its delete_after,
// which the pointer names (deleted: record, copy, pointer); another store's
// open checkpoint (released: delete_after = now + 7 days); and a checkpoint
// directory without a record (garbage). Each copy is an empty store, so it
// opens and holds no module row.
func checkpointsToSweep(t *testing.T) crashmatrix.Fixture {
	f := crashmatrix.Fixture{Name: "checkpoints-to-sweep", StateDir: t.TempDir()}
	boot(t, f, hello.New())
	dir := filepath.Join(filepath.Dir(dbPath(f)), ".mediacore-checkpoints")
	const (
		expired  = "20260301T000000Z-e9b1e001"
		orphaned = "20260302T000000Z-0b1e0002"
		garbage  = "20260303T000000Z-6a7b0003"
	)
	for _, n := range []string{expired, orphaned, garbage} {
		eng, err := ipebble.Open(filepath.Join(dir, n, "db"), ipebble.Options{FS: vfs.Default})
		if err != nil {
			t.Fatal(err)
		}
		if err := eng.Close(10 * time.Second); err != nil {
			t.Fatal(err)
		}
	}
	day := 24 * time.Hour
	completed, due := crashmatrix.Now().Add(-14*day), crashmatrix.Now().Add(-7*day)
	records := map[string]string{
		expired: fmt.Sprintf(`{"install_id":"arkham-install","epoch":1,"taken_at":%q,"completed_at":%q,"delete_after":%q,"status":"completed"}`,
			completed.Format(time.RFC3339), completed.Format(time.RFC3339), due.Format(time.RFC3339)),
		orphaned: fmt.Sprintf(`{"install_id":"joker-install","epoch":1,"taken_at":%q,"status":"open"}`, completed.Format(time.RFC3339)),
	}
	for n, rec := range records {
		if err := os.WriteFile(filepath.Join(dir, n+".json"), []byte(rec+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(dbPath(f)+".migration-checkpoint", []byte(`{"name":"`+expired+`","state":"completed"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
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
