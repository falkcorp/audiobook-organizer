// file: internal/metafetch/ensure_library_copy_rows_test.go
// version: 1.0.0
// guid: 2a7b6c31-8e94-4f52-9d0a-6c1e5b83f742
// last-edited: 2026-09-02

package metafetch

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// ensureLibraryCopy is the third caller that moved audio into the library
// without writing the rows that say where it is. It created the copy's
// book_file rows one CreateBookFile at a time with every error only logged,
// so a book could end up with a library copy owning SOME of its files, or
// none -- and then it demoted the original anyway, leaving a version group
// whose primary owned no audio.
//
// These tests use a real PebbleStore because the rows are the subject.

func libraryCopyFixture(t *testing.T) (*database.PebbleStore, *database.Book, string, []string) {
	t.Helper()

	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, database.RunMigrations(store))
	t.Cleanup(func() { _ = store.Close() })

	libRoot := t.TempDir()
	importRoot := t.TempDir()

	orig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = orig })
	config.AppConfig.RootDir = libRoot
	config.AppConfig.FolderNamingPattern = "{author}/{title}"
	config.AppConfig.FileNamingPattern = "{title} - {track:02d}"
	config.AppConfig.OrganizationStrategy = "copy"

	_, err = store.CreateImportPath(importRoot, "Imports")
	require.NoError(t, err)

	bookDir := filepath.Join(importRoot, "Protected Book")
	require.NoError(t, os.MkdirAll(bookDir, 0o755))
	sources := []string{
		filepath.Join(bookDir, "ch01.mp3"),
		filepath.Join(bookDir, "ch02.mp3"),
	}
	for i, p := range sources {
		require.NoError(t, os.WriteFile(p, []byte{byte('a' + i)}, 0o644))
	}

	author, err := store.CreateAuthor("Protected Author")
	require.NoError(t, err)

	book, err := store.CreateBook(&database.Book{
		Title: "Protected Book", Format: "mp3", FilePath: bookDir, AuthorID: &author.ID,
	})
	require.NoError(t, err)

	for i, p := range sources {
		require.NoError(t, store.CreateBookFile(&database.BookFile{
			BookID: book.ID, FilePath: p, TrackNumber: i + 1, TrackCount: len(sources),
		}))
	}

	return store, book, libRoot, sources
}

// TestEnsureLibraryCopyWritesBookFileRows is the row assertion for the
// metafetch caller.
func TestEnsureLibraryCopyWritesBookFileRows(t *testing.T) {
	store, book, libRoot, sources := libraryCopyFixture(t)

	svc := NewService(store)
	require.True(t, svc.isProtectedPath(book.FilePath),
		"test precondition: the book must be on a protected path or ensureLibraryCopy returns early")

	copyBook := svc.ensureLibraryCopy(book)
	require.NotNil(t, copyBook, "a protected book with real files must get a library copy")
	require.True(t, strings.HasPrefix(copyBook.FilePath, libRoot),
		"the library copy must live under the library root, got %s", copyBook.FilePath)

	rows, err := store.GetBookFiles(copyBook.ID)
	require.NoError(t, err)
	require.Len(t, rows, len(sources), "the library copy must own one book_file row per source file")
	for _, r := range rows {
		require.True(t, strings.HasPrefix(r.FilePath, libRoot),
			"book_file %s names a path outside the library: %s", r.ID, r.FilePath)
		require.NotContains(t, sources, r.FilePath,
			"book_file %s still names the SOURCE file %s", r.ID, r.FilePath)
		require.FileExists(t, r.FilePath)
	}

	// The original keeps its own rows at the source; it is demoted, not emptied.
	origRows, err := store.GetBookFiles(book.ID)
	require.NoError(t, err)
	require.Len(t, origRows, len(sources))
	for _, r := range origRows {
		require.Contains(t, sources, r.FilePath)
	}
}

// TestEnsureLibraryCopyCarriesRatingsAndWorkID guards a field list. The old
// implementation built the copy with a full struct copy, so everything came
// along; the copy is now built by organizer.CreateOrganizedVersion, whose
// field list is explicit and omits these. UserRating* is what the user typed
// and exists nowhere else, and WorkID is what joins this book to its other
// editions -- losing either is silent data loss, not a cosmetic diff.
func TestEnsureLibraryCopyCarriesRatingsAndWorkID(t *testing.T) {
	store, book, _, _ := libraryCopyFixture(t)

	workID := "work-abc"
	rating := 4.5
	notes := "the narrator makes this one"
	qty := 3
	book.WorkID = &workID
	book.UserRatingOverall = &rating
	book.UserRatingNotes = &notes
	book.Quantity = &qty
	_, err := store.UpdateBook(book.ID, book)
	require.NoError(t, err)

	svc := NewService(store)
	copyBook := svc.ensureLibraryCopy(book)
	require.NotNil(t, copyBook)

	stored, err := store.GetBookByID(copyBook.ID)
	require.NoError(t, err)
	require.NotNil(t, stored.WorkID, "WorkID must survive onto the library copy")
	require.Equal(t, workID, *stored.WorkID)
	require.NotNil(t, stored.UserRatingOverall, "a user-entered rating must survive onto the library copy")
	require.Equal(t, rating, *stored.UserRatingOverall)
	require.NotNil(t, stored.UserRatingNotes)
	require.Equal(t, notes, *stored.UserRatingNotes)
	require.NotNil(t, stored.Quantity)
	require.Equal(t, qty, *stored.Quantity)
}

// failingBookFileStore fails only the book_file batch write. Everything else,
// including the version row that must be rolled back with it, is real.
type failingBookFileStore struct {
	*database.PebbleStore
	fired bool
}

func (s *failingBookFileStore) BatchCreateBookFiles(files []*database.BookFile) error {
	s.fired = true
	return errors.New("injected: pebble batch write failed")
}

// TestEnsureLibraryCopyRollsBackAndLeavesTheOriginalPrimary is the rollback
// contract for this caller. The old code created the rows one at a time,
// ignored their errors, and demoted the original unconditionally -- so a
// failed row write produced a version group whose primary owned no audio
// while the row that still had the files was marked superseded.
func TestEnsureLibraryCopyRollsBackAndLeavesTheOriginalPrimary(t *testing.T) {
	store, book, libRoot, sources := libraryCopyFixture(t)

	failing := &failingBookFileStore{PebbleStore: store}
	svc := NewService(failing)

	got := svc.ensureLibraryCopy(book)
	require.Nil(t, got, "a copy whose rows could not be written is not a library copy")
	require.True(t, failing.fired, "the injected failure never fired; this test proved nothing")

	after, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.Equal(t, book.FilePath, after.FilePath, "the original must not be repointed")
	if after.IsPrimaryVersion != nil {
		require.True(t, *after.IsPrimaryVersion,
			"a failed library copy must not demote the original -- it still owns the only audio")
	}

	rows, err := store.GetBookFiles(book.ID)
	require.NoError(t, err)
	require.Len(t, rows, len(sources))
	for _, r := range rows {
		require.Contains(t, sources, r.FilePath)
		require.FileExists(t, r.FilePath, "organize copies, never moves; the sources must survive")
	}

	// Nothing this organize wrote may be left under the library root.
	var leftover []string
	if _, statErr := os.Stat(libRoot); statErr == nil {
		require.NoError(t, filepath.WalkDir(libRoot, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err //nolint:wrapcheck // walk callback
			}
			leftover = append(leftover, path)
			return nil
		}))
	}
	require.Empty(t, leftover, "the copies the failed organize created must be removed")
}
