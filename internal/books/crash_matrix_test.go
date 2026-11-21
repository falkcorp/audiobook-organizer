// file: internal/books/crash_matrix_test.go
// version: 1.0.10
// guid: 1a6f3d82-9c4e-4b17-8e25-7d0b5c9a2f61
// last-edited: 2026-11-19

package books

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
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/backup"
	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/coretest/crashmatrix"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/codec"
	"github.com/falkcorp/mediacore/storage/keys"
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
		LegacySuffixes: legacySidecarSuffixes(), VerifyStore: database.VerifyStoreOpensWith(StandaloneOptions(database.StandaloneOptions{})), BeforeStage: database.DiscardHNSWCleanStop,
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
		swapBack   = "ABO's restore stager opens the extracted store with this build's guard before it stages (database.VerifyStoreOpensWith), " +
			"so no ABO-staged restore holds a store the next boot refuses after the swap; hello's restore-refused fixture breaks the swap-back"
		badSlot         = "an unusable pending.json (moved aside at P0a); no ABO stager writes one, every one links a complete intent (restorejournal.CreateIntent)"
		noDataMigration = "books declares no data migration until F3-06, and the boot call that runs one is F2-16's; " +
			"hello's data-migration cells break the writer through the kernel's entry point until then"
		noM8 = "books registers no store rewrite (M8) until F3-07, and the boot call that runs one is F2-16's; " +
			"hello's m8-rewrite cells break the writer through the kernel's entry point and restoreswap's P0a until then"
		// The config boot's two P0 writers no ABO fixture reaches: every
		// fixture has a state directory, and none holds an unparsable boot record.
		noStateDirFixture = "every ABO fixture has its state directory already, so P0 never creates one; hello's boot-record fixtures and missing-state-dir cell break it"
		noBadBootRecord   = "no ABO fixture holds an unparsable config boot record to set aside; hello's boot-record-unparsable fixture breaks the quarantine"
	)
	out := crashmatrix.JournalUnreachedLabels()
	// The identity boot's writers (F4-08 B3): ABO declares the flip at B10.
	maps.Copy(out, crashmatrix.IdentityUnreachedLabels())
	maps.Copy(out, map[string]string{
		"config.statedir.mkdir":        noStateDirFixture,
		"config.bootrecord.quarantine": noBadBootRecord,
		"storage.levelstep.sidecars":   "books declares no level step at R1 (Level 0; raising one is an R1-23 STOP condition)",
		"storage.levelstep.rollback":   "books declares no level step at R1",
		"storage.dm.plan":              noDataMigration,
		"storage.dm.chunk":             noDataMigration,
		"storage.dm.state":             noDataMigration,
		"storage.rewrite.state":        noM8,
		"storage.rewrite.compact":      noM8,
		"storage.rewrite.copy":         noM8,
		"storage.rewrite.close":        noM8,
		"storage.rewrite.reopen":       noM8,
		"storage.rewrite.checkpoint":   noM8,
		"swap.rewrite.stage":           noM8,
		"swap.rewrite.delete":          noM8,
		// F2-16 wires the definition snapshot writer, but books registers no
		// task kind until F5, so no ABO boot has a definition to upsert.
		// hello registers hello.count, and its matrix breaks the writer.
		"tasks.def.upsert":         "books registers no task kind until F5, so an ABO boot upserts no definition snapshot; hello's matrix breaks it over hello.count",
		"storage.checkpoint.ready": "needs an upgrade whose step failed, leaving its checkpoint open, then a boot with nothing to run; no books step fails on a store HEAD wrote",
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
	})
	return out
}

// F3-07 slice (e7): the flip boot's cells. The matrix above boots ABO with the
// config boot and the flip off (the gate-off allowlist entries stay until slice
// e8 deletes the gate), so it never breaks the writes the flip adds at boot:
//
//   - the keyring (secrets.keyring) and c/secrets/meta (secrets.meta), P4b's
//     secrets.Open;
//   - both halves of M7, core.level-1 and books.level-1: the stamp, the level
//     sidecar and books' legacy projection (.storage-format, the stamp and the
//     marker row), written before the stamp commit;
//   - M2, the config rename migration: the plan, the one chunk that moves the
//     four secret rows into c/secrets/, the blob's members into c/config/ and
//     rewrites the blob, and the done state;
//   - M8, the store rewrite and its KindRewrite swap.
//
// The kit's invariants (core/coretest/crashmatrix/invariants.go) digest only
// m/ rows and level stamps, so they cannot see a lost secret: a flip boot that
// deletes setting:openai_api_key without importing it passes I1 to I6. These
// cells carry their own checks, over the store's rows and files:
//
//   - aboFlipKillViolations at the state a kill leaves: for each of the four
//     rows exactly one of the legacy row and the sealed record exists, the
//     blob's concurrent_scans member and its config row likewise, the level
//     sidecars are never below a stamp, and books' stamp implies the legacy
//     projection HEAD refuses;
//   - aboFlipFinalViolations after the recovery boots: M2 fully applied (no
//     legacy row, every secret sealed, the config row staged, no
//     user row changed), both halves of M7 stamped with sidecars equal to the
//     stamps, the keyring whole with no temporary file beside it, meta naming
//     this app, no swap left in flight;
//   - aboFlipProbe inside every boot that reaches Register, which reads each
//     secret back through Deps.Config (the value for the three that are not
//     config keys, Meta.Set for core.http.basic_gate.password, which is one).
//
// The generic matrix (TestBootCrashMatrix_ABOFlip_Part1-3) runs the flip without M8:
// the kit's I2 holds a KindRewrite swap to the restore swap's rule that
// previous/ keeps the original store, and M8 deletes it by design (phase 4,
// 05-storage 5S.3.13), so the kit reports every fault-free M8 boot as a
// violation. M8's cells therefore break the full flip boot through KillAt and
// check it with the functions above (TestBootCrashMatrix_ABOFlipCells).

// aboFlipScans is the blob's concurrent_scans: not HEAD's default (10), so M2
// stages it (a value equal to its default is not stored).
const aboFlipScans = 3

// aboFlipConfigKey is the config key concurrent_scans moves to.
const aboFlipConfigKey = "modules.books.scan.concurrent_scans"

// aboFlipSecret is one of the four rows M2 drains: the HEAD row, the name it
// moves to, its value, and whether that name is a registered config key
// (core.http.basic_gate.password is: its value is not readable through
// Deps.Config, only its Meta).
type aboFlipSecret struct {
	row, name, value string
	registered       bool
}

func aboFlipSecrets() []aboFlipSecret {
	return []aboFlipSecret{
		{"openai_api_key", "integrations.ai.openai.api_key", "joker-openai", false},
		{"google_books_api_key", "integrations.metadata.google-books.api_key", "penguin-books", false},
		{"hardcover_api_token", "integrations.metadata.hardcover.api_token", "riddler-cover", false},
		{"basic_auth_password", "core.http.basic_gate.password", "alfred-gate", true},
	}
}

// aboFlipFixtureMu orders the fixture writers: database.InitEncryption sets a
// package-level key, which the encrypted rows' writer reads.
var aboFlipFixtureMu sync.Mutex

// aboFlipFixture is an R1 store ready for the flip: HEAD's store at LegacyCap
// with the four encrypted rows and a blob holding concurrent_scans, taken
// through R1's first boot (the gate-off storage boot) so the flip boot's
// writes are the flip's own. The state directory holds the settings key M2
// reads.
func aboFlipFixture(t *testing.T, name string) crashmatrix.Fixture {
	t.Helper()
	root := tempDir(t)
	f := crashmatrix.Fixture{Name: name, StateDir: filepath.Join(root, "state"), DBDir: filepath.Join(root, "db", "books.pebble")}
	require.NoError(t, os.MkdirAll(f.StateDir, 0o700))
	require.NoError(t, os.WriteFile(database.EncryptionKeyPath(f.StateDir), make([]byte, 32), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Dir(f.DBDir), 0o700))
	aboFlipFixtureMu.Lock()
	require.NoError(t, database.InitEncryption(f.StateDir))
	headStoreAt(t, f.DBDir, func(s *database.PebbleStore) {
		blob := fmt.Sprintf(`{"concurrent_scans":%d,"embedding_enabled":true,"embedding_model":"batcomputer"}`, aboFlipScans)
		require.NoError(t, s.SetSetting("config_blob", blob, "json", false))
		for _, sec := range aboFlipSecrets() {
			require.NoError(t, s.SetSetting(sec.row, sec.value, "string", true))
		}
	})
	aboFlipFixtureMu.Unlock()
	require.NoError(t, crashmatrix.Boot(t, aboStorageBuild(f), f), "fixture %s: R1's first boot", name)
	stripKeyring(t, f)
	return f
}

// stripKeyring turns the store of an install's first boot into the store the
// deployed R1 left: this build's every boot creates the keyring and
// c/secrets/meta, and R1 created neither, so the flip boot's own writes (the
// keyring's five kill points and the meta batch) would otherwise be behind it.
// The keyring file and every c/secrets/ row go. The rows are deleted under the
// engine's own prefix with a plain Pebble open, as the foreign-keys fixture
// plants its row, so nothing else of the store changes.
func stripKeyring(t *testing.T, f crashmatrix.Fixture) {
	t.Helper()
	require.NoError(t, os.Remove(filepath.Join(f.StateDir, "secrets.keyring")), "fixture %s: the first boot left no keyring", f.Name)
	db, err := pebble.Open(f.DBDir, &pebble.Options{FormatMajorVersion: database.PebbleFormatMajorVersion})
	require.NoError(t, err)
	require.NoError(t, db.DeleteRange([]byte("c/secrets/"), []byte("c/secrets0"), pebble.Sync))
	require.NoError(t, db.Close())
}

// aboFlipModule is books' manifest with F3's cutover declared (flipOnAt) and
// no Register of its own: what books' flip boot writes before readiness, as
// booksStorage is for the gate-off boot. Settings and AppConfig stay, so M2
// stages into registered keys and the probe can read the secrets. noRewrite
// leaves M8 out (see the comment above); probe, when set, runs at Register.
type aboFlipModule struct {
	m         *booksModule
	noRewrite bool
	probe     func(context.Context, module.Deps) error
}

func (a aboFlipModule) Manifest() module.Manifest {
	b := a.m.Manifest()
	man := module.Manifest{ID: b.ID, Version: b.Version, DisplayName: b.DisplayName, MinCoreVersion: b.MinCoreVersion,
		DefaultEnabled: true, Settings: b.Settings, Cutover: b.Cutover,
		// database.BooksSpec(nil) carries no observer: books' own beforeMigration
		// discards the HNSW marker on the real disk, which the kit forbids.
		Storage:   cutover.NewFlipShape(flipShapeOptions()).Spec(database.BooksSpec(nil)),
		AppConfig: &module.AppConfigSpec{Secrets: func(secrets.Name) bool { return true }}}
	if !a.noRewrite {
		man.Rewrite = b.Rewrite
	}
	return man
}

func (a aboFlipModule) Register(r *module.Registrar) error {
	if a.probe == nil {
		return nil
	}
	return a.probe(context.Background(), r.Deps())
}

// aboFlipBuild boots mod over f with the flip's config lists, through the
// kit's injector.
func aboFlipBuild(f crashmatrix.Fixture, mod aboFlipModule) func(testing.TB, *crashmatrix.Injector) error {
	retired, bindings, passthrough := flipConfigNames()
	return func(_ testing.TB, inj *crashmatrix.Injector) error {
		_, err := inj.New(context.Background(), app.Options{
			AppID: AppID, Logger: slog.New(slog.DiscardHandler), StorageStateFiles: StateFiles(f.StateDir, f.DBDir),
			RetiredConfigNames: retired, ConfigEnvBindings: bindings, ConfigPassthrough: passthrough,
		}, mod)
		return err
	}
}

// aboFlipProbe is the check inside a boot that reached Register, which is after
// P4b: M2, M7 and M8 have all run, so every secret must be readable and no
// legacy row left. n counts the boots it checked (a matrix whose probe never
// ran proves nothing).
func aboFlipProbe(n *atomic.Int64) func(context.Context, module.Deps) error {
	return func(ctx context.Context, d module.Deps) error {
		n.Add(1)
		if d.Config == nil {
			return errors.New("aboFlipProbe: Deps.Config is nil")
		}
		for _, s := range aboFlipSecrets() {
			if s.registered {
				ks, ok := d.Config.Lookup(config.Path(s.name))
				if !ok || ks.Secret == nil || !ks.Secret.Set || ks.Secret.Err != nil {
					return fmt.Errorf("aboFlipProbe: %s is not a readable set secret after the flip boot: %+v", s.name, ks.Secret)
				}
			} else {
				v, m, err := d.Config.Secret(ctx, secrets.Name(s.name))
				if err != nil || !m.Set || v.Reveal() != s.value {
					return fmt.Errorf("aboFlipProbe: secret %s does not read back after the flip boot (set=%t, err=%v)", s.name, m.Set, err)
				}
			}
			if _, err := d.Storage.Legacy.Get(ctx, []byte("setting:"+s.row)); !errors.Is(err, storage.ErrNotFound) {
				return fmt.Errorf("aboFlipProbe: the legacy row setting:%s is still readable after M2 (%v)", s.row, err)
			}
		}
		ks, ok := d.Config.Lookup(config.Path(aboFlipConfigKey))
		if !ok || string(ks.Effective) != strconv.Itoa(aboFlipScans) {
			return fmt.Errorf("aboFlipProbe: %s = %s after M2, want %d", aboFlipConfigKey, ks.Effective, aboFlipScans)
		}
		return nil
	}
}

// aboFlipState is the store's rows and sidecars as a crash left them.
type aboFlipState struct {
	kv       map[string]string
	levels   map[string]int // .storage-levels; nil when absent
	format   int            // .storage-format; 0 when absent
	stamps   map[string]int // c/storage/level/<owner>
	inflight bool           // a swap is in flight (restore/<id>/inflight.json)
}

// readAboFlipState reads f's store through fsys (a crash clone, or the real
// disk with vfs.Default): nil when no store is at the database path, which a
// kill between the set-aside and the placement of a swap leaves.
func readAboFlipState(fsys vfs.FS, f crashmatrix.Fixture) (*aboFlipState, error) {
	if _, err := fsys.Stat(f.DBDir); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	db, err := pebble.Open(f.DBDir, &pebble.Options{FS: fsys, ReadOnly: true, ErrorIfNotExists: true})
	if errors.Is(err, pebble.ErrDBDoesNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open the store read-only: %w", err)
	}
	st := &aboFlipState{kv: map[string]string{}, stamps: map[string]int{}}
	it, err := db.NewIter(nil)
	if err != nil {
		return nil, errors.Join(err, db.Close())
	}
	stampPrefix := append([]byte("c/storage/"), keys.Join(keys.Str("level"))...)
	for ok := it.First(); ok; ok = it.Next() {
		k, v := string(it.Key()), string(it.Value())
		st.kv[k] = v
		if strings.HasPrefix(k, string(stampPrefix)) {
			segs, serr := keys.Split([]byte(k)[len("c/storage/"):])
			rec, derr := codec.JSON[struct {
				Level int `json:"level"`
			}](1).Decode([]byte(v))
			if serr != nil || len(segs) != 2 || derr != nil {
				return nil, errors.Join(fmt.Errorf("stamp key %q unreadable (%v, %v)", k, serr, derr), it.Close(), db.Close())
			}
			st.stamps[string(segs[1])] = rec.Level
		}
	}
	if err := errors.Join(it.Error(), it.Close(), db.Close()); err != nil {
		return nil, err
	}
	if b, ok, err := readAboFile(fsys, f.DBDir+".storage-levels"); err != nil {
		return nil, err
	} else if ok {
		st.levels = map[string]int{}
		for line := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
			owner, n, found := strings.Cut(strings.TrimSpace(line), " ")
			lvl, cerr := strconv.Atoi(strings.TrimSpace(n))
			if !found || cerr != nil {
				return nil, fmt.Errorf(".storage-levels line %q unreadable", line)
			}
			st.levels[owner] = lvl
		}
	}
	if b, ok, err := readAboFile(fsys, f.DBDir+database.StorageFormatSidecarSuffix); err != nil {
		return nil, err
	} else if ok {
		if st.format, err = strconv.Atoi(strings.TrimSpace(string(b))); err != nil {
			return nil, fmt.Errorf(".storage-format unreadable: %w", err)
		}
	}
	ids, err := restorejournal.InFlightIDs(fsys, f.StateDir)
	st.inflight = len(ids) > 0
	return st, err
}

// readAboFile reads path through fsys; ok is false when it does not exist.
func readAboFile(fsys vfs.FS, path string) (b []byte, ok bool, err error) {
	h, err := fsys.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	b, err = io.ReadAll(h)
	return b, true, errors.Join(err, h.Close())
}

// aboFlipSecretKey is the c/secrets/ record of name.
func aboFlipSecretKey(name string) string {
	return string(append([]byte("c/secrets/"), keys.Join(keys.Str("v"), keys.Str(name))...))
}

// aboFlipMetaKey is c/secrets/meta's key: the owner prefix and one tuple
// segment, which keys.Join terminates (a literal "c/secrets/meta" is no key).
func aboFlipMetaKey() string {
	return string(append([]byte("c/secrets/"), keys.Join(keys.Str("meta"))...))
}

// aboFlipConfigRowKey is the c/config/ override row of the key M2 stages.
func aboFlipConfigRowKey() string {
	return string(append([]byte("c/config/"), keys.Join(keys.Str("v"), keys.Str(aboFlipConfigKey))...))
}

// blobHasScans reports whether the settings blob still holds concurrent_scans.
func (st *aboFlipState) blobHasScans() (bool, error) {
	raw, ok := st.kv["setting:config_blob"]
	if !ok {
		return false, errors.New("the settings blob row is gone")
	}
	var row struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal([]byte(raw), &row); err != nil {
		return false, fmt.Errorf("the settings blob row: %w", err)
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal([]byte(row.Value), &members); err != nil {
		return false, fmt.Errorf("the settings blob: %w", err)
	}
	_, has := members["concurrent_scans"]
	return has, nil
}

// aboFlipVolatile are the legacy rows the flip legitimately changes: the
// settings rows (M2), books' storage-format stamp and migration marker (M7's
// projection) and the preference counter the projection allocates from.
var aboFlipVolatile = []string{"setting:", "preference:storage_", "preference:db_version", "counter:preference"}

// aboFlipKillViolations is what must hold of the store a kill leaves, before
// the recovery boots. base is the store before the flip boot; label is the
// writer the kill came before, "" for a state that is not a kill's.
func aboFlipKillViolations(st, base *aboFlipState, label string) []string {
	var out []string
	for _, s := range aboFlipSecrets() {
		_, legacy := st.kv["setting:"+s.row]
		_, sealed := st.kv[aboFlipSecretKey(s.name)]
		switch {
		case !legacy && !sealed:
			out = append(out, fmt.Sprintf("%s is lost: neither setting:%s nor %s exists", s.name, s.row, aboFlipSecretKey(s.name)))
		case legacy && sealed:
			out = append(out, fmt.Sprintf("%s is half drained: setting:%s and its sealed record both exist (M2's chunk is one batch)", s.name, s.row))
		}
	}
	_, staged := st.kv[aboFlipConfigRowKey()]
	if has, err := st.blobHasScans(); err != nil {
		out = append(out, err.Error())
	} else if !has && !staged {
		out = append(out, "concurrent_scans is lost: neither the blob nor "+aboFlipConfigKey+" holds it")
	} else if has && staged {
		out = append(out, "concurrent_scans is half moved: the blob and "+aboFlipConfigKey+" both hold it")
	}
	if len(st.stamps) > 0 && st.levels == nil && !st.inflight {
		out = append(out, fmt.Sprintf("stamps %v with no .storage-levels and no swap in flight", st.stamps))
	}
	for o, l := range st.stamps {
		if st.levels != nil && st.levels[o] < l {
			out = append(out, fmt.Sprintf(".storage-levels has %s at %d, below its stamp %d (the sidecar is written first)", o, st.levels[o], l))
		}
	}
	if books := st.stamps["books"]; books > 0 {
		out = append(out, aboFlipProjectionViolations(st, label)...)
	}
	out = append(out, aboFlipRowsUnchanged(st, base)...)
	return out
}

// aboFlipProjectionViolations: with books stamped, HEAD must refuse the store:
// the sidecar and the stamp row project LegacyMaxFormat+1 (books' half of M7).
// The storage_migration marker is set in the same batch as the stamp and
// cleared by the migration list's finish batch (storage step 9), so a kill
// point may see it either way; aboFlipFinalViolations requires it gone. The
// store rewrite (M8) is a step of that list, so a kill before one of its
// writers comes before the finish batch, and the marker must still be set:
// that is the window a build older than the guard would otherwise open the
// half-migrated store in.
func aboFlipProjectionViolations(st *aboFlipState, label string) []string {
	var out []string
	const want = 2 // store's legacyMaxFormat (1) + 1, which every pre-guard build refuses
	if st.format != want && !st.inflight {
		out = append(out, fmt.Sprintf(".storage-format = %d with books stamped, want %d", st.format, want))
	}
	if strings.HasPrefix(label, "swap.rewrite.") || strings.HasPrefix(label, "storage.rewrite.") {
		if _, ok := st.kv["preference:storage_migration"]; !ok {
			out = append(out, "the storage_migration marker is missing with books stamped and the store rewrite still to finish (the finish batch clears it)")
		}
	}
	raw, ok := st.kv["preference:storage_format"]
	var pref database.UserPreference
	var dv database.DatabaseVersion
	if !ok || json.Unmarshal([]byte(raw), &pref) != nil || pref.Value == nil || json.Unmarshal([]byte(*pref.Value), &dv) != nil || dv.Version != want {
		out = append(out, fmt.Sprintf("preference:storage_format is %q with books stamped, want version %d", raw, want))
	}
	return out
}

// aboFlipRowsUnchanged: every legacy row outside aboFlipVolatile is
// byte-identical to the store before the flip boot (M2, M7 and M8 move or copy
// nothing else).
func aboFlipRowsUnchanged(st, base *aboFlipState) []string {
	var out []string
	for k, v := range base.kv {
		if strings.HasPrefix(k, "c/") || strings.HasPrefix(k, "m/") || slices.ContainsFunc(aboFlipVolatile, func(p string) bool { return strings.HasPrefix(k, p) }) {
			continue
		}
		if got, ok := st.kv[k]; !ok || got != v {
			out = append(out, fmt.Sprintf("the user row %q was lost or changed (present=%t)", k, ok))
		}
	}
	return out
}

// aboFlipFinalViolations is what must hold after the recovery boots reached
// readiness: the whole flip applied.
func aboFlipFinalViolations(fsys vfs.FS, f crashmatrix.Fixture, st, base *aboFlipState) []string {
	out := aboFlipKillViolations(st, base, "")
	for _, s := range aboFlipSecrets() {
		if _, ok := st.kv["setting:"+s.row]; ok {
			out = append(out, "setting:"+s.row+" is still there after M2")
		}
		if _, ok := st.kv[aboFlipSecretKey(s.name)]; !ok {
			out = append(out, s.name+" was not sealed")
		}
	}
	if _, ok := st.kv[aboFlipConfigRowKey()]; !ok {
		out = append(out, aboFlipConfigKey+" was not staged")
	}
	for _, owner := range []string{"core", "books"} {
		if st.stamps[owner] != cutover.BooksCutoverLevel || st.levels[owner] != st.stamps[owner] {
			out = append(out, fmt.Sprintf("%s: stamp %d, .storage-levels %d, want both %d", owner, st.stamps[owner], st.levels[owner], cutover.BooksCutoverLevel))
		}
	}
	if _, ok := st.kv["preference:storage_migration"]; ok {
		out = append(out, "the storage_migration marker is still set after the migration list finished")
	}
	raw, ok := st.kv[aboFlipMetaKey()]
	var meta struct {
		AppID    string `json:"app_id"`
		MaxKeyID int    `json:"max_key_id"`
	}
	if !ok || json.Unmarshal([]byte(raw), &meta) != nil || meta.AppID != AppID || meta.MaxKeyID < 1 {
		out = append(out, fmt.Sprintf("c/secrets/meta = %q, want this app's with max_key_id >= 1", raw))
	}
	if b, ok, err := readAboFile(fsys, filepath.Join(f.StateDir, "secrets.keyring")); err != nil || !ok || len(b) == 0 {
		out = append(out, fmt.Sprintf("the keyring is not whole (present=%t, %d bytes, err=%v)", ok, len(b), err))
	}
	if names, err := fsys.List(f.StateDir); err != nil {
		out = append(out, err.Error())
	} else {
		for _, n := range names {
			if strings.HasPrefix(n, "secrets.keyring.tmp-") {
				out = append(out, "a stale keyring temporary file is left: "+n)
			}
		}
	}
	if st.inflight {
		out = append(out, "a swap is still in flight after recovery")
	}
	if _, err := fsys.Stat(restorejournal.PendingPath(f.StateDir)); err == nil {
		out = append(out, "pending.json is still staged after recovery")
	}
	return out
}

// TestBootCrashMatrix_ABOFlip_Part1, _Part2 and _Part3 are the generic matrix
// over books' flip boot: M2, both halves of M7, M8, the keyring and
// c/secrets/meta, each broken by a kill and by an error return, the boots
// after it checked against the kit's invariants. The probe runs in every boot
// that reaches Register, so every recovery boot also reads the four secrets
// back and finds no legacy row.
//
// The matrix runs as three top-level tests, each the census plus a third of
// its first-level jobs (crashmatrix Scenario.Part), because whole it was one
// indivisible test of about 900 s under -race on a GitHub runner: the fixture
// slicer deals tests out by name, so no split of the package could put it
// under the 600 s a slice is sized for, against the 1,500 s per-process
// timeout. The three names hash to three different slices of the package's
// four (scripts/ci/fixture_test_packages.py part_of), none of them the
// Tasklint/basic-gate slice; re-check that if the package's part count changes.
func TestBootCrashMatrix_ABOFlip_Part1(t *testing.T) { aboFlipMatrixPart(t, 0) }

func TestBootCrashMatrix_ABOFlip_Part2(t *testing.T) { aboFlipMatrixPart(t, 1) }

func TestBootCrashMatrix_ABOFlip_Part3(t *testing.T) { aboFlipMatrixPart(t, 2) }

// aboFlipMatrixParts is how many tests the generic flip matrix runs as.
const aboFlipMatrixParts = 3

func aboFlipMatrixPart(t *testing.T, part int) {
	if testing.Short() {
		t.Skip("the boot crash matrix: extraction-gates runs it")
	}
	f := aboFlipFixture(t, "flip")
	var n atomic.Int64
	crashmatrix.Run(t, crashmatrix.Scenario{
		Build:    aboFlipBuild(f, aboFlipModule{m: flipOnAt(f.StateDir, f.DBDir), probe: aboFlipProbe(&n)}),
		Fixtures: []crashmatrix.Fixture{f},
		Part:     part, Parts: aboFlipMatrixParts,
	})
	require.Positive(t, n.Load(), "the probe never ran: no boot of the matrix reached Register")
}

// aboFlipCell is one write of the flip boot, broken by a kill before its nth
// kill point of label. at checks the state the kill left is the one the cell
// names (a cell whose kill landed elsewhere proves nothing, and a label whose
// kill points shift under a refactor would otherwise pass vacuously); a nil
// at is for M8's swap, whose kill states may have no store at the database
// path.
type aboFlipCell struct {
	name, label string
	nth         int
	at          func(st *aboFlipState, keyring bool) error
}

// keyed: the cell's starting state already holds the keyring
// (aboFlipKeyedFixture). Only the load-sync cell needs that: a boot that finds
// a keyring is the only one that syncs its directory before trusting it, and
// the keyed state is the one a killed keyring write leaves a temporary file in.
func (c aboFlipCell) keyed() bool {
	return c.label == "secrets.keyring.load-sync" || c.label == "secrets.keyring.stale"
}

func aboFlipCells() []aboFlipCell {
	before := func(what string, ok func(st *aboFlipState, keyring bool) bool) func(*aboFlipState, bool) error {
		return func(st *aboFlipState, keyring bool) error {
			if !ok(st, keyring) {
				return fmt.Errorf("the kill left a state that is not %s: stamps %v, .storage-levels %v, .storage-format %d, keyring file %t", what, st.stamps, st.levels, st.format, keyring)
			}
			return nil
		}
	}
	legacyRows := func(st *aboFlipState) bool {
		for _, s := range aboFlipSecrets() {
			if _, ok := st.kv["setting:"+s.row]; !ok {
				return false
			}
		}
		return true
	}
	hasMeta := func(st *aboFlipState) bool { _, ok := st.kv[aboFlipMetaKey()]; return ok }
	var cells []aboFlipCell
	// The keyring's five kill points (the temporary file's create, its write
	// and sync, the rename into place, the directory sync) and c/secrets/meta's
	// batch: P4b, before any step runs.
	for n := range 5 {
		cells = append(cells, aboFlipCell{fmt.Sprintf("keyring-%d", n), "secrets.keyring", n,
			before("before M2 with no meta", func(st *aboFlipState, _ bool) bool { return !hasMeta(st) && legacyRows(st) && st.stamps["core"] == 0 })})
	}
	// A boot over a keyring that already exists syncs its directory first
	// (secrets.keyring.load-sync, the F3-07 e7 data-loss fix): killed there,
	// nothing was sealed or stamped yet.
	cells = append(cells, aboFlipCell{"keyring-load-sync", "secrets.keyring.load-sync", 0,
		before("before M2 with the keyring in place", func(st *aboFlipState, keyring bool) bool {
			return keyring && legacyRows(st) && st.stamps["core"] == 0
		})})
	cells = append(cells, aboFlipCell{"keyring-stale-temp", "secrets.keyring.stale", 0,
		before("before M2 with the keyring in place and a stale temporary file to remove", func(st *aboFlipState, keyring bool) bool {
			return keyring && legacyRows(st) && st.stamps["core"] == 0
		})})
	cells = append(cells, aboFlipCell{"meta", "secrets.meta", 0,
		before("the keyring whole, meta unwritten, M2 not run", func(st *aboFlipState, keyring bool) bool {
			return keyring && !hasMeta(st) && legacyRows(st) && st.stamps["core"] == 0
		})})
	// M7's two halves: each is its sidecar file writes (.storage-levels, then
	// .storage-format: four kill points each), then the stamp batch.
	for _, n := range []int{0, 3, 4, 7} {
		cells = append(cells, aboFlipCell{fmt.Sprintf("m7-core-sidecar-%d", n), "storage.levelstep.sidecars", n,
			before("core's level step before its stamp", func(st *aboFlipState, _ bool) bool { return st.stamps["core"] == 0 && st.stamps["books"] == 0 })})
	}
	cells = append(cells, aboFlipCell{"m7-core-stamp", "storage.step.batch", 0,
		before("core's sidecar written, its stamp not", func(st *aboFlipState, _ bool) bool {
			return st.stamps["core"] == 0 && st.levels["core"] == 1
		})})
	for _, n := range []int{8, 11, 12, 15} {
		cells = append(cells, aboFlipCell{fmt.Sprintf("m7-books-sidecar-%d", n), "storage.levelstep.sidecars", n,
			before("books' level step before its stamp, core stamped", func(st *aboFlipState, _ bool) bool {
				return st.stamps["core"] == 1 && st.stamps["books"] == 0
			})})
	}
	cells = append(cells, aboFlipCell{"m7-books-stamp", "storage.step.batch", 1,
		before("books' sidecars written (projecting a format HEAD refuses), its stamp not", func(st *aboFlipState, _ bool) bool {
			return st.stamps["books"] == 0 && st.levels["books"] == 1 && st.format == 2
		})})
	// M2: its plan, its one chunk (the four rows drained, the blob rewritten,
	// one batch) and its state records.
	cells = append(cells,
		aboFlipCell{"m2-plan", "storage.dm.plan", 0,
			before("M2 not yet run", func(st *aboFlipState, _ bool) bool { return legacyRows(st) && st.stamps["books"] == 1 })},
		aboFlipCell{"m2-chunk", "storage.dm.chunk", 0,
			before("M2's chunk not yet applied", func(st *aboFlipState, _ bool) bool { return legacyRows(st) && st.stamps["books"] == 1 })},
		aboFlipCell{"m2-state-0", "storage.dm.state", 0,
			before("M2's chunk applied, its record not", func(st *aboFlipState, _ bool) bool { return !legacyRows(st) })},
		aboFlipCell{"m2-state-1", "storage.dm.state", 1,
			before("M2's chunk applied, its record not", func(st *aboFlipState, _ bool) bool { return !legacyRows(st) })},
	)
	// M8: the rewrite's record, compaction, copy and swap, and phase 4's
	// deletion of the set-aside. After M2, so the secrets are already sealed.
	for _, c := range []struct {
		label string
		nth   int
	}{
		{"storage.rewrite.state", 0}, {"storage.rewrite.compact", 0}, {"storage.rewrite.copy", 0}, {"storage.rewrite.copy", 4},
		{"storage.rewrite.reopen", 0}, {"storage.rewrite.checkpoint", 0},
		{"swap.rewrite.stage", 0}, {"swap.rewrite.stage", 8}, {"swap.rewrite.delete", 0}, {"swap.rewrite.delete", 7},
	} {
		cells = append(cells, aboFlipCell{fmt.Sprintf("m8-%s-%d", strings.TrimPrefix(strings.TrimPrefix(c.label, "storage."), "swap."), c.nth), c.label, c.nth, nil})
	}
	return cells
}

// restoreAboFlip replaces f's real directories with the crash clone's, the
// state a killed boot left, so crashmatrix.Boot (which boots the real
// directories) is the recovery boot.
func restoreAboFlip(t *testing.T, clone *vfs.MemFS, f crashmatrix.Fixture) {
	t.Helper()
	for _, root := range []string{f.StateDir, filepath.Dir(f.DBDir)} {
		require.NoError(t, os.RemoveAll(root))
		if _, err := clone.Stat(root); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		_, err := vfs.Clone(clone, vfs.Default, root, root)
		require.NoError(t, err, "restore %s from the crash clone", root)
	}
}

// aboFlipKeyedFixture is aboFlipFixture after a flip boot killed once the
// keyring is durable and c/secrets/meta is not yet written: the starting state
// of every later boot, which finds a keyring already in place (the flip boot
// on ABO's fixture is the first to create one, so no fault-free flip boot over
// aboFlipFixture reaches secrets.keyring.load-sync).
func aboFlipKeyedFixture(t *testing.T, name string) crashmatrix.Fixture {
	t.Helper()
	f := aboFlipFixture(t, name)
	full := aboFlipModule{m: flipOnAt(f.StateDir, f.DBDir)}
	clone, err := crashmatrix.KillAt(t, f, aboFlipBuild(f, full), "secrets.meta", 0)(nil)
	require.NoError(t, err)
	restoreAboFlip(t, clone, f)
	return f
}

// TestBootCrashMatrix_ABOFlipCells breaks the flip boot at each write it adds
// (aboFlipCells: the keyring, c/secrets/meta, both halves of M7, M2 and M8),
// checks the state the kill left (no secret lost or half drained, the legacy
// guard held, the sidecars never below a stamp), then reboots the killed
// directories and checks the recovery: M2 applied whole, both M7 halves
// stamped with equal sidecars, every secret readable through Deps.Config in
// the boot itself, the keyring whole with no stale temporary file, meta naming
// this app, no swap in flight.
func TestBootCrashMatrix_ABOFlipCells(t *testing.T) {
	if testing.Short() {
		t.Skip("the boot crash matrix: extraction-gates runs it")
	}
	for _, c := range aboFlipCells() {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := aboFlipFixture(t, c.name)
			if c.keyed() {
				f = aboFlipKeyedFixture(t, c.name)
			}
			base, err := readAboFlipState(vfs.Default, f)
			require.NoError(t, err)
			require.NotNil(t, base)
			full := aboFlipModule{m: flipOnAt(f.StateDir, f.DBDir)}
			clone, err := crashmatrix.KillAt(t, f, aboFlipBuild(f, full), c.label, c.nth)(nil)
			require.NoError(t, err)

			killed, err := readAboFlipState(clone, f)
			require.NoError(t, err, "read the store the kill left")
			_, kerr := clone.Stat(filepath.Join(f.StateDir, "secrets.keyring"))
			switch {
			case killed != nil:
				require.Empty(t, aboFlipKillViolations(killed, base, c.label), "the state a kill before %s #%d left", c.label, c.nth)
				if c.at != nil {
					require.NoError(t, c.at(killed, kerr == nil))
				}
			case c.at != nil:
				t.Fatalf("no store at the database path, in a cell that names its state")
			}

			restoreAboFlip(t, clone, f)
			var n atomic.Int64
			rec := aboFlipModule{m: flipOnAt(f.StateDir, f.DBDir), probe: aboFlipProbe(&n)}
			var errs []error
			for boot := 0; boot < 3 && n.Load() == 0; boot++ {
				errs = append(errs, crashmatrix.Boot(t, aboFlipBuild(f, rec), f))
			}
			require.NotZero(t, n.Load(), "no recovery boot reached readiness: %v", errs)
			final, err := readAboFlipState(vfs.Default, f)
			require.NoError(t, err)
			require.NotNil(t, final)
			require.Empty(t, aboFlipFinalViolations(vfs.Default, f, final, base), "after the recovery boot")
		})
	}
}

// aboFlipNoKillPoint are labels the flip boot reaches whose operations are no
// kill point (a close's own syncs), so KillAt has nothing to break: the labels
// either side of them (storage.rewrite.compact before, .reopen after) have
// cells, and a kill between those is a kill before the next one.
func aboFlipNoKillPoint() map[string]string {
	return map[string]string{
		"storage.rewrite.close": "the engine close carries only non-kill operations; the kill points around it are rewrite.copy and rewrite.reopen",
	}
}

// aboFlipWriterLabels are the crash-point labels of every write F3-07's flip
// boot adds over the R1 boot: the keyring (its creation and the directory sync
// that now precedes trusting a present one), c/secrets/meta, both halves of M7
// (the level step's sidecars and its stamp batch), M2's plan, chunk and state
// records, and M8's rewrite and its in-process swap. Each needs a cell.
func aboFlipWriterLabels() []string {
	return []string{
		"secrets.keyring", "secrets.keyring.stale", "secrets.keyring.load-sync", "secrets.meta",
		"storage.levelstep.sidecars", "storage.step.batch",
		"storage.dm.plan", "storage.dm.chunk", "storage.dm.state",
		"storage.rewrite.state", "storage.rewrite.compact", "storage.rewrite.copy",
		"storage.rewrite.reopen", "storage.rewrite.checkpoint",
		"swap.rewrite.stage", "swap.rewrite.delete",
	}
}

// TestBootCrashMatrix_ABOFlipCoversEveryWriter: each write the flip boot adds
// is reached by the fault-free flip boot and has a cell, and no label of the
// families the flip adds (secrets, the data-migration records, the rewrite,
// the level step) is reached without one.
func TestBootCrashMatrix_ABOFlipCoversEveryWriter(t *testing.T) {
	if testing.Short() {
		t.Skip("the boot crash matrix: extraction-gates runs it")
	}
	observed := map[string]bool{}
	for _, f := range []crashmatrix.Fixture{aboFlipFixture(t, "flip-coverage"), aboFlipKeyedFixture(t, "flip-coverage-keyed")} {
		var n atomic.Int64
		for l := range crashmatrix.Observed(t, crashmatrix.Scenario{
			Build:    aboFlipBuild(f, aboFlipModule{m: flipOnAt(f.StateDir, f.DBDir), probe: aboFlipProbe(&n)}),
			Fixtures: []crashmatrix.Fixture{f},
		}) {
			observed[l] = true
		}
	}
	covered := map[string]bool{}
	for _, c := range aboFlipCells() {
		covered[c.label] = true
	}
	for l := range aboFlipNoKillPoint() {
		if !observed[l] {
			t.Errorf("no-kill-point entry %s: the flip boot does not reach it; drop it", l)
		}
		if covered[l] {
			t.Errorf("no-kill-point entry %s: a cell breaks it; drop the entry", l)
		}
	}
	want := map[string]bool{}
	for _, l := range aboFlipWriterLabels() {
		want[l] = true
		if !observed[l] {
			t.Errorf("label %s: the fault-free flip boot never reaches it, so no cell can break it", l)
		}
		if !covered[l] {
			t.Errorf("label %s: the flip boot writes it and no cell breaks it", l)
		}
	}
	for l := range observed {
		for _, family := range []string{"secrets.", "storage.dm.", "storage.rewrite.", "swap.rewrite.", "storage.levelstep."} {
			if _, skipped := aboFlipNoKillPoint()[l]; skipped {
				continue
			}
			if strings.HasPrefix(l, family) && !want[l] {
				t.Errorf("label %s: the flip boot reaches it and aboFlipWriterLabels does not list it (add it, with a cell)", l)
			}
		}
	}
}
