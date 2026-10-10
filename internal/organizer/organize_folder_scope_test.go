// file: internal/organizer/organize_folder_scope_test.go
// version: 1.0.0
// guid: 7c41e0d2-93ab-4f58-b6a1-0e5d28c9f347
// last-edited: 2026-10-10

package organizer

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// Request.FolderPath scopes an organize run to one folder. Until 2026-10-10
// PerformOrganizeStats accepted the field and never read it, so a run a caller
// had scoped to one folder organized the whole library: every book's files
// were copied or moved, not just the ones the caller named.
//
// The fixture plants a book in each of three folders chosen so a sloppy
// prefix match fails: /import/a (the scope), /import/ab (a sibling that
// string-prefix-matches the scope), and /import/b (unrelated).

type folderScopeFixture struct {
	store   *database.PebbleStore
	svc     *Service
	rootDir string
	dirs    map[string]string // folder label -> absolute folder
	books   map[string]string // folder label -> book ID
	files   map[string]string // folder label -> source audio path
}

func newFolderScopeFixture(t *testing.T, labels ...string) *folderScopeFixture {
	t.Helper()
	store := newLandingTestStore(t)

	prev := config.AppConfig
	t.Cleanup(func() { config.AppConfig = prev })
	tmp := t.TempDir()
	rootDir := filepath.Join(tmp, "library")
	config.AppConfig = config.Config{
		RootDir:              rootDir,
		FolderNamingPattern:  "{author}",
		FileNamingPattern:    "{title}",
		OrganizationStrategy: "copy",
	}

	fx := &folderScopeFixture{
		store:   store,
		svc:     NewService(store),
		rootDir: rootDir,
		dirs:    map[string]string{},
		books:   map[string]string{},
		files:   map[string]string{},
	}
	for _, label := range labels {
		dir := filepath.Join(tmp, "import", label)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		src := filepath.Join(dir, "book-"+label+".m4b")
		require.NoError(t, os.WriteFile(src, []byte("audio "+label), 0o644))

		author, err := store.CreateAuthor("Author " + label)
		require.NoError(t, err)
		book, err := store.CreateBook(&database.Book{
			Title:    "Title " + label,
			FilePath: src,
			AuthorID: &author.ID,
		})
		require.NoError(t, err)
		require.NoError(t, store.CreateBookFile(&database.BookFile{
			BookID: book.ID, FilePath: src, Format: "m4b",
		}))
		fx.dirs[label] = dir
		fx.books[label] = book.ID
		fx.files[label] = src
	}
	return fx
}

// bookCount is every book row, organized copies included: an organize in copy
// mode creates a new book row per organized book.
func (fx *folderScopeFixture) bookCount(t *testing.T) int {
	t.Helper()
	books, err := fx.store.GetAllBooksCore(1000, 0)
	require.NoError(t, err)
	return len(books)
}

func (fx *folderScopeFixture) libraryEntries(t *testing.T) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(fx.rootDir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	return out
}

func TestOrganizeFolderScope_OnlyBooksInFolderAreOrganized(t *testing.T) {
	fx := newFolderScopeFixture(t, "a", "ab", "b")
	scope := fx.dirs["a"]

	stats, err := fx.svc.PerformOrganizeStats(context.Background(),
		&Request{FolderPath: &scope}, logger.New("test"))
	require.NoError(t, err)
	require.NotNil(t, stats)

	require.Equal(t, 1, stats.Total, "only the book under the scoped folder may be counted")
	require.Equal(t, 1, stats.Organized)
	require.Len(t, fx.libraryEntries(t), 1, "only the in-scope book's file may land in the library")
	require.Equal(t, 4, fx.bookCount(t), "3 originals + exactly 1 organized copy")

	for _, label := range []string{"ab", "b"} {
		rows, err := fx.store.GetBookFiles(fx.books[label])
		require.NoError(t, err)
		require.Len(t, rows, 1)
		require.Equal(t, fx.files[label], rows[0].FilePath, "%s: out-of-scope book_file row must not move", label)
		_, statErr := os.Stat(fx.files[label])
		require.NoError(t, statErr, "%s: out-of-scope source file must still exist", label)
	}
}

func TestOrganizeFolderScope_TrailingSlashAndDotSegmentsAreNormalized(t *testing.T) {
	fx := newFolderScopeFixture(t, "a", "ab")
	scope := fx.dirs["a"] + string(filepath.Separator) + "." + string(filepath.Separator)

	stats, err := fx.svc.PerformOrganizeStats(context.Background(),
		&Request{FolderPath: &scope}, logger.New("test"))
	require.NoError(t, err)
	require.Equal(t, 1, stats.Total)
	require.Equal(t, 3, fx.bookCount(t))
}

func TestOrganizeFolderScope_IntersectsWithBookIDs(t *testing.T) {
	fx := newFolderScopeFixture(t, "a", "b")
	scope := fx.dirs["a"]

	// The named book is outside the scoped folder: both constraints apply, so
	// nothing is organized rather than the ID winning over the folder.
	stats, err := fx.svc.PerformOrganizeStats(context.Background(),
		&Request{FolderPath: &scope, BookIDs: []string{fx.books["b"]}}, logger.New("test"))
	require.NoError(t, err)
	require.NotNil(t, stats)
	require.Zero(t, stats.Total)
	require.Empty(t, fx.libraryEntries(t))
	require.Equal(t, 2, fx.bookCount(t))
}

func TestOrganizeFolderScope_UnusableFolderIsRejectedNotWidened(t *testing.T) {
	fx := newFolderScopeFixture(t, "a")
	for name, scope := range map[string]string{
		"empty":      "",
		"blank":      "   ",
		"relative":   "import/a",
		"dotdot":     filepath.Join(fx.dirs["a"], "..", "a") + string(filepath.Separator) + "..",
		"dotdot-mid": fx.dirs["a"] + string(filepath.Separator) + ".." + string(filepath.Separator) + "a",
	} {
		t.Run(name, func(t *testing.T) {
			scope := scope
			stats, err := fx.svc.PerformOrganizeStats(context.Background(),
				&Request{FolderPath: &scope}, logger.New("test"))
			require.Error(t, err, "a scope that cannot be honored must fail, never fall back to the whole library")
			require.Nil(t, stats)
			require.Empty(t, fx.libraryEntries(t))
			require.Equal(t, 1, fx.bookCount(t))
		})
	}
}

// A book whose files straddle the scope boundary cannot be organized without
// moving files outside it, so it is held back.
func TestOrganizeFolderScope_BookStraddlingBoundaryIsHeldBack(t *testing.T) {
	fx := newFolderScopeFixture(t, "a", "b")
	extra := filepath.Join(fx.dirs["b"], "part2.m4b")
	require.NoError(t, os.WriteFile(extra, []byte("part 2"), 0o644))
	require.NoError(t, fx.store.CreateBookFile(&database.BookFile{
		BookID: fx.books["a"], FilePath: extra, Format: "m4b",
	}))
	scope := fx.dirs["a"]

	stats, err := fx.svc.PerformOrganizeStats(context.Background(),
		&Request{FolderPath: &scope}, logger.New("test"))
	require.NoError(t, err)
	require.Zero(t, stats.Total)
	require.Empty(t, fx.libraryEntries(t))
	_, statErr := os.Stat(extra)
	require.NoError(t, statErr)
}

// fetchRecorder stands in for FetchMetadataForBook and records every ID it is
// asked about. The real fetch writes to the book row, so a call on an
// out-of-scope book is itself a touch.
func (fx *folderScopeFixture) recordFetches() func() []string {
	var mu sync.Mutex
	var ids []string
	fx.svc.FetchMetadataForBook = func(_ context.Context, id string) (any, error) {
		mu.Lock()
		defer mu.Unlock()
		ids = append(ids, id)
		return nil, nil
	}
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), ids...)
	}
}

// The metadata pass re-read the WHOLE library afterwards, which silently
// replaced a BookIDs scope with "everything" for every FetchMetadataFirst run.
func TestOrganizeFolderScope_FetchMetadataFirstKeepsBookIDsScope(t *testing.T) {
	fx := newFolderScopeFixture(t, "a", "b")
	fetched := fx.recordFetches()

	stats, err := fx.svc.PerformOrganizeStats(context.Background(),
		&Request{BookIDs: []string{fx.books["a"]}, FetchMetadataFirst: true}, logger.New("test"))
	require.NoError(t, err)
	require.Equal(t, 1, stats.Total, "the post-metadata reload must not widen a BookIDs scope")
	require.Equal(t, []string{fx.books["a"]}, fetched())
	require.Len(t, fx.libraryEntries(t), 1)
	require.Equal(t, 3, fx.bookCount(t))
}

func TestOrganizeFolderScope_FetchMetadataFirstKeepsFolderScope(t *testing.T) {
	fx := newFolderScopeFixture(t, "a", "ab", "b")
	fetched := fx.recordFetches()
	scope := fx.dirs["a"]

	stats, err := fx.svc.PerformOrganizeStats(context.Background(),
		&Request{FolderPath: &scope, FetchMetadataFirst: true}, logger.New("test"))
	require.NoError(t, err)
	require.Equal(t, 1, stats.Total)
	require.Equal(t, []string{fx.books["a"]}, fetched(),
		"metadata must be fetched only for in-scope books")
	require.Len(t, fx.libraryEntries(t), 1)
}

// An explicit, empty BookIDs list is a scope that resolved to nothing. It used
// to be indistinguishable from "no scope" and organized the whole library.
func TestOrganizeFolderScope_EmptyBookIDsOrganizesNothing(t *testing.T) {
	fx := newFolderScopeFixture(t, "a", "b")

	stats, err := fx.svc.PerformOrganizeStats(context.Background(),
		&Request{BookIDs: []string{}}, logger.New("test"))
	require.NoError(t, err)
	require.Zero(t, stats.Total)
	require.Empty(t, fx.libraryEntries(t))
	require.Equal(t, 2, fx.bookCount(t))
}

func TestOrganizeFolderScope_NoScopeStillOrganizesEverything(t *testing.T) {
	fx := newFolderScopeFixture(t, "a", "b")

	stats, err := fx.svc.PerformOrganizeStats(context.Background(), &Request{}, logger.New("test"))
	require.NoError(t, err)
	require.Equal(t, 2, stats.Total)
}

func TestFolderScope_Contains(t *testing.T) {
	scope, err := newFolderScope(func() *string { s := "/x/a/"; return &s }())
	require.NoError(t, err)
	for path, want := range map[string]bool{
		"/x/a":            true,
		"/x/a/":           true,
		"/x/a/book.m4b":   true,
		"/x/a/../a/b.mp3": true,  // cleaned before comparing
		"/x/ab":           false, // sibling sharing a string prefix
		"/x/ab/book.m4b":  false,
		"/x":              false,
		"/x/b/a/book":     false,
		"":                false,
		"x/a/book":        false,
	} {
		require.Equal(t, want, scope.contains(path), "contains(%q)", path)
	}
}
