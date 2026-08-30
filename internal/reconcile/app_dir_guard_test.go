// file: internal/reconcile/app_dir_guard_test.go
// version: 1.1.0
// guid: 9d5c3e78-1b46-4a02-8f95-7c2e60b3d417
// last-edited: 2026-08-30

package reconcile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/appdirs"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
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

// appDirStore is the 8-method reconcile.Store surface, stubbed. Only
// GetAllImportPaths is exercised by FindUntrackedFiles.
type appDirStore struct{ imports []database.ImportPath }

func (s appDirStore) GetAllBooksCore(int, int) ([]database.BookCore, error) { return nil, nil }
func (s appDirStore) GetBookByID(string) (*database.Book, error)            { return nil, nil }
func (s appDirStore) GetBookFiles(string) ([]database.BookFile, error)      { return nil, nil }
func (s appDirStore) GetBooksByVersionGroup(string) ([]database.Book, error) {
	return nil, nil
}
func (s appDirStore) UpdateBook(string, *database.Book) (*database.Book, error) { return nil, nil }
func (s appDirStore) DeleteBook(string) error                                   { return nil }
func (s appDirStore) GetAllImportPaths() ([]database.ImportPath, error)         { return s.imports, nil }
func (s appDirStore) CreateOperationChange(*database.OperationChange) error     { return nil }

// TestFindUntrackedFiles_SkipsAppDirs pins the untracked-file scan.
//
// Its output becomes IMPORT CANDIDATES, so an audio file sitting in the backup
// or OpenLibrary dump tree would be offered for import as a new book. The
// extension filter is what stops that today — a naming coincidence, not a
// control, which is exactly what this guard replaces.
func TestFindUntrackedFiles_SkipsAppDirs(t *testing.T) {
	for _, tc := range []struct {
		name      string
		appDirs   bool
		wantCount int
	}{
		{"app dirs configured: their audio is not offered for import", true, 1},
		{"empty AppDirs: whole tree scanned, exactly as before", false, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			prevRoot := config.AppConfig.RootDir
			prevBackup, prevDump := config.AppConfig.BackupDir, config.AppConfig.OpenLibraryDumpDir
			prevExts := config.AppConfig.SupportedExtensions
			prevDB, prevPlaylist := config.AppConfig.DatabasePath, config.AppConfig.PlaylistDir
			config.AppConfig.RootDir = root
			config.AppConfig.SupportedExtensions = []string{".m4b"}
			if tc.appDirs {
				config.AppConfig.BackupDir = filepath.Join(root, "backups")
				config.AppConfig.OpenLibraryDumpDir = filepath.Join(root, "openlibrary-dumps")
			} else {
				// Zero EVERY field appdirs.FromConfig reads, not just these two.
				// backup.ResolveDir SYNTHESIZES "backups" when BackupDir is unset and
				// anchors it to the database's own directory, so clearing BackupDir alone
				// still produces a live absolute exclusion whenever DatabasePath is set --
				// and sibling tests in this tree do set it. The assertion below is the real
				// guarantee: it fails loudly if FromConfig ever grows a source field that
				// nobody zeroed here, instead of letting this subtest quietly stop testing
				// the empty case while still passing.
				config.AppConfig.BackupDir, config.AppConfig.OpenLibraryDumpDir = "", ""
				config.AppConfig.DatabasePath, config.AppConfig.PlaylistDir = "", ""
				if got := appdirs.Current(); got != (pathutil.AppDirs{}) {
					t.Fatalf("the empty-AppDirs case is not actually empty: %+v", got)
				}
			}
			t.Cleanup(func() {
				config.AppConfig.RootDir = prevRoot
				config.AppConfig.BackupDir, config.AppConfig.OpenLibraryDumpDir = prevBackup, prevDump
				config.AppConfig.SupportedExtensions = prevExts
				config.AppConfig.DatabasePath, config.AppConfig.PlaylistDir = prevDB, prevPlaylist
			})

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

			got, err := FindUntrackedFiles(appDirStore{}, map[string]bool{})
			if err != nil {
				t.Fatalf("FindUntrackedFiles: %v", err)
			}
			if len(got) != tc.wantCount {
				t.Errorf("found %d untracked files (%v), want %d", len(got), got, tc.wantCount)
			}
		})
	}
}
