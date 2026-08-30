// file: internal/plugins/maintenance/app_dir_guard_test.go
// version: 1.0.0
// guid: 7c94e0a6-5d31-4b82-9e08-3f61b7d425ac
// last-edited: 2026-08-30

package maintenance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/stretchr/testify/require"
)

// rootDirDeps overrides fakeDeps' hard-coded "/lib" with a real temp root.
type rootDirDeps struct {
	fakeDeps
	root string
}

func (d rootDirDeps) RootDir() string { return d.root }

// withPluginAppDirs declares `<root>/backups` and `<root>/openlibrary-dumps` as
// application-owned in the process config, which is where appdirs.Current()
// reads from, and restores the previous values afterwards.
//
// NON-DOT names are essential: a dot-named fixture is already skipped by
// pathutil's pre-existing dot rule, so the test would pass with the AppDirs
// guard deleted. root is used verbatim (never EvalSymlinks'd) so that
// filepath.Rel inside AppDirs compares like with like on macOS.
func withPluginAppDirs(t *testing.T, root string, enabled bool) {
	t.Helper()
	prevBackup, prevDump := config.AppConfig.BackupDir, config.AppConfig.OpenLibraryDumpDir
	if enabled {
		config.AppConfig.BackupDir = filepath.Join(root, "backups")
		config.AppConfig.OpenLibraryDumpDir = filepath.Join(root, "openlibrary-dumps")
	} else {
		config.AppConfig.BackupDir, config.AppConfig.OpenLibraryDumpDir = "", ""
	}
	t.Cleanup(func() {
		config.AppConfig.BackupDir, config.AppConfig.OpenLibraryDumpDir = prevBackup, prevDump
	})
}

// TestRunCleanupOldBackups_SkipsAppDirs pins maintenance.cleanup-old-backups,
// which DELETES any file whose name contains ".bak-" past the retention window.
//
// This is the THIRD implementation of "delete old backup files" in the tree
// (see also internal/scheduler/extra_ops.go and
// internal/maintenance/jobs/cleanup_backups.go, which uses a different regex).
// Consolidating them is out of scope here; guarding all three is not.
func TestRunCleanupOldBackups_SkipsAppDirs(t *testing.T) {
	for _, tc := range []struct {
		name        string
		appDirs     bool
		wantAppGone bool
	}{
		{"app dirs configured: files inside them survive", true, false},
		{"empty AppDirs: unchanged pre-existing behaviour", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			withPluginAppDirs(t, root, tc.appDirs)

			old := time.Now().Add(-90 * 24 * time.Hour)
			libraryFile := filepath.Join(root, "Author", "Book", "cover.bak-20250101")
			backupFile := filepath.Join(root, "backups", "db.bak-20250101")
			dumpFile := filepath.Join(root, "openlibrary-dumps", "db", "ol.bak-20250101")
			for _, p := range []string{libraryFile, backupFile, dumpFile} {
				require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
				require.NoError(t, os.WriteFile(p, []byte("x"), 0o644))
				require.NoError(t, os.Chtimes(p, old, old))
			}

			p := New(rootDirDeps{root: root})
			require.NoError(t, p.runCleanupOldBackups(context.Background(), nil, &fakeReporter{}))

			require.NoFileExists(t, libraryFile, "a stale library .bak- file must still be removed")
			for _, f := range []string{backupFile, dumpFile} {
				if tc.wantAppGone {
					require.NoFileExists(t, f, "with no app dirs configured, behaviour is unchanged")
				} else {
					require.FileExists(t, f, "DATA LOSS: a file inside an app dir was deleted")
				}
			}
		})
	}
}

// TestFileProvenanceCapture_SkipsAppDirs pins the provenance sweep, whose harm
// is HASHING: pointed at the library root it would read multi-GB backup
// archives end to end.
//
// Roots are operator-supplied, but that does not make what is found BELOW one
// operator-supplied — the guard applies to app dirs discovered inside the tree,
// while ShouldSkipDir's root exemption keeps a root that IS an app dir fully
// walked. Dry run (apply=false) is used so the assertion is on what was WALKED
// without paying for real hashing.
func TestFileProvenanceCapture_SkipsAppDirs(t *testing.T) {
	files := map[string]string{
		filepath.Join("Author", "Book", "library.m4b"):   "a",
		filepath.Join("backups", "archived.m4b"):         "b",
		filepath.Join("openlibrary-dumps", "dumped.m4b"): "c",
	}

	t.Run("app dirs configured: their audio is not walked", func(t *testing.T) {
		p, _, root := newCaptureFixture(t, files)
		withPluginAppDirs(t, root, true)
		res := runCapture(t, p, map[string]any{"roots": []string{root}})
		require.Equal(t, 1, res.Walked, "only the real library file should be walked")
	})

	t.Run("empty AppDirs: whole tree walked, exactly as before", func(t *testing.T) {
		p, _, root := newCaptureFixture(t, files)
		withPluginAppDirs(t, root, false)
		res := runCapture(t, p, map[string]any{"roots": []string{root}})
		require.Equal(t, 3, res.Walked, "with nothing configured behaviour is unchanged")
	})

	t.Run("an app dir named AS the root is still walked in full", func(t *testing.T) {
		p, _, root := newCaptureFixture(t, files)
		withPluginAppDirs(t, root, true)
		dumps := filepath.Join(root, "openlibrary-dumps")
		raw, err := json.Marshal(map[string]any{"roots": []string{dumps}})
		require.NoError(t, err)
		res, err := p.captureFileProvenance(context.Background(), raw)
		require.NoError(t, err)
		require.Equal(t, 1, res.Walked,
			"an explicitly named root is a deliberate choice and must not be skipped")
	})
}
