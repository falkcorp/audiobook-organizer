// file: internal/books/crash_matrix_test.go
// version: 1.0.7
// guid: 1a6f3d82-9c4e-4b17-8e25-7d0b5c9a2f61
// last-edited: 2026-11-11

package books

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/backup"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/coretest/crashmatrix"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// TestBootCrashMatrix_ABO is ABO's instance of the boot crash matrix
// (05-storage §5S.3.16, §5S.10; 06 §6.5.1; 09 §9.4 R1): R1-21's kit over
// books' storage boot, broken at every durable write, rename and fsync from
// P0a to readiness, by a kill and by an error return, then rebooted to
// readiness with the kit's invariants checked. The scenarios are ABO's R1
// starting states: a fresh install, and a HEAD (R0) store at LegacyCap
// (books' core-era steps pending, so the first-boot checkpoint and its
// state files) and at books v58 (legacy steps 59..LegacyCap first); and
// R1-25's swaps, a booted store with an archive restore staged by books'
// live restore stager (under this build's ID, so the boot swaps it in, and
// under another build's, so the boot rejects it) and one with `storage
// reset --kind store` staged (the boot sets the store aside and installs
// fresh).
//
// What books writes before readiness is its storage declaration's: the
// claims' bookkeeping, the legacy steps, the core-era steps, the version
// rows and the projection, all through core storage under the kit's labels
// (storage.step.batch and the rest). Books' Register commits no write
// (TestBooksConstructorWritesNothing), so the module booted here is books'
// manifest without its Register: the server it builds reads process-wide
// state (the global store, AppConfig) that the kit's concurrent boots would
// share. Books' start hooks (the settings load's writes, the boot repairs)
// run after Run's start step (1) under no writer-table label yet, and the
// R0-written fixture corpus is F2-12's; both join this matrix when core
// labels module start hooks and the corpus lands (todo.d
// 2026-11-08-crash-matrix-abo-start-hooks.md).
func TestBootCrashMatrix_ABO(t *testing.T) {
	if testing.Short() {
		t.Skip("the boot crash matrix: extraction-gates runs it")
	}
	for _, f := range aboCrashFixtures(t) {
		t.Run(f.Name, func(t *testing.T) {
			t.Parallel()
			crashmatrix.Run(t, crashmatrix.Scenario{Build: aboStorageBuild(f), Fixtures: []crashmatrix.Fixture{f}})
		})
	}
}

// TestBootCrashMatrix_ABOCoversEveryWriter: every writer-table label books'
// storage boot can reach is reached by a fixture's fault-free boot, so the
// matrix breaks it. The labels no ABO fixture reaches are listed with the
// reason.
func TestBootCrashMatrix_ABOCoversEveryWriter(t *testing.T) {
	if testing.Short() {
		t.Skip("the boot crash matrix: extraction-gates runs it")
	}
	observed := map[string]bool{}
	for _, f := range aboCrashFixtures(t) {
		for l := range crashmatrix.Observed(t, crashmatrix.Scenario{Build: aboStorageBuild(f), Fixtures: []crashmatrix.Fixture{f}}) {
			observed[l] = true
		}
	}
	missing, stale := crashmatrix.Unobserved(crashmatrix.Rows(), observed, aboUnreachedLabels())
	for _, l := range missing {
		t.Errorf("label %s: no ABO fixture's fault-free boot reaches it, so the matrix never breaks that writer", l)
	}
	for _, l := range stale {
		t.Errorf("allowlist entry %s: an ABO fixture reaches it, or no writer-table row lists it; drop it", l)
	}
}

// aboCrashFixtures are the matrix's starting states, each a state directory
// holding an encryption key and a store directory beside it, as an install
// has them: fresh; a HEAD store at LegacyCap and at books v58; the first of
// those with a record-less checkpoint directory (P4a's sweep), killed before
// its first step's batch (the next boot reuses the open checkpoint) and
// before its checkpoint's completion (the next boot completes the stranded
// attempt); a store a first boot already took through its checkpoint, beside
// three things the checkpoint sweeper's first sweep after readiness handles
// (an expired completed checkpoint, another install's open one, a
// record-less directory); and a HEAD store holding core keys without an
// install marker, which every boot refuses after P2 (the failed boot's
// storage close); and the swap fixtures (aboRestoreFixture,
// aboResetStoreFixture): a booted store with an archive restore staged
// under the kit's binary ID and under another build's, and one with a
// store reset staged.
func aboCrashFixtures(t *testing.T) []crashmatrix.Fixture {
	t.Helper()
	mk := func(name string, write func(dbPath string)) crashmatrix.Fixture {
		root := tempDir(t)
		f := crashmatrix.Fixture{Name: name, StateDir: filepath.Join(root, "state"), DBDir: filepath.Join(root, "db", "books.pebble")}
		require.NoError(t, os.MkdirAll(f.StateDir, 0o700))
		require.NoError(t, os.WriteFile(database.EncryptionKeyPath(f.StateDir), make([]byte, 32), 0o600))
		require.NoError(t, os.MkdirAll(filepath.Dir(f.DBDir), 0o700))
		if write != nil {
			write(f.DBDir)
		}
		return f
	}
	atCap := func(p string) { headStoreAt(t, p, nil) }
	garbage := mk("checkpoint-garbage", atCap)
	cp := filepath.Join(filepath.Dir(garbage.DBDir), ".mediacore-checkpoints", "20260101T000000Z-deadbeef", "db")
	require.NoError(t, os.MkdirAll(cp, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(cp, "OPTIONS-000001"), []byte("riddler"), 0o600))
	reused := mk("checkpoint-reused", atCap)
	reused.Mutate = crashmatrix.KillAt(t, reused, aboStorageBuild(reused), "storage.step.batch", 0)
	stranded := mk("attempt-stranded", atCap)
	stranded.Mutate = crashmatrix.KillAt(t, stranded, aboStorageBuild(stranded), "storage.checkpoint.complete", 0)
	foreign := mk("foreign-keys", func(p string) {
		headStoreAt(t, p, nil)
		db, err := pebble.Open(p, &pebble.Options{FormatMajorVersion: database.PebbleFormatMajorVersion})
		require.NoError(t, err)
		require.NoError(t, db.Set([]byte("c/rogue"), []byte("x"), pebble.Sync))
		require.NoError(t, db.Close())
	})
	foreign.Refusal = storage.ErrForeignKeys
	sweep := mk("checkpoints-to-sweep", atCap)
	require.NoError(t, crashmatrix.Boot(t, aboStorageBuild(sweep), sweep))
	addCheckpointsToSweep(t, sweep.DBDir)
	return []crashmatrix.Fixture{
		mk("fresh", nil),
		mk("head-at-legacycap", atCap),
		mk("head-at-v58", func(p string) { headStoreAtVersion(t, p, 58, nil) }),
		garbage, reused, stranded, foreign, sweep,
		aboRestoreFixture(t, mk, "restore-archive", crashmatrix.BuildID),
		aboRestoreFixture(t, mk, "restore-other-build", "an-older-abo-build"),
		aboResetStoreFixture(t, mk),
	}
}

// aboRestoreFixture is a booted ABO store with an archive restore staged by
// books' live restore stager (backup.StageRestore, as POST /backup/restore
// stages it; R1-25): the archive is HEAD's backup of another booted ABO
// store, and the intent is staged under buildID on the kit's clock, with
// no staged record (the stager writes none; P0a writes it first). Under the
// kit's binary ID the next boot swaps it in (TestInflightBeforeFirstRename's
// ABO case: the matrix kills between every rename of the swap); under
// another binary's it rejects it and the live store stays.
func aboRestoreFixture(t *testing.T, mk func(string, func(string)) crashmatrix.Fixture, name, buildID string) crashmatrix.Fixture {
	t.Helper()
	f := mk(name, nil)
	require.NoError(t, crashmatrix.Boot(t, aboStorageBuild(f), f))
	src := mk(name+"-source", nil)
	require.NoError(t, crashmatrix.Boot(t, aboStorageBuild(src), src))
	backups := filepath.Join(filepath.Dir(src.DBDir), "backups")
	info, err := backup.CreateBackup(src.DBDir, "pebble", backup.BackupConfig{BackupDir: backups, MaxBackups: 5})
	require.NoError(t, err)
	_, err = backup.StageRestore(context.Background(), backup.StageRestoreOptions{
		BackupDir: backups, Filename: info.Filename, Verify: true,
		StateDir: f.StateDir, DBPath: f.DBDir, BuildID: buildID, RequestedBy: "user:alfred",
		LegacySuffixes: legacySidecarSuffixes(), VerifyStore: database.VerifyStoreOpens, BeforeStage: database.DiscardHNSWCleanStop,
		Now: crashmatrix.Now,
	})
	require.NoError(t, err, "fixture %s: stage", name)
	return f
}

// aboResetStoreFixture is a booted ABO store with `storage reset --kind
// store` staged (KindReset, ResetStore: the store and every sidecar beside
// it set aside, no reset hook), as the kernel's offline stager stages it,
// under the kit's binary ID: the next boot sets them aside and installs
// fresh.
func aboResetStoreFixture(t *testing.T, mk func(string, func(string)) crashmatrix.Fixture) crashmatrix.Fixture {
	t.Helper()
	f := mk("reset-store", nil)
	require.NoError(t, crashmatrix.Boot(t, aboStorageBuild(f), f))
	const id = "20260314T150926Z-0a1b2c3d"
	in := restorejournal.Intent{ID: id, Kind: restorejournal.KindReset, Reset: restorejournal.ResetStore, DBPath: f.DBDir}
	for _, s := range append([]string{""}, append(restorejournal.CoreSidecarSuffixes(), legacySidecarSuffixes()...)...) {
		if p := f.DBDir + s; exists(p) {
			in.Targets = append(in.Targets, restorejournal.Target{From: p, To: restorejournal.SetAsideDest(f.StateDir, f.DBDir, id, p)})
		}
	}
	require.NoError(t, crashmatrix.Stage(f, in), "fixture reset-store: stage")
	return f
}

// addCheckpointsToSweep puts beside the store at dbPath, which a first boot
// already took through its checkpoint (so the next boot takes none and P4a
// runs no garbage sweep), three entries the checkpoint sweeper's first sweep
// after readiness handles (05-storage §5S.3.12 "Retention"; §5S.4 step 15):
// a completed checkpoint past its delete_after (deleted, record then copy),
// another install's open checkpoint (released: delete_after = now + 7 days)
// and a checkpoint directory without a record (garbage). Each copy is an
// empty store, so it opens and holds no module row.
func addCheckpointsToSweep(t *testing.T, dbPath string) {
	t.Helper()
	dir := filepath.Join(filepath.Dir(dbPath), ".mediacore-checkpoints")
	const (
		expired  = "20260301T000000Z-e9b1e001"
		orphaned = "20260302T000000Z-0b1e0002"
		garbage  = "20260303T000000Z-6a7b0003"
	)
	for _, n := range []string{expired, orphaned, garbage} {
		db, err := pebble.Open(filepath.Join(dir, n, "db"), &pebble.Options{FormatMajorVersion: database.PebbleFormatMajorVersion})
		require.NoError(t, err)
		require.NoError(t, db.Close())
	}
	day := 24 * time.Hour
	completed, due := crashmatrix.Now().Add(-14*day), crashmatrix.Now().Add(-7*day)
	records := map[string]string{
		expired: fmt.Sprintf(`{"install_id":"wayne-install","epoch":1,"taken_at":%q,"completed_at":%q,"delete_after":%q,"status":"completed"}`,
			completed.Format(time.RFC3339), completed.Format(time.RFC3339), due.Format(time.RFC3339)),
		orphaned: fmt.Sprintf(`{"install_id":"joker-install","epoch":1,"taken_at":%q,"status":"open"}`, completed.Format(time.RFC3339)),
	}
	for n, rec := range records {
		require.NoError(t, os.WriteFile(filepath.Join(dir, n+".json"), []byte(rec+"\n"), 0o600))
	}
}

// aboStorageBuild boots books' storage declaration over f through the kit's
// injector, with the state files serve names (the encryption key, the
// gap-fill file).
func aboStorageBuild(f crashmatrix.Fixture) func(testing.TB, *crashmatrix.Injector) error {
	return func(_ testing.TB, inj *crashmatrix.Injector) error {
		_, err := inj.New(context.Background(), app.Options{
			AppID:             AppID,
			Logger:            slog.New(slog.DiscardHandler),
			R1StoragePath:     f.DBDir,
			StorageStateFiles: StateFiles(f.StateDir, f.DBDir),
		}, booksStorage{})
		return err
	}
}

// booksStorage is books' module identity and storage declaration with no
// Register: what books' boot writes before readiness (see
// TestBootCrashMatrix_ABO).
type booksStorage struct{}

func (booksStorage) Manifest() module.Manifest {
	m := (&booksModule{}).Manifest()
	return module.Manifest{ID: m.ID, Version: m.Version, DisplayName: m.DisplayName, MinCoreVersion: m.MinCoreVersion,
		DefaultEnabled: true, Storage: database.BooksSpec(nil)}
}

func (booksStorage) Register(*module.Registrar) error { return nil }

// aboUnreachedLabels are the writer-table labels no ABO fixture reaches at
// R1, each with the reason. Core's own matrix (core/examples/hello) breaks
// every one of them over hello.
func aboUnreachedLabels() map[string]string {
	const (
		noLocation = "ABO keeps no backup.location.json at R1 (05-backup §5B.3.6 lands with F8), so the swap has none to copy or put back"
		swapBack   = "ABO's restore stager opens the extracted store with this build's guard before it stages (database.VerifyStoreOpens), " +
			"so no ABO-staged restore holds a store the next boot refuses after the swap; hello's restore-refused fixture breaks the swap-back"
		badSlot         = "an unusable pending.json (moved aside at P0a); no ABO stager writes one, every one links a complete intent (restorejournal.CreateIntent)"
		noDataMigration = "books declares no data migration until F3-06, and the boot call that runs one is F2-16's; " +
			"hello's data-migration cells break the writer through the kernel's entry point until then"
		noM8 = "books registers no store rewrite (M8) until F3-07, and the boot call that runs one is F2-16's; " +
			"hello's m8-rewrite cells break the writer through the kernel's entry point and restoreswap's P0a until then"
	)
	return map[string]string{
		"storage.levelstep.sidecars": "books declares no level step at R1 (Level 0; raising one is an R1-23 STOP condition)",
		"storage.levelstep.rollback": "books declares no level step at R1",
		"storage.dm.plan":            noDataMigration,
		"storage.dm.chunk":           noDataMigration,
		"storage.dm.state":           noDataMigration,
		"storage.rewrite.state":      noM8,
		"storage.rewrite.compact":    noM8,
		"storage.rewrite.copy":       noM8,
		"storage.rewrite.close":      noM8,
		"storage.rewrite.reopen":     noM8,
		"swap.rewrite.stage":         noM8,
		"swap.rewrite.delete":        noM8,
		"storage.checkpoint.ready":   "needs an upgrade whose step failed, leaving its checkpoint open, then a boot with nothing to run; no books step fails on a store HEAD wrote",
		// The kit's clock is fixed (crashmatrix.Now) and a fixture's files
		// are copied into memory with the host's current time as their
		// mtime, so no scratch entry is 7 days old at a kit boot. Storage's
		// TestDryRunScratchModeAndSweep deletes one on a fake clock.
		"storage.sweeper.scratch":   "a copied fixture file's mtime is the host's clock, never 7 days before the kit's fixed clock",
		"swap.location.copy":        noLocation,
		"swap.back.location":        noLocation,
		"swap.back.mark":            swapBack,
		"swap.back.mv":              swapBack,
		"swap.back.sidecars":        swapBack,
		"swap.journal.swapped_back": swapBack,
		"swap.slot.invalid":         badSlot,
		"swap.slot.cleanup":         badSlot,
		"swap.journal.failed": "a KindCheckpoint restore whose migrations fail (DATA7-12) needs a books step that fails on a store HEAD wrote; " +
			"no books step does (the same reason as storage.checkpoint.ready)",
		"swap.delete.remove": "a factory reset's delete entries lie off the store's filesystem (05-storage §5S.3.15) and run books' reset hook; " +
			"the matrix boots books' storage declaration without its hooks (booksStorage) on a one-filesystem host, and the hook's own tests " +
			"(reset_hook_test.go) cover its removals",
	}
}
