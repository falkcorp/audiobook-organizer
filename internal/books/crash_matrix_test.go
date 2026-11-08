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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/coretest/crashmatrix"
	"github.com/falkcorp/mediacore/module"
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
// 2026-11-08_crash_matrix_abo_start_hooks.md).
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
	for _, want := range []string{
		"storage.fresh.batch", "storage.checkpoint.pebble", "storage.checkpoint.statefiles",
		"storage.checkpoint.record", "storage.step.batch", "storage.finish.batch", "storage.checkpoint.complete",
	} {
		if !observed[want] {
			t.Errorf("no ABO fixture's fault-free boot reaches %s, so the matrix never breaks it", want)
		}
	}
}

// aboCrashFixtures are the matrix's starting states, each a state directory
// holding an encryption key and a store directory beside it, as an install
// has them: fresh, a HEAD store at LegacyCap, a HEAD store at books v58.
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
	return []crashmatrix.Fixture{
		mk("fresh", nil),
		mk("head-at-legacycap", func(p string) { headStoreAt(t, p, nil) }),
		mk("head-at-v58", func(p string) { headStoreAtVersion(t, p, 58, nil) }),
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
