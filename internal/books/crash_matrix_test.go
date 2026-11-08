// file: internal/books/crash_matrix_test.go
// version: 1.0.0
// guid: 1a6f3d82-9c4e-4b17-8e25-7d0b5c9a2f61
// last-edited: 2026-11-08

package books

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/coretest/crashmatrix"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
)

// TestBootCrashMatrix_ABO is ABO's instance of the boot crash matrix
// (05-storage §5S.3.16, §5S.10; 06 §6.5.1; 09 §9.4 R1): R1-21's kit over
// books' storage boot, broken at every durable write, rename and fsync from
// P0a to readiness, by a kill and by an error return, then rebooted to
// readiness with the kit's invariants checked. The scenarios are ABO's R1
// starting states: a fresh install, and a HEAD (R0) store at LegacyCap
// (books' core-era steps pending, so the first-boot checkpoint and its
// state files) and at books v58 (legacy steps 59..LegacyCap first).
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
// attempt); and a HEAD store holding core keys without an install marker,
// which every boot refuses after P2 (the failed boot's storage close).
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
	return []crashmatrix.Fixture{
		mk("fresh", nil),
		mk("head-at-legacycap", atCap),
		mk("head-at-v58", func(p string) { headStoreAtVersion(t, p, 58, nil) }),
		garbage, reused, stranded, foreign,
	}
}

// aboStorageBuild boots books' storage declaration over f through the kit's
// injector, with the state files serve names (the encryption key, the
// gap-fill file).
func aboStorageBuild(f crashmatrix.Fixture) func(testing.TB, *crashmatrix.Injector) error {
	return func(_ testing.TB, inj *crashmatrix.Injector) error {
		_, err := inj.New(context.Background(), app.Options{
			AppID:             appID,
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
		DefaultEnabled: true, Storage: database.BooksSpec()}
}

func (booksStorage) Register(*module.Registrar) error { return nil }

// aboUnreachedLabels are the writer-table labels no ABO fixture reaches at
// R1, each with the reason. Core's own matrix (core/examples/hello) breaks
// every one of them over hello.
func aboUnreachedLabels() map[string]string {
	const noSwap = "ABO stages no restore or reset at R1 (R1-25 moves books' resets onto the KindReset swap and adds its fixtures)"
	out := map[string]string{
		"storage.levelstep.sidecars": "books declares no level step at R1 (Level 0; raising one is an R1-23 STOP condition)",
		"storage.levelstep.rollback": "books declares no level step at R1",
		"storage.checkpoint.ready":   "needs an upgrade whose step failed, leaving its checkpoint open, then a boot with nothing to run; no books step fails on a store HEAD wrote",
	}
	for _, r := range crashmatrix.Rows() {
		for _, l := range r.Labels {
			if strings.HasPrefix(l, "swap.") {
				out[l] = noSwap
			}
		}
	}
	return out
}
