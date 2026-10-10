// file: internal/organizer/organize_folder_scope_test.go
// version: 1.1.1
// guid: 7c41e0d2-93ab-4f58-b6a1-0e5d28c9f347
// last-edited: 2026-10-10

package organizer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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

// newFolderScopeFixture plants one book per label under <tmp>/import/<label>,
// outside the library root, so an organize copies it in (strategy "copy").
func newFolderScopeFixture(t *testing.T, labels ...string) *folderScopeFixture {
	t.Helper()
	return buildFolderScopeFixture(t, false, labels...)
}

// newInPlaceFolderScopeFixture plants one book per label under
// <root>/incoming/<label>, INSIDE the library root, so an organize renames it
// in place. That is the branch that moves the source file, so it is the one
// where "the out-of-scope file is still where it was" can actually fail.
func newInPlaceFolderScopeFixture(t *testing.T, labels ...string) *folderScopeFixture {
	t.Helper()
	return buildFolderScopeFixture(t, true, labels...)
}

func buildFolderScopeFixture(t *testing.T, underRoot bool, labels ...string) *folderScopeFixture {
	t.Helper()
	store := newLandingTestStore(t)
	if underRoot {
		// In-place moves ask the complete ownership index (memdb) who owns a
		// file and refuse while it is not serving; serve it, as prod does
		// after warmup (see setupInPlace).
		store.UseMemDB = true
	}

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
	base := filepath.Join(tmp, "import")
	if underRoot {
		base = filepath.Join(rootDir, "incoming")
	}
	for _, label := range labels {
		dir := filepath.Join(base, label)
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

// libraryEntries lists the files that landed under the library root, minus
// the fixture's own incoming/ tree (the in-place fixture's sources live there).
func (fx *folderScopeFixture) libraryEntries(t *testing.T) []string {
	t.Helper()
	incoming := filepath.Join(fx.rootDir, "incoming")
	var out []string
	_ = filepath.WalkDir(fx.rootDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && p == incoming {
			return filepath.SkipDir
		}
		if !d.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	return out
}

// landedContents is the content of every file that landed in the library, so
// a test can say WHICH book landed rather than only how many did.
func (fx *folderScopeFixture) landedContents(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, p := range fx.libraryEntries(t) {
		b, err := os.ReadFile(p)
		require.NoError(t, err)
		out = append(out, string(b))
	}
	sort.Strings(out)
	return out
}

// target is where an organize of label's book would land it.
func (fx *folderScopeFixture) target(t *testing.T, label string) string {
	t.Helper()
	b, err := fx.store.GetBookByID(fx.books[label])
	require.NoError(t, err)
	require.NotNil(t, b)
	target, err := fx.svc.newOrganizer().GenerateTargetPath(b)
	require.NoError(t, err)
	return target
}

// repointFilePath sets label's book.file_path to path and leaves its book_file
// rows alone: the stale-column shape, where the two disagree.
func (fx *folderScopeFixture) repointFilePath(t *testing.T, label, path string) {
	t.Helper()
	require.NoError(t, fx.setFilePath(label, path))
}

// setFilePath is repointFilePath for code off the test goroutine.
func (fx *folderScopeFixture) setFilePath(label, path string) error {
	b, err := fx.store.GetBookByID(fx.books[label])
	if err != nil {
		return err
	}
	if b == nil {
		return fmt.Errorf("book %s not found", fx.books[label])
	}
	b.FilePath = path
	_, err = fx.store.UpdateBook(b.ID, b)
	return err
}

// plantStale writes a file outside the scoped folder with content no fixture
// book has, so a test can tell whether it was organized.
func plantStale(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "stale.m4b")
	require.NoError(t, os.WriteFile(p, []byte(staleContent), 0o644))
	return p
}

const staleContent = "OUT OF SCOPE"

func requireNotExist(t *testing.T, path, msg string) {
	t.Helper()
	_, err := os.Stat(path)
	require.Truef(t, os.IsNotExist(err), "%s: %s exists (stat err %v)", msg, path, err)
}

func requireContent(t *testing.T, path, want, msg string) {
	t.Helper()
	got, err := os.ReadFile(path)
	require.NoErrorf(t, err, "%s: %s", msg, path)
	require.Equal(t, want, string(got), msg)
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
	require.Equal(t, []string{"audio a"}, fx.landedContents(t),
		"exactly the in-scope book's file, and no other, may land in the library")
	require.Equal(t, 4, fx.bookCount(t), "3 originals + exactly 1 organized copy")

	// Copy mode never removes a source, so "the source still exists" proves
	// nothing here. What would show a leak is the out-of-scope book's
	// DESTINATION existing.
	for _, label := range []string{"ab", "b"} {
		requireNotExist(t, fx.target(t, label), label+": an out-of-scope book must not land in the library")
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
			require.ErrorIs(t, err, ErrInvalidFolderScope, "a scope that cannot be honored must fail, never fall back to the whole library")
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
	require.Equal(t, 1, stats.Collisions[OutcomeStraddlesFolderScope],
		"a held-back book must be counted, not silently dropped")
	require.Empty(t, fx.libraryEntries(t))
	requireNotExist(t, fx.target(t, "a"), "a straddling book must not land")
	requireNotExist(t, fx.target(t, "b"), "b is out of scope")
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

// B1 (review round 1), copy branch. The scope was decided from book_file rows,
// but a single-file book is copied from book.file_path -- the stale column.
// Rows in /import/a, file_path in /import/b, scope /import/a: the run admitted
// the book and copied the /import/b file into the library.
func TestOrganizeFolderScope_StaleFilePathOutsideFolderIsHeldBack(t *testing.T) {
	fx := newFolderScopeFixture(t, "a", "b")
	stale := plantStale(t, fx.dirs["b"])
	fx.repointFilePath(t, "a", stale)
	scope := fx.dirs["a"]

	stats, err := fx.svc.PerformOrganizeStats(context.Background(),
		&Request{FolderPath: &scope}, logger.New("test"))
	require.NoError(t, err)
	require.NotContains(t, fx.landedContents(t), staleContent,
		"a file outside the scoped folder was organized into the library")
	require.Empty(t, fx.libraryEntries(t))
	require.Zero(t, stats.Total)
	require.Equal(t, 1, stats.Collisions[OutcomeFilePathOutsideFolderScope])
	requireContent(t, stale, staleContent, "the out-of-scope file must be untouched")
}

// B1, in-place branch: the worse version. A book already under the library
// root is RENAMED from book.file_path, so a stale file_path outside the scoped
// folder was moved, not copied.
func TestOrganizeFolderScope_InPlaceStaleFilePathOutsideFolderIsNotMoved(t *testing.T) {
	fx := newInPlaceFolderScopeFixture(t, "a", "b")
	stale := plantStale(t, fx.dirs["b"])
	fx.repointFilePath(t, "a", stale)
	scope := fx.dirs["a"]

	stats, err := fx.svc.PerformOrganizeStats(context.Background(),
		&Request{FolderPath: &scope}, logger.New("test"))
	require.NoError(t, err)
	requireContent(t, stale, staleContent, "a file outside the scoped folder was moved")
	requireContent(t, fx.files["a"], "audio a", "a held-back book's own file must not move either")
	requireContent(t, fx.files["b"], "audio b", "b is out of scope")
	require.Empty(t, fx.libraryEntries(t))
	require.Zero(t, stats.Organized+stats.ReOrganized)
	require.Equal(t, 1, stats.Collisions[OutcomeFilePathOutsideFolderScope])
}

// S3: the in-place branch is the one that moves a source, so it is the only
// place "the out-of-scope file is still where it was" can fail. Unscoped, both
// books would be renamed to their targets.
func TestOrganizeFolderScope_InPlaceRenamesOnlyTheScopedBook(t *testing.T) {
	fx := newInPlaceFolderScopeFixture(t, "a", "ab", "b")
	targetA, targetAB, targetB := fx.target(t, "a"), fx.target(t, "ab"), fx.target(t, "b")
	scope := fx.dirs["a"]

	stats, err := fx.svc.PerformOrganizeStats(context.Background(),
		&Request{FolderPath: &scope}, logger.New("test"))
	require.NoError(t, err)
	require.Equal(t, 1, stats.Total)
	require.Equal(t, 1, stats.Organized+stats.ReOrganized)

	requireNotExist(t, fx.files["a"], "the in-scope book must have been renamed away")
	requireContent(t, targetA, "audio a", "the in-scope book must land at its target")
	for label, target := range map[string]string{"ab": targetAB, "b": targetB} {
		requireContent(t, fx.files[label], "audio "+label, label+": an out-of-scope file must stay where it was")
		requireNotExist(t, target, label+": an out-of-scope book must not be renamed")
	}
}

// mergeOnReadStore stands in for a library scan that merges a book while the
// organize waits for its scan lock: the first GetBookByID of bookID runs merge
// against the store, then reads the book back.
type mergeOnReadStore struct {
	*database.PebbleStore
	bookID string
	merge  func() error
	once   sync.Once
}

func (s *mergeOnReadStore) GetBookByID(id string) (*database.Book, error) {
	if id == s.bookID {
		var err error
		s.once.Do(func() { err = s.merge() })
		if err != nil {
			return nil, err
		}
	}
	return s.PebbleStore.GetBookByID(id)
}

// runWithMergeOnRead runs a FolderPath-scoped, scan-locked organize of fx's
// folder a, with merge applied to book a the first time the run reads it by
// ID. FolderPath only: a BookIDs load would call GetBookByID itself, and the
// merge would land before the scope check rather than after it.
//
// merge runs on an organize worker goroutine, not the test's, so it returns an
// error rather than calling require (t.FailNow there would hang the run). An
// error fails the re-read, the book counts as Failed, and the caller's
// Skipped/outcome assertions catch it.
func (fx *folderScopeFixture) runWithMergeOnRead(t *testing.T, merge func() error) *Stats {
	t.Helper()
	wrapped := &mergeOnReadStore{PebbleStore: fx.store, bookID: fx.books["a"], merge: merge}
	scope := fx.dirs["a"]
	stats, err := NewService(wrapped).PerformOrganizeStats(context.Background(),
		&Request{FolderPath: &scope, LockBooksAgainstScan: true}, logger.New("test"))
	require.NoError(t, err)
	return stats
}

// S1 (review round 1): under LockBooksAgainstScan each book is re-read under
// its scan lock and the FRESH row is organized. The scope was decided on the
// listing, so a scan that repointed the book outside the folder in between
// carried the outside file into the library.
func TestOrganizeFolderScope_FreshRowIsRecheckedUnderScanLock(t *testing.T) {
	fx := newFolderScopeFixture(t, "a", "b")
	stale := plantStale(t, fx.dirs["b"])

	stats := fx.runWithMergeOnRead(t, func() error { return fx.setFilePath("a", stale) })
	require.NotContains(t, fx.landedContents(t), staleContent,
		"the re-read row pointed outside the folder and was organized anyway")
	require.Empty(t, fx.libraryEntries(t))
	require.Equal(t, 1, stats.Total, "the book was in scope when the run listed it")
	require.Equal(t, 1, stats.Skipped)
	require.Zero(t, stats.Organized+stats.ReOrganized)
	require.Equal(t, 1, stats.Collisions[OutcomeFilePathOutsideFolderScope])
}

// S1, the other re-check outcome: by its turn nothing the book would touch is
// in the folder any more (its only row went missing and file_path moved out).
// Skipped and counted, not organized and not silently dropped.
func TestOrganizeFolderScope_BookThatLeftTheFolderIsCountedNotOrganized(t *testing.T) {
	fx := newFolderScopeFixture(t, "a", "b")
	stale := plantStale(t, fx.dirs["b"])

	stats := fx.runWithMergeOnRead(t, func() error {
		rows, err := fx.store.GetBookFiles(fx.books["a"])
		if err != nil {
			return err
		}
		for i := range rows {
			rows[i].Missing = true
			if err := fx.store.UpdateBookFile(rows[i].ID, &rows[i]); err != nil {
				return err
			}
		}
		return fx.setFilePath("a", stale)
	})
	require.Empty(t, fx.libraryEntries(t))
	require.Equal(t, 1, stats.Skipped)
	require.Zero(t, stats.Organized+stats.ReOrganized)
	require.Equal(t, 1, stats.Collisions[OutcomeLeftFolderScope])
}

// N4: a Missing row is a path that is gone from disk; organize does not touch
// it, so it cannot put a book outside the scope.
func TestOrganizeFolderScope_MissingRowOutsideFolderDoesNotHoldBookBack(t *testing.T) {
	fx := newFolderScopeFixture(t, "a", "b")
	require.NoError(t, fx.store.CreateBookFile(&database.BookFile{
		BookID: fx.books["a"], FilePath: filepath.Join(fx.dirs["b"], "gone.m4b"), Format: "m4b", Missing: true,
	}))
	scope := fx.dirs["a"]

	stats, err := fx.svc.PerformOrganizeStats(context.Background(),
		&Request{FolderPath: &scope}, logger.New("test"))
	require.NoError(t, err)
	require.Equal(t, 1, stats.Total)
	require.Empty(t, stats.Collisions)
	require.Equal(t, []string{"audio a"}, fx.landedContents(t))
}

// N1: a book with no live rows is handled the way an unscoped run handles it
// when its file_path is inside the folder. Under the library root that means
// it is renamed in place from file_path; scoping the run to its folder used to
// drop it without a count.
func TestOrganizeFolderScope_BookWithNoLiveRowsIsTreatedLikeAnUnscopedRun(t *testing.T) {
	fx := newInPlaceFolderScopeFixture(t, "a", "b")
	rows, err := fx.store.GetBookFiles(fx.books["a"])
	require.NoError(t, err)
	require.Len(t, rows, 1)
	rows[0].Missing = true
	require.NoError(t, fx.store.UpdateBookFile(rows[0].ID, &rows[0]))
	targetA := fx.target(t, "a")
	scope := fx.dirs["a"]

	stats, err := fx.svc.PerformOrganizeStats(context.Background(),
		&Request{FolderPath: &scope}, logger.New("test"))
	require.NoError(t, err)
	require.Equal(t, 1, stats.Total)
	require.Equal(t, 1, stats.Organized+stats.ReOrganized)
	requireContent(t, targetA, "audio a", "the book must be renamed in place exactly as an unscoped run would")
	requireContent(t, fx.files["b"], "audio b", "b is out of scope")
}

// N3: "/" is a valid absolute folder and every absolute path is inside it, so
// it scopes the run to every book that has a file to organize: the whole
// library, by another name. Pinned so a change to that is deliberate.
func TestOrganizeFolderScope_RootFolderMeansEveryBook(t *testing.T) {
	fx := newFolderScopeFixture(t, "a", "b")
	scope := string(filepath.Separator)

	stats, err := fx.svc.PerformOrganizeStats(context.Background(),
		&Request{FolderPath: &scope}, logger.New("test"))
	require.NoError(t, err)
	require.Equal(t, 2, stats.Total)
	require.Equal(t, []string{"audio a", "audio b"}, fx.landedContents(t))
}

// The rule both scopeBooks and the per-book re-check apply, on the cases the
// end-to-end tests above do not reach. Paths are illustrative only.
func TestFolderScope_Admit(t *testing.T) {
	scope, err := newFolderScope(func() *string { s := "/x/a"; return &s }())
	require.NoError(t, err)
	row := func(path string, missing bool) database.BookFileCore {
		return database.BookFileCore{FilePath: path, Missing: missing}
	}
	for name, tc := range map[string]struct {
		filePath string
		rows     []database.BookFileCore
		want     scopeVerdict
	}{
		"rows and file_path inside":    {"/x/a/1.m4b", []database.BookFileCore{row("/x/a/1.m4b", false), row("/x/a/2.m4b", false)}, scopeInside},
		"file_path is the folder":      {"/x/a", []database.BookFileCore{row("/x/a/1.m4b", false)}, scopeInside},
		"nothing inside":               {"/x/b/1.m4b", []database.BookFileCore{row("/x/b/1.m4b", false)}, scopeOutside},
		"rows split":                   {"/x/a/1.m4b", []database.BookFileCore{row("/x/a/1.m4b", false), row("/x/b/2.m4b", false)}, scopeStraddles},
		"rows inside, file_path out":   {"/x/b/old.m4b", []database.BookFileCore{row("/x/a/1.m4b", false)}, scopeFilePathOutside},
		"file_path is a parent dir":    {"/x", []database.BookFileCore{row("/x/a/1.m4b", false)}, scopeFilePathOutside},
		"rows outside, file_path in":   {"/x/a/1.m4b", []database.BookFileCore{row("/x/b/1.m4b", false)}, scopeStraddles},
		"missing row outside ignored":  {"/x/a/1.m4b", []database.BookFileCore{row("/x/a/1.m4b", false), row("/x/b/2.m4b", true)}, scopeInside},
		"N2: empty-path row ignored":   {"/x/a/1.m4b", []database.BookFileCore{row("/x/a/1.m4b", false), row("", false)}, scopeInside},
		"no rows, file_path inside":    {"/x/a/1.m4b", nil, scopeInside},
		"no rows, file_path outside":   {"/x/b/1.m4b", nil, scopeOutside},
		"only missing rows, no path":   {"", []database.BookFileCore{row("/x/a/1.m4b", true)}, scopeOutside},
		"rows inside, empty file_path": {"", []database.BookFileCore{row("/x/a/1.m4b", false)}, scopeInside},
		"nothing at all":               {"", nil, scopeOutside},
		"sibling prefix is not inside": {"/x/ab/1.m4b", []database.BookFileCore{row("/x/ab/1.m4b", false)}, scopeOutside},
	} {
		require.Equal(t, tc.want, scope.admit(tc.filePath, tc.rows), name)
	}
}
