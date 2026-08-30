// file: internal/sweep/app_dir_guard_test.go
// version: 1.0.0
// guid: 6c1d9a35-4e28-4b70-8f13-9a5e2d7c04b8
// last-edited: 2026-08-30

package sweep

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
)

// TestCleanupOrphanedTempFiles_SkipsAppDirs pins the highest-risk guard in this
// change: a DELETING sweep must not descend into directories the application
// owns inside the library root.
//
// The fixture directories are deliberately NON-DOT (`backups`,
// `openlibrary-dumps`). A dot-named fixture would prove nothing -- pathutil's
// pre-existing dot rule already skipped those, so the test would pass with the
// AppDirs guard deleted.
//
// t.TempDir()'s string is used verbatim for both the walk root and the AppDirs
// fields. Do NOT EvalSymlinks it: on macOS /var vs /private/var would make
// filepath.Rel disagree inside underDir, and the guard would silently no-op
// while the test still passed for the wrong reason.
func TestCleanupOrphanedTempFiles_SkipsAppDirs(t *testing.T) {
	type want struct {
		libraryGone bool
		backupGone  bool
		dumpGone    bool
		removed     int
	}
	tests := []struct {
		name string
		app  func(root string) pathutil.AppDirs
		want want
	}{
		{
			name: "app dirs configured: temp files inside them survive",
			app: func(root string) pathutil.AppDirs {
				return pathutil.AppDirs{
					BackupDir:          filepath.Join(root, "backups"),
					OpenLibraryDumpDir: filepath.Join(root, "openlibrary-dumps"),
				}
			},
			want: want{libraryGone: true, backupGone: false, dumpGone: false, removed: 1},
		},
		{
			// The fail-open case. With nothing configured every walker must
			// behave EXACTLY as it did before this change: the full tree is
			// visited and every match is removed.
			name: "empty AppDirs: unchanged pre-existing behaviour, whole tree swept",
			app:  func(string) pathutil.AppDirs { return pathutil.AppDirs{} },
			want: want{libraryGone: true, backupGone: true, dumpGone: true, removed: 3},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			libraryTmp := filepath.Join(root, "Author", "Book", "part.tmp.m4b")
			backupTmp := filepath.Join(root, "backups", "archive.tmp.m4b")
			dumpTmp := filepath.Join(root, "openlibrary-dumps", "db", "sst.tmp.m4b")
			for _, p := range []string{libraryTmp, backupTmp, dumpTmp} {
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			removed := CleanupOrphanedTempFiles(root, tt.app(root), nil, "")

			if removed != tt.want.removed {
				t.Errorf("removed = %d, want %d", removed, tt.want.removed)
			}
			for _, c := range []struct {
				path string
				gone bool
				what string
			}{
				{libraryTmp, tt.want.libraryGone, "library temp file"},
				{backupTmp, tt.want.backupGone, "temp file inside the backup dir"},
				{dumpTmp, tt.want.dumpGone, "temp file inside the OpenLibrary dump dir"},
			} {
				_, err := os.Stat(c.path)
				if c.gone && err == nil {
					t.Errorf("%s should have been removed: %s", c.what, c.path)
				}
				if !c.gone && err != nil {
					t.Errorf("%s must NOT be removed (data loss): %s", c.what, c.path)
				}
			}
		})
	}
}
