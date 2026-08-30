// file: internal/reconcile/app_dir_guard_test.go
// version: 1.0.0
// guid: 9d5c3e78-1b46-4a02-8f95-7c2e60b3d417
// last-edited: 2026-08-30

package reconcile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
)

// TestBuildFileIndex_SkipsAppDirs pins the iTunes-heal index. A file indexed
// from an app directory becomes a candidate destination for REPOINTING an
// iTunes track, which would aim the library at application state.
//
// Non-dot fixture directories: `backups` and `openlibrary-dumps` are the real
// defaults, and neither has a leading dot, so the pre-existing dot rule never
// covered them.
func TestBuildFileIndex_SkipsAppDirs(t *testing.T) {
	extSet := map[string]bool{".m4b": true}

	tests := []struct {
		name          string
		app           func(root string) pathutil.AppDirs
		wantIndexed   []string
		wantUnindexed []string
	}{
		{
			name: "app dirs configured: their audio files are not indexed",
			app: func(root string) pathutil.AppDirs {
				return pathutil.AppDirs{
					BackupDir:          filepath.Join(root, "backups"),
					OpenLibraryDumpDir: filepath.Join(root, "openlibrary-dumps"),
				}
			},
			wantIndexed:   []string{"library.m4b"},
			wantUnindexed: []string{"archived.m4b", "dumped.m4b"},
		},
		{
			name:          "empty AppDirs: whole tree indexed, exactly as before",
			app:           func(string) pathutil.AppDirs { return pathutil.AppDirs{} },
			wantIndexed:   []string{"library.m4b", "archived.m4b", "dumped.m4b"},
			wantUnindexed: nil,
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

			idx := BuildFileIndex([]string{root}, extSet, tt.app(root))

			for _, name := range tt.wantIndexed {
				if len(idx[name]) == 0 {
					t.Errorf("%s should be indexed", name)
				}
			}
			for _, name := range tt.wantUnindexed {
				if len(idx[name]) != 0 {
					t.Errorf("%s is inside an app dir and must NOT be indexed", name)
				}
			}
		})
	}
}
