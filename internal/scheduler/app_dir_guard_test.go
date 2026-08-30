// file: internal/scheduler/app_dir_guard_test.go
// version: 1.0.0
// guid: 0e5b8c74-a916-4d23-b7f0-42c98e1b6035
// last-edited: 2026-08-30

package scheduler

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/stretchr/testify/require"
)

// TestRunCleanupOldBackups_SkipsAppDirs pins scheduler.cleanup-old-backups,
// which DELETES any file whose name contains ".bak-" past the retention window.
//
// The fixture app directories are deliberately NON-DOT (`backups`,
// `openlibrary-dumps`) — those are the real defaults, and a dot-named fixture
// would be skipped by pathutil's pre-existing dot rule, so it could not tell a
// working AppDirs guard from a deleted one.
//
// The temp root is used verbatim, never EvalSymlinks'd: t.TempDir() on macOS
// lives under /var (a symlink to /private/var), and resolving one side of the
// comparison but not the other makes filepath.Rel inside AppDirs disagree,
// silently no-opping the guard while the test still passes.
// noopProgress is the whole cleanupProgressLogger surface.
type noopProgress struct{}

func (noopProgress) Log(_, _ string, _ *string) error { return nil }

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
			prevRoot := config.AppConfig.RootDir
			prevBackup, prevDump := config.AppConfig.BackupDir, config.AppConfig.OpenLibraryDumpDir
			config.AppConfig.RootDir = root
			if tc.appDirs {
				config.AppConfig.BackupDir = filepath.Join(root, "backups")
				config.AppConfig.OpenLibraryDumpDir = filepath.Join(root, "openlibrary-dumps")
			} else {
				config.AppConfig.BackupDir, config.AppConfig.OpenLibraryDumpDir = "", ""
			}
			t.Cleanup(func() {
				config.AppConfig.RootDir = prevRoot
				config.AppConfig.BackupDir, config.AppConfig.OpenLibraryDumpDir = prevBackup, prevDump
			})

			old := time.Now().Add(-90 * 24 * time.Hour)
			libraryFile := filepath.Join(root, "Author", "Book", "cover.bak-20250101")
			backupFile := filepath.Join(root, "backups", "db.bak-20250101")
			dumpFile := filepath.Join(root, "openlibrary-dumps", "db", "ol.bak-20250101")
			for _, p := range []string{libraryFile, backupFile, dumpFile} {
				require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
				require.NoError(t, os.WriteFile(p, []byte("x"), 0o644))
				require.NoError(t, os.Chtimes(p, old, old))
			}

			require.NoError(t, runCleanupOldBackups(context.Background(), noopProgress{}))

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
