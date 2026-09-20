// file: internal/scanner/directory_book_bounds_test.go
// version: 1.0.0
// guid: 6b1d0f83-2c47-4a91-95ea-0d4b7e6c1a52
// last-edited: 2026-09-19

package scanner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// The directory-book verdict in groupFilesIntoBooks used to be decided by the
// first THREE files of a directory, however many files the directory held. A
// flat iTunes author shelf whose first three entries are consecutive chapters
// of one work therefore collapsed into one book: measured in prod on
// 2026-09-19, a 1,494-file "Gene Wolfe" shelf recorded as a single 404.9-hour
// book (eight times over), and 205 books library-wide holding 20.1% of every
// book_file row. See .claude/notes/oversized-and-split-books-2026-09-19.md.
//
// The tests below pin both bounds that replaced the fixed sample, and the two
// shapes that must keep working unchanged.

// unnumberedNames returns n file names that carry no sequence number anywhere,
// so DetectMultiFileGroup cannot claim the folder and the sample/fallthrough
// rule is what decides. The first three sort first.
func unnumberedNames(n int) []string {
	out := make([]string, 0, n)
	out = append(out, "aaa alpha.mp3", "aab bravo.mp3", "aac charlie.mp3")
	for i := len(out); i < n; i++ {
		j := i - 3
		out = append(out, fmt.Sprintf("m%c%c misc.mp3", 'a'+rune(j/26), 'a'+rune(j%26)))
	}
	return out[:n]
}

// writeDirFixture writes the named files into dir; every name in tagged gets a
// real ID3 album frame, the rest are untagged bytes. It returns the paths in
// os.ReadDir (alphabetical) order, which is how the scan walk hands them over.
func writeDirFixture(t *testing.T, dir string, names []string, album string, tagged func(i int) bool) []string {
	t.Helper()
	for i, n := range names {
		content := []byte("untagged " + n)
		if tagged(i) {
			content = id3AlbumFile(album)
		}
		require.NoError(t, os.WriteFile(filepath.Join(dir, n), content, 0o644))
	}
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	paths := make([]string, 0, len(entries))
	for _, e := range entries {
		paths = append(paths, filepath.Join(dir, e.Name()))
	}
	return paths
}

func hasDirectoryBook(books []Book, dir string) bool {
	for _, b := range books {
		if b.FilePath == dir {
			return true
		}
	}
	return false
}

// TestGroupFilesIntoBooks_ThreeAgreeingFilesDoNotClaimALargeDirectory is the
// red test for bound 1. Only the first three files of a 34-file directory
// carry the album tag; every other file is untagged. Under the old
// `sampleSize := min(3, len(files))` those three were the entire evidence and
// the whole directory became ONE book at the directory path.
func TestGroupFilesIntoBooks_ThreeAgreeingFilesDoNotClaimALargeDirectory(t *testing.T) {
	dir := t.TempDir()
	n := directoryBookFastSampleMaxDirFiles + 10
	files := writeDirFixture(t, dir, unnumberedNames(n), "One Work", func(i int) bool { return i < 3 })

	books := groupFilesIntoBooks(context.Background(), files)

	require.False(t, hasDirectoryBook(books, dir),
		"a %d-file directory became ONE book on the evidence of its first 3 files; "+
			"the other %d were never looked at", n, n-3)
	// The three tagged files still group together (they share an album); the
	// untagged rest become their own records rather than being swallowed.
	require.Greater(t, len(books), 1, "expected the directory to split, got %d book(s)", len(books))
}

// TestGroupFilesIntoBooks_UnanimousLargeDirectoryStillOneBook is the other side
// of bound 1: raising the evidence bar must not split a folder whose files all
// agree. 34 files, all carrying one album tag, no sequence numbers in their
// names (so the multi-file detector declines and this rule really is what
// decides) — still exactly one book at the directory path.
func TestGroupFilesIntoBooks_UnanimousLargeDirectoryStillOneBook(t *testing.T) {
	dir := t.TempDir()
	n := directoryBookFastSampleMaxDirFiles + 10
	files := writeDirFixture(t, dir, unnumberedNames(n), "One Work", func(int) bool { return true })

	books := groupFilesIntoBooks(context.Background(), files)

	require.Len(t, books, 1, "a unanimous %d-file folder must stay one book", n)
	require.Equal(t, dir, books[0].FilePath)
}

// TestGroupFilesIntoBooks_TwelveNumberedChaptersStillOneBook is a regression
// guard, not red evidence: a 12-file numbered-chapter folder sharing an album
// tag is claimed by DetectMultiFileGroup before the sample is reached, so it
// was one book before this change and must stay one book after it. It is here
// because "must not regress a genuine multi-file book" is the stated risk of
// the change, and the cheapest way to hold that claim is to assert it.
func TestGroupFilesIntoBooks_TwelveNumberedChaptersStillOneBook(t *testing.T) {
	dir := t.TempDir()
	files := writeDirFixture(t, dir, chapterFiles(12), "Numbered Book", func(int) bool { return true })

	books := groupFilesIntoBooks(context.Background(), files)

	require.Len(t, books, 1, "12 numbered chapters sharing an album must be one book")
	// The one book owns all 12 files either as an explicit segment list or,
	// on the directory-verdict branch, by being the directory itself.
	if books[0].FilePath != dir {
		require.Len(t, books[0].SegmentFiles, 12,
			"the one book must own all 12 chapter files")
	}
}

// TestGroupFilesIntoBooks_HardBoundRefusesAnAuthorShelf is bound 2: no amount
// of agreement lets one book claim more than maxDirectoryBookFiles files out of
// one flat directory. Every file here carries the SAME album tag — unanimous
// evidence — and the directory verdict is still refused, because a folder this
// size sharing one ALBUM tag is an author shelf whose publisher wrote the
// author's name into every tag, not a 400-hour book.
func TestGroupFilesIntoBooks_HardBoundRefusesAnAuthorShelf(t *testing.T) {
	dir := t.TempDir()
	n := maxDirectoryBookFiles + 1
	files := writeDirFixture(t, dir, unnumberedNames(n), "Gene Wolfe", func(int) bool { return true })

	books := groupFilesIntoBooks(context.Background(), files)

	require.False(t, hasDirectoryBook(books, dir),
		"%d files in one flat directory were claimed as a single book despite the hard bound of %d",
		n, maxDirectoryBookFiles)
}

// TestCreateBookFilesForBook_HardBoundRefusesUnboundedExpansion covers the
// second site of the hard bound. A directory-shaped book row that already
// exists — the 205 measured in prod — reaches the nil-segment expansion on
// every rescan without going back through the grouping rule, so the bound has
// to be enforced here too. It refuses rather than truncates: zero rows, and a
// loud warning, instead of a book silently owning an arbitrary prefix of the
// folder.
func TestCreateBookFilesForBook_HardBoundRefusesUnboundedExpansion(t *testing.T) {
	store, cleanup := setupPebbleStore(t)
	defer cleanup()
	SetStore(store)
	defer SetStore(nil)

	oldExts := config.AppConfig.SupportedExtensions
	t.Cleanup(func() { config.AppConfig.SupportedExtensions = oldExts })
	config.AppConfig.SupportedExtensions = []string{".mp3"}

	dir := t.TempDir()
	names := unnumberedNames(maxDirectoryBookFiles + 1)
	for _, n := range names {
		require.NoError(t, os.WriteFile(filepath.Join(dir, n), []byte("audio"), 0o644))
	}
	row, err := store.CreateBook(&database.Book{FilePath: dir, Title: "Author Shelf"})
	require.NoError(t, err)

	rec := &recordingLogger{Logger: logger.New("test")}
	createBookFilesForBook(dir, nil, rec, false)

	files, err := store.GetBookFiles(row.ID)
	require.NoError(t, err)
	require.Empty(t, files,
		"expansion of a %d-file directory book was not refused: %d book_file rows created",
		len(names), len(files))
	require.True(t, rec.sawWarn(), "the refusal was silent; it must be visible in the scan log")
}

// recordingLogger records whether a Warn was emitted, so the refusal can be
// asserted to be VISIBLE rather than merely to have happened.
type recordingLogger struct {
	logger.Logger
	mu    sync.Mutex
	warns int
}

func (l *recordingLogger) Warn(format string, args ...any) {
	l.mu.Lock()
	l.warns++
	l.mu.Unlock()
	l.Logger.Warn(format, args...)
}

func (l *recordingLogger) sawWarn() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.warns > 0
}

// slowLookupStore widens the window that already exists in saveBookToDatabase
// between its `GetBookByFilePath` existence check and the `CreateBook` that
// acts on the answer. It does not invent a race: in prod that gap spans the
// tag reads, hashing and author/series/work resolution of one book, which is
// seconds. The delay makes it deterministic in a test.
type slowLookupStore struct {
	scannerStore
	delay  time.Duration
	probes atomic.Int32
}

func (s *slowLookupStore) GetBookByFilePath(path string) (*database.Book, error) {
	s.probes.Add(1)
	time.Sleep(s.delay)
	return s.scannerStore.GetBookByFilePath(path)
}

// TestSaveBookToDatabase_ConcurrentWritersMintOneRowPerPath pins the
// create-if-absent stripe. Two writers save a book at the SAME directory path
// at the same time. Before the fix both read nil, both reached CreateBook, and
// two rows existed at one path — the shape behind the eight "Gene Wolfe" twins,
// each owning its own private full set of 1,494 book_file rows.
//
// A directory path is the probe deliberately: ComputeFileHash of a directory
// fails, so every hash-duplicate branch is skipped and GetBookByFilePath is the
// ONLY thing between two writers and two rows.
func TestSaveBookToDatabase_ConcurrentWritersMintOneRowPerPath(t *testing.T) {
	base, cleanup := setupPebbleStore(t)
	defer cleanup()

	slow := &slowLookupStore{scannerStore: base, delay: 100 * time.Millisecond}
	SetStore(slow)
	defer SetStore(nil)

	dir := t.TempDir()

	const writers = 4
	var wg sync.WaitGroup
	errs := make([]error, writers)
	for i := range writers {
		wg.Go(func() {
			book := &Book{FilePath: dir, Title: "Author Shelf"}
			errs[i] = saveBookToDatabase(context.Background(), book)
		})
	}
	wg.Wait()
	for i, err := range errs {
		require.NoError(t, err, "writer %d", i)
	}

	// Count every row at the path, not just the one GetBookByFilePath answers
	// with -- the defect is precisely that the index holds more than one.
	siblings, err := base.GetBooksByTitleInDir("author shelf", filepath.Dir(dir))
	require.NoError(t, err)
	atPath := 0
	for i := range siblings {
		if siblings[i].FilePath == dir {
			atPath++
		}
	}
	require.Equal(t, 1, atPath,
		"%d writers saving one directory path produced %d book rows at it; "+
			"each such row goes on to own its own full set of book_file rows", writers, atPath)
}
