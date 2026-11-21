// file: core/examples/hello/crash_test.go
// version: 1.0.9
// guid: d2573b3f-2985-4783-baa2-5b0ee3888984
// last-edited: 2026-11-19

package hello_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/coretest/crashmatrix"
	"github.com/falkcorp/mediacore/examples/hello"
	ipebble "github.com/falkcorp/mediacore/internal/pebble"
	"github.com/falkcorp/mediacore/kernel"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/keys"
	"github.com/falkcorp/mediacore/storage/restorejournal"
	"github.com/falkcorp/mediacore/storage/restorejournal/restorejournaltest"
	"github.com/falkcorp/mediacore/tasks"
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
// asserts no row lost or doubled and the migration done. hello's own boots
// run the config boot (CoreConfigBoot, F3-07 a6), so P0's and P4b's config
// and secrets writers are broken by the scenarios above; these cells still
// exercise the entry point, not a full boot, because hello declares no data
// migration (F3-07's slice (d) registers books' M2, whose
// TestBootCrashMatrix_ABO cells cover the boot path).
func dataMigrationCells() []dataMigrationCell {
	var out []dataMigrationCell
	for _, l := range crashmatrix.DataMigrationLabels() {
		s := crashmatrix.SyntheticCutover{Owner: "hello", Spec: hello.New().Manifest().Storage,
			Parallelism: tasks.Fixed(4, "test: four disjoint shards of the fixture, so every shard's chunks are broken")}.Scenario()
		s.Labels = []string{l}
		// The chunk's cell breaks every chunk boundary of every shard and a
		// point inside each chunk; the plan's and the state's break their
		// own writes only.
		s.Inside = l == crashmatrix.DataMigrationLabels()[1]
		s.NoChunkStarts = !s.Inside
		out = append(out, dataMigrationCell{label: l, ChunkScenario: s})
	}
	// M8 (F3-04): a kill at every write of the store rewrite, over hello's
	// storage spec, booted as the kernel's boot runs a manifest's
	// Manifest.Rewrite since F3-07 a4 (restoreswap's P0a with the Swapper
	// serving M8, then the DAG with M8 last). hello declares no M8, so the
	// cells drive that path directly.
	// The second cell starts after one swap-back: M8's one restart. Two
	// consecutive swap-backs refuse the boot (storage.ErrRewriteSwappedBack,
	// exit 78), which no cell of a completing boot can hold;
	// internal/restoreswap's TestRewriteSwapBackMovesNoCheckpoint has it.
	spec := hello.New().Manifest().Storage
	return append(out,
		dataMigrationCell{label: "m8-rewrite", ChunkScenario: crashmatrix.SyntheticRewrite{Owner: "hello", Spec: spec}.Scenario()},
		dataMigrationCell{label: "m8-rewrite-after-swap-back", ChunkScenario: crashmatrix.SyntheticRewrite{Owner: "hello", Spec: spec, SwappedBack: true}.Scenario()})
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
	// The journal's writers on a non-default branch of the re-apply: no hello
	// fixture reaches them before F4-08 binds the journal (and then only the
	// scenarios F4-08 adds); each entry goes when a scenario reaches its label.
	out := crashmatrix.JournalUnreachedLabels()
	maps.Copy(out, map[string]string{
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
		// statefs.MkdirAll performs no operation on a directory that exists,
		// as os.MkdirAll stats first and never calls mkdir(2) on one, and
		// the kit creates every fixture's state directory before the boot
		// (crashmatrix's load): no fixture boots without one. A kill before
		// the mkdir of an absent state directory leaves nothing; an error
		// there is config.ResolveStateDir's HEAD fallback, beside the store,
		// which config's TestStateDirFallbackMatchesHEAD rows cover.
		"config.statedir.mkdir": "every fixture's state directory exists before its boot (crashmatrix's load creates it), and P0's mkdir of an existing directory is no operation",
	})
	return out
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
		bootRecordUnparsable(t),
		keyringStaleTemp(t),
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
		_, err := inj.New(context.Background(), app.Options{AppID: "hello", CoreConfigBoot: true, Logger: slog.New(slog.DiscardHandler)}, mod)
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

// bootRecordUnparsable is a served store whose boot record does not decode:
// P0 sets it aside as config.boot.json.bad and boots on the operator layers
// (05-config §1.5; config.bootrecord.quarantine).
func bootRecordUnparsable(t *testing.T) crashmatrix.Fixture {
	f := crashmatrix.Fixture{Name: "boot-record-unparsable", StateDir: t.TempDir()}
	boot(t, f, hello.New())
	if err := os.WriteFile(filepath.Join(f.StateDir, "config.boot.json"), []byte(`{"format": "mediacore-boot/1", "values": `), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

// keyringStaleTemp is a served store beside a keyring temporary file a
// crashed keyring write left (its directory entry made durable): secrets.Open
// removes it before it resolves the keyring (secrets.keyring.stale).
func keyringStaleTemp(t *testing.T) crashmatrix.Fixture {
	f := crashmatrix.Fixture{Name: "keyring-stale-temp", StateDir: t.TempDir()}
	boot(t, f, hello.New())
	if _, err := os.Stat(filepath.Join(f.StateDir, "secrets.keyring")); err != nil {
		t.Fatalf("the gated fixture boot wrote no keyring: %v", err)
	}
	if err := os.WriteFile(filepath.Join(f.StateDir, "secrets.keyring.tmp-0123456789ab"), []byte("half a keyring"), 0o600); err != nil {
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

// The secrets writes of the config boot (F3-07: the keyring file and
// c/secrets/meta, P4b first) as hello meets them. hello holds no secret of
// its own, so sealer (below) writes one through Deps.Secrets, the module scope
// every module has, and every cell asks the same questions of the recovery
// boot: the keyring is whole with no stale temporary file beside it,
// c/secrets/meta names this app and a key number at least the keyring's, and
// the sealed value reads back.
const (
	sealerName  secrets.Name = "modules.hello.oracle_token"
	sealerValue string       = "wayne-enterprises-uplink-7"
)

// sealer is hello that, in Register, reads its secret back and writes it
// when it was never set. Register runs once the store is open, after the
// keyring and c/secrets/meta, so a boot that sealed it under a keyring that
// was then lost fails the next boot's Register (secrets.ErrKeyUnavailable,
// or Open's ErrKeyMissing before it) instead of passing silently. ready
// counts the boots that read the value back.
type sealer struct {
	*hello.Module
	ready *atomic.Int64
}

func (m sealer) Register(r *module.Registrar) error {
	if err := m.Module.Register(r); err != nil {
		return err
	}
	ctx, sc := context.Background(), r.Deps().Secrets
	if _, _, err := sc.Get(ctx, sealerName); errors.Is(err, secrets.ErrNotSet) {
		if _, err := sc.Put(ctx, sealerName, secrets.NewValue(sealerValue), "user:alfred"); err != nil {
			return fmt.Errorf("hello sealer: put: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("hello sealer: get: %w", err)
	}
	v, _, err := sc.Get(ctx, sealerName)
	if err != nil {
		return fmt.Errorf("hello sealer: read back: %w", err)
	}
	if v.Reveal() != sealerValue {
		return errors.New("hello sealer: the secret reads back as another value")
	}
	m.ready.Add(1)
	return nil
}

// helloSecretsState is the secrets files and rows a boot left, read straight
// from fsys.
type helloSecretsState struct {
	keyring    []byte // nil when the file does not exist
	keyringMax int    // the highest key number in it
	temps      []string
	meta       map[string]any // c/secrets/meta's JSON; nil when the row does not exist
}

func readHelloSecrets(t *testing.T, fsys vfs.FS, f crashmatrix.Fixture) helloSecretsState {
	t.Helper()
	var st helloSecretsState
	names, err := fsys.List(f.StateDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("list the state directory: %v", err)
	}
	for _, n := range names {
		if strings.HasPrefix(n, "secrets.keyring.tmp-") {
			st.temps = append(st.temps, n)
		}
	}
	if h, err := fsys.Open(filepath.Join(f.StateDir, "secrets.keyring")); err == nil {
		raw := make([]byte, 1<<16)
		n, rerr := h.ReadAt(raw, 0)
		if cerr := h.Close(); cerr != nil || (rerr != nil && !errors.Is(rerr, io.EOF)) {
			t.Fatalf("read the keyring: %v %v", rerr, cerr)
		}
		st.keyring = raw[:n]
		var kf struct {
			Keys []struct {
				ID string `json:"id"`
			} `json:"keys"`
		}
		if err := json.Unmarshal(st.keyring, &kf); err != nil {
			return st // a keyring that does not parse is reported by the caller
		}
		for _, k := range kf.Keys {
			if num, err := strconv.Atoi(strings.TrimLeft(k.ID, "kd")); err == nil {
				st.keyringMax = max(st.keyringMax, num)
			}
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("open the keyring: %v", err)
	}
	db := dbPath(f)
	if _, err := fsys.Stat(db); err != nil {
		return st
	}
	pdb, err := pebble.Open(db, &pebble.Options{FS: fsys, ReadOnly: true, ErrorIfNotExists: true})
	if err != nil {
		t.Fatalf("open the store read-only: %v", err)
	}
	raw, closer, err := pdb.Get(append([]byte("c/secrets/"), keys.Join(keys.Str("meta"))...))
	switch {
	case errors.Is(err, pebble.ErrNotFound):
	case err != nil:
		t.Fatalf("read c/secrets/meta: %v", err)
	default:
		if err := json.Unmarshal(raw, &st.meta); err != nil {
			t.Fatalf("c/secrets/meta does not parse: %v", err)
		}
		_ = closer.Close()
	}
	if err := pdb.Close(); err != nil {
		t.Fatal(err)
	}
	return st
}

// helloSecretsViolations is what is wrong with a state that must be
// consistent: a keyring that exists parses; c/secrets/meta, when present,
// names hello and a max_key_id no lower than the keyring's (a keyring
// numbered past it would give a retired key's number to new material).
func helloSecretsViolations(st helloSecretsState, afterRecovery bool) []string {
	var out []string
	if st.keyring != nil && !json.Valid(st.keyring) {
		out = append(out, "secrets.keyring exists and is not whole JSON")
	}
	if st.keyring != nil && st.keyringMax == 0 {
		out = append(out, "secrets.keyring names no key number")
	}
	if st.meta != nil {
		if st.meta["app_id"] != "hello" {
			out = append(out, fmt.Sprintf("c/secrets/meta names app %v, not hello", st.meta["app_id"]))
		}
		if mk, _ := st.meta["max_key_id"].(float64); st.keyring != nil && int(mk) < st.keyringMax {
			out = append(out, fmt.Sprintf("c/secrets/meta max_key_id %d is below the keyring's %d", int(mk), st.keyringMax))
		}
	}
	if afterRecovery {
		switch {
		case st.keyring == nil:
			out = append(out, "no keyring after the recovery boot")
		case st.meta == nil:
			out = append(out, "no c/secrets/meta after the recovery boot")
		}
		if len(st.temps) > 0 {
			out = append(out, fmt.Sprintf("stale keyring temporary files left after the recovery boot: %v", st.temps))
		}
	}
	return out
}

// TestBootCrashMatrix_SecretsCells kills hello's first boot at each write of
// the keyring (the exclusive temporary file, its fsync, the link, the temp's
// removal, the directory fsync) and of c/secrets/meta, and reboots: the keyring
// is whole or absent after the kill, and the recovery boot ends with the
// keyring, a c/secrets/meta that names hello and covers the keyring, no stale
// temporary file, and the sealed secret read back.
func TestBootCrashMatrix_SecretsCells(t *testing.T) {
	cells := []struct {
		label string
		nth   int
		// sealed: the fixture is a state already booted once, with the
		// secret sealed under its keyring, so the recovery boot must read
		// the value an earlier boot wrote, not write it again.
		sealed bool
	}{
		{"secrets.keyring", 0, false}, {"secrets.keyring", 1, false}, {"secrets.keyring", 2, false},
		{"secrets.keyring", 3, false}, {"secrets.keyring", 4, false},
		{"secrets.meta", 0, false},
		// A boot over a present keyring syncs its directory before it trusts
		// the file (secrets.keyring.load-sync); killed there, the sealed
		// secret and its keyring both survive.
		{"secrets.keyring.load-sync", 0, true},
	}
	for _, c := range cells {
		t.Run(fmt.Sprintf("%s-%d", strings.TrimPrefix(c.label, "secrets."), c.nth), func(t *testing.T) {
			t.Parallel()
			var ready atomic.Int64
			mod := sealer{Module: hello.New(), ready: &ready}
			f := crashmatrix.Fixture{Name: "secrets-cell", StateDir: t.TempDir()}
			if c.sealed {
				boot(t, f, mod)
				if ready.Load() == 0 {
					t.Fatal("the fixture boot did not seal and read back the secret")
				}
			}
			clone, err := crashmatrix.KillAt(t, f, build(mod), c.label, c.nth)(nil)
			if err != nil {
				t.Fatal(err)
			}
			ready.Store(0) // KillAt's own boot ran to readiness; only a recovery boot counts
			if v := helloSecretsViolations(readHelloSecrets(t, clone, f), false); len(v) > 0 {
				t.Fatalf("the state a kill before %s #%d left: %v", c.label, c.nth, v)
			}
			if err := os.RemoveAll(f.StateDir); err != nil {
				t.Fatal(err)
			}
			if _, err := vfs.Clone(clone, vfs.Default, f.StateDir, f.StateDir); err != nil {
				t.Fatalf("restore the crash clone: %v", err)
			}
			var errs []error
			for boot := 0; boot < 3 && ready.Load() == 0; boot++ {
				errs = append(errs, crashmatrix.Boot(t, build(mod), f))
			}
			if ready.Load() == 0 {
				t.Fatalf("no recovery boot read the sealed secret back: %v", errs)
			}
			if v := helloSecretsViolations(readHelloSecrets(t, vfs.Default, f), true); len(v) > 0 {
				t.Fatalf("after the recovery boot: %v", v)
			}
		})
	}
}
