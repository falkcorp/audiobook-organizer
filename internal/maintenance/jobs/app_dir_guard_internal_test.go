// file: internal/maintenance/jobs/app_dir_guard_internal_test.go
// version: 1.0.0
// guid: e2a71c58-9430-4b6d-8c17-05fe93b2a64d
// last-edited: 2026-08-30

// This file is in `package jobs` (not jobs_test) because repair-missing-files'
// per-entry guards live in the unexported rmfr_repairOne, which the external
// test package cannot reach.
package jobs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
	"github.com/stretchr/testify/require"
)

type noopBookFileMutator struct{}

func (noopBookFileMutator) GetBookFiles(string) ([]database.BookFile, error) { return nil, nil }
func (noopBookFileMutator) UpdateBookFile(string, *database.BookFile) error  { return nil }

// TestRepairMissingFiles_ReadDirGuardSkipsAppDirs pins the SINGLE-LEVEL
// os.ReadDir guards in rmfr_repairOne's tier-4 author-directory enumeration.
//
// os.ReadDir does not descend, so filepath.SkipDir is meaningless here and the
// walk-based helper cannot simply be dropped in; the guard is a per-entry test
// on the joined path, applied only to directory entries. This test exists to
// prove that shape works, because the failure mode is quiet: an app directory
// gets enumerated as though it were an author directory, and a file inside it
// becomes a REPOINT TARGET for a book_file row whose path went missing — the
// library would then point at application state.
//
// The fixture app directory is named "Smithson-archive" specifically so that it
// MATCHES the author-name substring test. A name that could not match would
// leave the guard unexercised, and the test would pass with it deleted.
func TestRepairMissingFiles_ReadDirGuardSkipsAppDirs(t *testing.T) {
	audioExts := map[string]bool{".m4b": true}

	setup := func(t *testing.T) (root, missing string) {
		t.Helper()
		root = t.TempDir()
		// The app directory doubles as a plausible author directory name.
		album := filepath.Join(root, "Smithson-archive", "Winter Tales")
		require.NoError(t, os.MkdirAll(album, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(album, "book.m4b"), []byte("x"), 0o644))
		// The stored path is gone from disk, which is what makes this row a
		// repair candidate at all.
		return root, filepath.Join(root, "gone", "book.m4b")
	}

	call := func(t *testing.T, root, missing string, app pathutil.AppDirs) rmfr_result {
		t.Helper()
		return rmfr_repairOne(
			database.BookFileCore{ID: "f1", BookID: "b1", FilePath: missing},
			map[string]rmfr_bookMeta{"b1": {title: "Winter Tales", author: "Jane Smithson-archive"}},
			map[string]string{},
			itunes.ImportOptions{},
			true, // dryRun — never write during a guard test
			[]string{root},
			app,
			audioExts,
			func() {},
			func() map[string][]string { return map[string][]string{} },
			noopBookFileMutator{},
			"",
		)
	}

	t.Run("app dir configured: not enumerated as an author dir", func(t *testing.T) {
		root, missing := setup(t)
		res := call(t, root, missing, pathutil.AppDirs{
			OpenLibraryDumpDir: filepath.Join(root, "Smithson-archive"),
		})
		require.Empty(t, res.NewPath,
			"a file inside an app dir must never become a repair target")
		require.NotEqual(t, "author-title", res.Method)
	})

	t.Run("empty AppDirs: unchanged pre-existing behaviour", func(t *testing.T) {
		root, missing := setup(t)
		res := call(t, root, missing, pathutil.AppDirs{})
		require.NotEmpty(t, res.NewPath,
			"with nothing configured the walker must behave exactly as before")
	})
}

// TestRepairMissingFiles_FilenameIndexSkipsAppDirs pins the WalkDir filename
// index. Anything it indexes is a candidate REPOINT TARGET for a book_file row
// whose path went missing, so an entry from the backup or OpenLibrary dump tree
// would aim a library row at application state.
func TestRepairMissingFiles_FilenameIndexSkipsAppDirs(t *testing.T) {
	audioExts := map[string]bool{".m4b": true}

	seed := func(t *testing.T) string {
		t.Helper()
		root := t.TempDir()
		for _, p := range []string{
			filepath.Join(root, "Author", "Book", "library.m4b"),
			filepath.Join(root, "backups", "archived.m4b"),
			filepath.Join(root, "openlibrary-dumps", "db", "dumped.m4b"),
		} {
			require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
			require.NoError(t, os.WriteFile(p, []byte("x"), 0o644))
		}
		return root
	}

	t.Run("app dirs configured: their audio is not indexed", func(t *testing.T) {
		root := seed(t)
		idx := rmfr_buildFilenameIndex([]string{root}, audioExts, pathutil.AppDirs{
			BackupDir:          filepath.Join(root, "backups"),
			OpenLibraryDumpDir: filepath.Join(root, "openlibrary-dumps"),
		})
		require.Len(t, idx["library.m4b"], 1)
		require.Empty(t, idx["archived.m4b"], "a backup-tree file must never be a repoint target")
		require.Empty(t, idx["dumped.m4b"], "an OL-dump file must never be a repoint target")
	})

	t.Run("empty AppDirs: whole tree indexed, exactly as before", func(t *testing.T) {
		root := seed(t)
		idx := rmfr_buildFilenameIndex([]string{root}, audioExts, pathutil.AppDirs{})
		require.Len(t, idx, 3, "with nothing configured behaviour is unchanged")
	})
}

// TestRepairMissingFiles_Tier5ReadDirSkipsAppDirs pins the SECOND single-level
// os.ReadDir guard — tier 5's flat author-directory scan, which is a separate
// code path from tier 4 and needs its own fixture to be reached at all.
//
// Tier 5 matches a file whose STEM equals the title parsed from the stored
// filename, directly inside an author directory (no album level). The app
// directory is again named to MATCH the author-name substring test, so the
// guard is genuinely exercised.
func TestRepairMissingFiles_Tier5ReadDirSkipsAppDirs(t *testing.T) {
	audioExts := map[string]bool{".m4b": true}

	setup := func(t *testing.T) (root, missing string) {
		t.Helper()
		root = t.TempDir()
		authorDir := filepath.Join(root, "Smithson-archive")
		require.NoError(t, os.MkdirAll(authorDir, 0o755))
		// Flat: the audio file sits directly in the author dir (tier 5 shape),
		// and its stem equals the title parsed from the stored filename.
		require.NoError(t, os.WriteFile(filepath.Join(authorDir, "Winter Tales.m4b"), []byte("x"), 0o644))
		return root, filepath.Join(root, "gone", "01 Winter Tales.m4b")
	}

	call := func(t *testing.T, root, missing string, app pathutil.AppDirs) rmfr_result {
		t.Helper()
		return rmfr_repairOne(
			database.BookFileCore{ID: "f1", BookID: "b1", FilePath: missing},
			// No title => tier 4 is skipped entirely, so only the tier-5 guard
			// can decide the outcome of this fixture.
			map[string]rmfr_bookMeta{"b1": {title: "", author: "Jane Smithson-archive"}},
			map[string]string{},
			itunes.ImportOptions{},
			true,
			[]string{root},
			app,
			audioExts,
			func() {},
			func() map[string][]string { return map[string][]string{} },
			noopBookFileMutator{},
			"",
		)
	}

	t.Run("app dir configured: tier 5 does not enumerate it", func(t *testing.T) {
		root, missing := setup(t)
		res := call(t, root, missing, pathutil.AppDirs{
			OpenLibraryDumpDir: filepath.Join(root, "Smithson-archive"),
		})
		require.Empty(t, res.NewPath, "a file inside an app dir must never become a repair target")
	})

	t.Run("empty AppDirs: unchanged pre-existing behaviour", func(t *testing.T) {
		root, missing := setup(t)
		res := call(t, root, missing, pathutil.AppDirs{})
		require.NotEmpty(t, res.NewPath, "with nothing configured the walker must behave as before")
	})
}
