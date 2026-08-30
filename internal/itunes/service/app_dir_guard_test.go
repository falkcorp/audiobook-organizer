// file: internal/itunes/service/app_dir_guard_test.go
// version: 1.0.0
// guid: 4e83b105-72fa-4c68-91d0-8b4e37a6c209
// last-edited: 2026-08-30

package itunesservice

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
)

// TestFSTagScanner_SkipsAppDirs pins the path-repair scanner. Its root is
// cfg.AudiobookRoot, wired from config RootDir, so the application's own
// backup and OpenLibrary dump directories sit inside the tree it enumerates.
// Files found there would be tag-read and could enter the bookID→paths index
// used to repair track locations.
func TestFSTagScanner_SkipsAppDirs(t *testing.T) {
	tests := []struct {
		name      string
		app       func(root string) pathutil.AppDirs
		wantCount int
	}{
		{
			name: "app dirs configured: only library audio is enumerated",
			app: func(root string) pathutil.AppDirs {
				return pathutil.AppDirs{
					BackupDir:          filepath.Join(root, "backups"),
					OpenLibraryDumpDir: filepath.Join(root, "openlibrary-dumps"),
				}
			},
			wantCount: 1,
		},
		{
			name:      "empty AppDirs: full tree enumerated, exactly as before",
			app:       func(string) pathutil.AppDirs { return pathutil.AppDirs{} },
			wantCount: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			for _, p := range []string{
				filepath.Join(root, "Author", "Book", "library.m4b"),
				filepath.Join(root, "backups", "archived.m4b"),
				filepath.Join(root, "openlibrary-dumps", "db", "dumped.m4b"),
			} {
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			scan := newFSTagScanner(root, tt.app(root), nil)
			got := scan.allPaths()
			if len(got) != tt.wantCount {
				t.Errorf("enumerated %d files (%v), want %d", len(got), got, tt.wantCount)
			}
		})
	}
}
