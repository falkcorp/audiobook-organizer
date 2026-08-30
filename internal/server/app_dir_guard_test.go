// file: internal/server/app_dir_guard_test.go
// version: 1.0.0
// guid: 1f6a92d4-3b58-4e07-ac21-5d8b04e3719f
// last-edited: 2026-08-30

package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// withAppDirConfig points the process config's app directories at subdirectories
// of root and restores the previous values afterwards.
//
// These walkers resolve AppDirs with appdirs.Current() inside their own bodies
// (the alternative would widen calculateLibrarySizes' signature, which is passed
// as a function VALUE to sysinfo.NewSystemService), so the test has to drive the
// process config rather than pass a struct.
//
// The root string is used verbatim, never through EvalSymlinks: on macOS
// t.TempDir() lives under /var, which is a symlink to /private/var, and
// resolving one side but not the other makes filepath.Rel inside AppDirs
// disagree — the guard would no-op and the test would pass for the wrong reason.
func withAppDirConfig(t *testing.T, root string) {
	t.Helper()
	prevBackup, prevDump := config.AppConfig.BackupDir, config.AppConfig.OpenLibraryDumpDir
	config.AppConfig.BackupDir = filepath.Join(root, "backups")
	config.AppConfig.OpenLibraryDumpDir = filepath.Join(root, "openlibrary-dumps")
	t.Cleanup(func() {
		config.AppConfig.BackupDir, config.AppConfig.OpenLibraryDumpDir = prevBackup, prevDump
	})
}

// TestCalculateLibrarySizes_ExcludesAppDirs pins the library-size walk.
//
// This one is not a deletion hazard but a WRONG ANSWER: on a real install the
// reported library size silently included ~58 GB of database archives and
// ~39 GB of OpenLibrary dumps. Both default to NON-DOT directory names, which
// is why the fixture uses non-dot names — a dot-named fixture would have been
// skipped by the pre-existing rule and proved nothing.
func TestCalculateLibrarySizes_ExcludesAppDirs(t *testing.T) {
	writeN := func(t *testing.T, path string, n int) {
		t.Helper()
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, make([]byte, n), 0o644))
	}

	t.Run("app dirs configured are excluded from the total", func(t *testing.T) {
		resetLibrarySizeCache()
		root := t.TempDir()
		withAppDirConfig(t, root)
		writeN(t, filepath.Join(root, "Author", "Book", "book.m4b"), 1000)
		writeN(t, filepath.Join(root, "backups", "archive.tar.zst"), 500_000)
		writeN(t, filepath.Join(root, "openlibrary-dumps", "db", "000001.sst"), 500_000)

		librarySize, _ := calculateLibrarySizes(root, nil)

		// The two app files are 1,000x the library file; if either were counted
		// the total could not stay under the guard below.
		require.Less(t, librarySize, int64(500_000),
			"library size must exclude backup archives and OpenLibrary dumps")
		require.Greater(t, librarySize, int64(0), "the real book must still be counted")
	})

	t.Run("empty AppDirs: whole tree counted, exactly as before", func(t *testing.T) {
		resetLibrarySizeCache()
		root := t.TempDir()
		prevBackup, prevDump := config.AppConfig.BackupDir, config.AppConfig.OpenLibraryDumpDir
		config.AppConfig.BackupDir, config.AppConfig.OpenLibraryDumpDir = "", ""
		t.Cleanup(func() {
			config.AppConfig.BackupDir, config.AppConfig.OpenLibraryDumpDir = prevBackup, prevDump
		})
		writeN(t, filepath.Join(root, "Author", "Book", "book.m4b"), 1000)
		writeN(t, filepath.Join(root, "backups", "archive.tar.zst"), 500_000)

		librarySize, _ := calculateLibrarySizes(root, nil)
		require.Greater(t, librarySize, int64(500_000),
			"with nothing configured every walker must behave exactly as before")
	})

	t.Run("import paths are guarded as their own walk roots", func(t *testing.T) {
		resetLibrarySizeCache()
		importDir := t.TempDir()
		prevBackup, prevDump := config.AppConfig.BackupDir, config.AppConfig.OpenLibraryDumpDir
		config.AppConfig.BackupDir = filepath.Join(importDir, "backups")
		config.AppConfig.OpenLibraryDumpDir = ""
		t.Cleanup(func() {
			config.AppConfig.BackupDir, config.AppConfig.OpenLibraryDumpDir = prevBackup, prevDump
		})
		writeN(t, filepath.Join(importDir, "incoming", "book.m4b"), 1000)
		writeN(t, filepath.Join(importDir, "backups", "archive.tar.zst"), 500_000)

		_, importSize := calculateLibrarySizes("", []database.ImportPath{{Path: importDir, Enabled: true}})
		require.Less(t, importSize, int64(500_000),
			"an app dir inside an import path must be excluded too")
		require.Greater(t, importSize, int64(0))
	})
}
