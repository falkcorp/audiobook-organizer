// file: internal/transcode/app_dir_guard_test.go
// version: 1.0.0
// guid: 2a7f4b61-8c05-4d39-9e27-6b1d3a50f8c4
// last-edited: 2026-08-30

package transcode

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
)

// TestCleanupStaleTempFiles_SkipsAppDirs pins the second DELETING walker.
//
// Fixture dirs are NON-DOT on purpose: a dot-named fixture is already skipped
// by pathutil's pre-existing dot rule, so it could not tell a working AppDirs
// guard from a deleted one.
func TestCleanupStaleTempFiles_SkipsAppDirs(t *testing.T) {
	tests := []struct {
		name        string
		app         func(root string) pathutil.AppDirs
		wantCleaned int
		wantAppGone bool
	}{
		{
			name: "app dirs configured: stale temps inside them survive",
			app: func(root string) pathutil.AppDirs {
				return pathutil.AppDirs{
					BackupDir:          filepath.Join(root, "backups"),
					OpenLibraryDumpDir: filepath.Join(root, "openlibrary-dumps"),
				}
			},
			wantCleaned: 1,
			wantAppGone: false,
		},
		{
			name:        "empty AppDirs: unchanged pre-existing behaviour",
			app:         func(string) pathutil.AppDirs { return pathutil.AppDirs{} },
			wantCleaned: 3,
			wantAppGone: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			past := time.Now().Add(-4 * time.Hour)
			library := filepath.Join(root, "Author", "Book", "part-transcode.tmp")
			backup := filepath.Join(root, "backups", "old-transcode.tmp")
			dump := filepath.Join(root, "openlibrary-dumps", "db", "x-transcode.tmp")
			for _, p := range []string{library, backup, dump} {
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(p, past, past); err != nil {
					t.Fatal(err)
				}
			}

			cleaned := CleanupStaleTempFiles(root, tt.app(root), 1*time.Hour)
			if cleaned != tt.wantCleaned {
				t.Errorf("cleaned = %d, want %d", cleaned, tt.wantCleaned)
			}
			if _, err := os.Stat(library); err == nil {
				t.Error("library temp file should always be removed")
			}
			for _, p := range []string{backup, dump} {
				_, err := os.Stat(p)
				if tt.wantAppGone && err == nil {
					t.Errorf("expected removed with empty AppDirs: %s", p)
				}
				if !tt.wantAppGone && err != nil {
					t.Errorf("file inside an app dir must NOT be deleted: %s", p)
				}
			}
		})
	}
}
