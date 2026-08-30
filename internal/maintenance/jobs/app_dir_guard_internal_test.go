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
