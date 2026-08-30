// file: internal/maintenance/jobs/app_dir_guard_test.go
// version: 1.1.0
// guid: 5b0e47c9-a2d3-4816-9f74-0c63e8b1a5de
// last-edited: 2026-08-30

// Shared test helpers (noopReporter, blank jobs import) live in testhelpers_test.go.
package jobs_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/appdirs"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
	"github.com/stretchr/testify/require"
)

// withLibraryRoot points the process config at root, and (unless appDirs is
// false) declares `<root>/backups` and `<root>/openlibrary-dumps` as
// application-owned.
//
// The fixture directory names are deliberately NON-DOT. A dot-named fixture
// would be skipped by pathutil's pre-existing dot rule, so such a test would
// still pass with the AppDirs guard removed and would prove nothing.
//
// root is used verbatim — never EvalSymlinks'd. t.TempDir() on macOS is under
// /var (a symlink to /private/var); resolving one side of the comparison and
// not the other makes filepath.Rel inside AppDirs disagree, and the guard
// silently no-ops.
func withLibraryRoot(t *testing.T, root string, appDirs bool) {
	t.Helper()
	prevRoot := config.AppConfig.RootDir
	prevBackup := config.AppConfig.BackupDir
	prevDump := config.AppConfig.OpenLibraryDumpDir
	prevDB, prevPlaylist := config.AppConfig.DatabasePath, config.AppConfig.PlaylistDir
	config.AppConfig.RootDir = root
	if appDirs {
		config.AppConfig.BackupDir = filepath.Join(root, "backups")
		config.AppConfig.OpenLibraryDumpDir = filepath.Join(root, "openlibrary-dumps")
	} else {
		// Zero EVERY field appdirs.FromConfig reads, not just these two.
		// backup.ResolveDir SYNTHESIZES "backups" when BackupDir is unset and
		// anchors it to the database's own directory, so clearing BackupDir alone
		// still produces a live absolute exclusion whenever DatabasePath is set --
		// and sibling tests in this tree do set it. The assertion below is the
		// real guarantee: it fails loudly if FromConfig ever grows a source field
		// that nobody zeroed here, instead of letting this subtest quietly stop
		// testing the empty case while still passing.
		config.AppConfig.BackupDir, config.AppConfig.OpenLibraryDumpDir = "", ""
		config.AppConfig.DatabasePath, config.AppConfig.PlaylistDir = "", ""
		if got := appdirs.Current(); got != (pathutil.AppDirs{}) {
			t.Fatalf("the empty-AppDirs case is not actually empty: %+v", got)
		}
	}
	t.Cleanup(func() {
		config.AppConfig.RootDir = prevRoot
		config.AppConfig.BackupDir = prevBackup
		config.AppConfig.OpenLibraryDumpDir = prevDump
		config.AppConfig.DatabasePath, config.AppConfig.PlaylistDir = prevDB, prevPlaylist
	})
}

func mkfile(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o644))
}

// TestCleanupBackupsJob_SkipsAppDirs — this job DELETES by filename regex.
//
// A database archive is named audiobooks_<type>_<timestamp>.tar.{gz,zst}, which
// the regex does not match, so today nothing in the backup tree is destroyed.
// That is a NAMING COINCIDENCE, not a control: a `.backup`/`.bak` file parked
// there by an operator, or a future archive format, is deleted with no warning.
// The fixture uses a name that DOES match, to test the guard rather than the
// coincidence.
func TestCleanupBackupsJob_SkipsAppDirs(t *testing.T) {
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
			withLibraryRoot(t, root, tc.appDirs)

			libraryFile := filepath.Join(root, "Author", "Book", "stale.bak")
			backupFile := filepath.Join(root, "backups", "operator-notes.bak")
			dumpFile := filepath.Join(root, "openlibrary-dumps", "db", "index.backup")
			mkfile(t, libraryFile)
			mkfile(t, backupFile)
			mkfile(t, dumpFile)

			job, err := maintenance.Get("cleanup-backups")
			require.NoError(t, err)
			require.NoError(t, job.Run(context.Background(), nil, &noopReporter{}, false))

			require.NoFileExists(t, libraryFile, "a library .bak must still be cleaned")
			for _, p := range []string{backupFile, dumpFile} {
				if tc.wantAppGone {
					require.NoFileExists(t, p, "with no app dirs configured, behaviour is unchanged")
				} else {
					require.FileExists(t, p, "DATA LOSS: a file inside an app dir was deleted")
				}
			}
		})
	}
}

// TestCleanupEmptyFoldersJob_SkipsAppDirs — the highest-risk walker in the set.
//
// Every other cleanup job is protected from the application's own trees by a
// filename predicate that happens not to match. This one deletes by EMPTINESS
// and has no filename predicate at all, so an empty directory inside the backup
// or dump tree is removed TODAY. That is the concrete data-loss item, not a
// latent one.
func TestCleanupEmptyFoldersJob_SkipsAppDirs(t *testing.T) {
	for _, tc := range []struct {
		name        string
		appDirs     bool
		wantAppGone bool
	}{
		{"app dirs configured: empty dirs inside them survive", true, false},
		{"empty AppDirs: unchanged pre-existing behaviour", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			withLibraryRoot(t, root, tc.appDirs)

			libraryEmpty := filepath.Join(root, "Author", "EmptyBook")
			backupStaging := filepath.Join(root, "backups", "staging")
			dumpPending := filepath.Join(root, "openlibrary-dumps", "pending")
			for _, d := range []string{libraryEmpty, backupStaging, dumpPending} {
				require.NoError(t, os.MkdirAll(d, 0o755))
			}

			job, err := maintenance.Get("cleanup-empty-folders")
			require.NoError(t, err)
			require.NoError(t, job.Run(context.Background(), nil, &noopReporter{}, false))

			require.NoDirExists(t, libraryEmpty, "an empty library dir must still be removed")
			for _, d := range []string{backupStaging, dumpPending} {
				if tc.wantAppGone {
					require.NoDirExists(t, d, "with no app dirs configured, behaviour is unchanged")
				} else {
					require.DirExists(t, d, "DATA LOSS: an empty dir inside an app dir was removed")
				}
			}
		})
	}
}

// TestCleanupOrganizeMessJob_SkipsAppDirs — the second emptiness-based deleter.
//
// This job previously carried its own inline `strings.HasPrefix(base, ".")`
// skip, which the AppDirs guard REPLACES (not supplements) so that pathutil's
// `.alternates` carve-out is not defeated in this one job.
func TestCleanupOrganizeMessJob_SkipsAppDirs(t *testing.T) {
	for _, tc := range []struct {
		name        string
		appDirs     bool
		wantAppGone bool
	}{
		{"app dirs configured: empty dirs inside them survive", true, false},
		{"empty AppDirs: unchanged pre-existing behaviour", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			withLibraryRoot(t, root, tc.appDirs)

			libraryEmpty := filepath.Join(root, "Author", "EmptyBook")
			backupStaging := filepath.Join(root, "backups", "staging")
			dumpPending := filepath.Join(root, "openlibrary-dumps", "pending")
			for _, d := range []string{libraryEmpty, backupStaging, dumpPending} {
				require.NoError(t, os.MkdirAll(d, 0o755))
			}

			job, err := maintenance.Get("cleanup-organize-mess")
			require.NoError(t, err)
			require.NoError(t, job.Run(context.Background(), nil, &noopReporter{}, false))

			require.NoDirExists(t, libraryEmpty, "an empty library dir must still be removed")
			for _, d := range []string{backupStaging, dumpPending} {
				if tc.wantAppGone {
					require.NoDirExists(t, d, "with no app dirs configured, behaviour is unchanged")
				} else {
					require.DirExists(t, d, "DATA LOSS: an empty dir inside an app dir was removed")
				}
			}
		})
	}
}
