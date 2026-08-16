// file: internal/metafetch/ensure_library_copy_test.go
// version: 1.0.0
// guid: 6f0c81ad-9b2e-4f37-8a51-c4d739e0b182
// last-edited: 2026-08-16

package metafetch

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestEnsureLibraryCopy_EmptyOrganizeIsNotSuccess is the F6 regression.
//
// ensureLibraryCopy redirects work on a protected (iTunes/import) book to a
// library copy, creating one by organizing the files into the library folder.
// It set newBookPath = targetDir unconditionally on a nil error from
// OrganizeBookDirectory — and OrganizeBookDirectory MkdirAll's that directory
// BEFORE copying anything, then skips any source that has vanished from disk.
//
// So a book whose files were all gone (but whose rows were NOT flagged Missing,
// which is the case that had no guard) produced: an empty directory, an empty
// pathMap, a nil error, and a brand-new version-linked book record pointing at
// the empty directory. Every downstream consumer then saw a real book with no
// files.
//
// The fix rejects the empty result inside OrganizeBookDirectory, so this must
// now come back nil — "no library copy could be made" — rather than a record.
func TestEnsureLibraryCopy_EmptyOrganizeIsNotSuccess(t *testing.T) {
	libRoot := t.TempDir()
	importRoot := t.TempDir()

	orig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = orig })
	config.AppConfig.RootDir = libRoot
	config.AppConfig.FolderNamingPattern = "{author}/{title}"
	config.AppConfig.FileNamingPattern = "{title} - {track:02d}"
	config.AppConfig.OrganizationStrategy = "copy"

	// Two segment rows that are NOT flagged Missing but whose files are gone.
	bookDir := filepath.Join(importRoot, "Ghost Book")
	require.NoError(t, os.MkdirAll(bookDir, 0o755))
	segments := []database.BookFile{
		{ID: "f1", FilePath: filepath.Join(bookDir, "ch01.mp3"), TrackNumber: 1},
		{ID: "f2", FilePath: filepath.Join(bookDir, "ch02.mp3"), TrackNumber: 2},
	}
	for _, s := range segments {
		require.NoFileExists(t, s.FilePath, "the whole scenario is that these are gone")
	}

	mock := &database.MockStore{
		GetAllImportPathsFunc: func() ([]database.ImportPath, error) {
			return []database.ImportPath{{ID: 1, Path: importRoot, Enabled: true}}, nil
		},
		GetBookFilesFunc: func(bookID string) ([]database.BookFile, error) {
			return segments, nil
		},
	}
	svc := NewService(mock)

	book := &database.Book{
		ID:       "b1",
		Title:    "Ghost Book",
		Format:   "mp3",
		FilePath: bookDir,
		Author:   &database.Author{Name: "Ghost Author"},
	}
	require.True(t, svc.isProtectedPath(book.FilePath),
		"test precondition: the book must be on a protected path or ensureLibraryCopy returns early")

	got := svc.ensureLibraryCopy(book)

	assert.Nil(t, got, "an organize that copied nothing must not produce a library-copy book record")

	// And nothing may be left claiming to be an organized book: the only thing
	// that could exist under the library root is the empty directory organize
	// created before it discovered every source was gone.
	if entries, err := os.ReadDir(libRoot); err == nil {
		for _, e := range entries {
			sub := filepath.Join(libRoot, e.Name())
			files, _ := os.ReadDir(sub)
			for _, f := range files {
				inner, _ := os.ReadDir(filepath.Join(sub, f.Name()))
				assert.Empty(t, inner, "no audio file should have been produced")
			}
		}
	}
}
