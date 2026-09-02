// file: internal/server/organize_row_writers_test.go
// version: 1.0.0
// guid: 7f4c1a92-53d8-4a06-9c7e-1b0d2e6f84a3
// last-edited: 2026-09-02

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/scanner"
)

// Three callers moved audio into the library and then wrote the book row by
// hand, and none of them rewrote the book_file rows that say where a book's
// audio actually IS. The book pointed at the library copy while its rows still
// named the source, so every per-file consumer -- playback, write-back, hash
// repair -- followed a row to a path the book no longer had.
//
// These tests assert on ROWS, not on a return value or a log line: the count
// must match, every path must be under the library root, and no row may still
// name a source path. That last one is the assertion that separates fixed from
// broken -- the old code left exactly the right NUMBER of rows, all of them
// naming the source.

// rowWritersFixture is a two-file book outside the library root, its two
// book_file rows, an author (organize DEFERS a book with no resolvable author
// rather than baking "Unknown Author" into the path, so without one these
// tests would assert against a book that was never organized), and a library
// root to land in.
type rowWritersFixture struct {
	store    *database.PebbleStore
	book     *database.Book
	srcDir   string
	srcFiles []string
	root     string
}

func newRowWritersFixture(t *testing.T, store *database.PebbleStore) rowWritersFixture {
	t.Helper()

	srcDir := filepath.Join(t.TempDir(), "Imported", "Some Title")
	require.NoError(t, os.MkdirAll(srcDir, 0o755))
	root := t.TempDir()

	srcFiles := []string{
		filepath.Join(srcDir, "01 - part one.m4b"),
		filepath.Join(srcDir, "02 - part two.m4b"),
	}
	for i, p := range srcFiles {
		require.NoError(t, os.WriteFile(p, []byte(fmt.Sprintf("audio-bytes-%d", i)), 0o644))
	}

	author, err := store.CreateAuthor("Some Author")
	require.NoError(t, err)

	book, err := store.CreateBook(&database.Book{
		FilePath: srcDir, Title: "Some Title", AuthorID: &author.ID,
	})
	require.NoError(t, err)

	for i, p := range srcFiles {
		require.NoError(t, store.CreateBookFile(&database.BookFile{
			BookID: book.ID, FilePath: p, TrackNumber: i + 1, TrackCount: len(srcFiles),
		}))
	}

	old := config.AppConfig
	t.Cleanup(func() { config.AppConfig = old })
	config.AppConfig.RootDir = root
	config.AppConfig.AutoOrganize = true
	// Explicit "copy": "auto" tries reflink then hardlink first and which one
	// succeeds depends on the filesystem the temp dir lands on, which would
	// make these tests machine-specific.
	config.AppConfig.OrganizationStrategy = "copy"

	return rowWritersFixture{store: store, book: book, srcDir: srcDir, srcFiles: srcFiles, root: root}
}

func rowWritersStore(t *testing.T) *database.PebbleStore {
	t.Helper()
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, database.RunMigrations(store))
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// assertRowsLandedInLibrary is the shared assertion. It finds the book that
// owns the library copy (organize creates a version row for an out-of-root
// book rather than repointing the original) and checks its rows.
func assertRowsLandedInLibrary(t *testing.T, f rowWritersFixture) *database.Book {
	t.Helper()

	core, err := f.store.GetAllBooksCore(0, 0)
	require.NoError(t, err)

	var organized *database.Book
	for i := range core {
		if core[i].ID == f.book.ID {
			continue
		}
		if !strings.HasPrefix(core[i].FilePath, f.root) {
			continue
		}
		full, gerr := f.store.GetBookByID(core[i].ID)
		require.NoError(t, gerr)
		organized = full
	}
	require.NotNil(t, organized, "no book row was created for the library copy; books=%d", len(core))

	rows, err := f.store.GetBookFiles(organized.ID)
	require.NoError(t, err)
	require.Len(t, rows, len(f.srcFiles),
		"the organized book must own one book_file row per source file")

	for _, r := range rows {
		require.True(t, strings.HasPrefix(r.FilePath, f.root),
			"book_file %s still names a path outside the library: %s", r.ID, r.FilePath)
		require.NotContains(t, f.srcFiles, r.FilePath,
			"book_file %s still names the SOURCE file %s -- this is the defect: the audio moved and the row did not",
			r.ID, r.FilePath)
		_, statErr := os.Stat(r.FilePath)
		require.NoError(t, statErr, "book_file %s names %s, which does not exist", r.ID, r.FilePath)
	}
	return organized
}

// TestAutoOrganizeScannedBooksWritesBookFileRows covers the folder-auto-scan
// caller. library.folder-auto-scan ran its own organize loop that called
// OrganizeBookDirectory and threw the returned path map away, so a multi-file
// book it organized kept every row at the pre-organize path. It now delegates
// to this hook, which routes through PerformOrganize.
func TestAutoOrganizeScannedBooksWritesBookFileRows(t *testing.T) {
	if testing.Short() {
		t.Skip("touches the filesystem and the full organize pipeline")
	}
	store := rowWritersStore(t)
	f := newRowWritersFixture(t, store)

	srv := &Server{store: store, organizeService: NewOrganizeService(store)}
	srv.autoOrganizeScannedBooks(context.Background(),
		[]scanner.Book{{FilePath: f.srcDir}}, logger.New("test"))

	assertRowsLandedInLibrary(t, f)
}

// TestOrganizeAfterWriteBackWritesBookFileRows covers the metadata.batch-save
// caller. With Organize:true that op called OrganizeOneBook -- which does the
// file operation and NO database work -- and then wrote nothing at all, so it
// copied books into the library and left them there with no row of any kind.
func TestOrganizeAfterWriteBackWritesBookFileRows(t *testing.T) {
	if testing.Short() {
		t.Skip("touches the filesystem and the full organize pipeline")
	}
	store := rowWritersStore(t)
	f := newRowWritersFixture(t, store)

	srv := &Server{store: store, organizeService: NewOrganizeService(store)}
	moved, err := srv.organizeAfterWriteBack(f.book.ID, "op-batch-save", logger.New("test"))
	require.NoError(t, err)
	require.True(t, moved, "the book was outside the library; organize must report that it moved")

	assertRowsLandedInLibrary(t, f)
}

// failingBatchStore is a real store whose book_file batch write fails. Only
// BatchCreateBookFiles is overridden: everything else, including the version
// row that must be rolled back, is the real implementation.
type failingBatchStore struct {
	*database.PebbleStore
	mu     sync.Mutex
	calls  int
	failed bool
}

func (s *failingBatchStore) BatchCreateBookFiles(files []*database.BookFile) error {
	s.mu.Lock()
	s.calls++
	s.failed = true
	s.mu.Unlock()
	return errors.New("injected: pebble batch write failed")
}

// TestOrganizeRollsBackCreatedCopiesWhenRowWriteFails is the rollback
// contract, and it is the reason Landing.Created exists. When the book_file
// rows cannot be written, the copies this organize made must be removed, the
// original must keep its own rows and FilePath, and -- the part that was
// wrong for years -- the original must NOT be demoted. A demoted original is
// a version group whose primary owns no audio while the row that still has
// the files is marked superseded.
func TestOrganizeRollsBackCreatedCopiesWhenRowWriteFails(t *testing.T) {
	if testing.Short() {
		t.Skip("touches the filesystem and the full organize pipeline")
	}
	base := rowWritersStore(t)
	f := newRowWritersFixture(t, base)

	failing := &failingBatchStore{PebbleStore: base}
	srv := &Server{store: base, organizeService: NewOrganizeService(failing)}

	moved, err := srv.organizeAfterWriteBack(f.book.ID, "op-rollback", logger.New("test"))
	require.Error(t, err, "a failed row write must fail the organize, not be swallowed")
	require.False(t, moved)
	require.True(t, failing.failed, "the injected failure never fired; this test proved nothing")
	require.Contains(t, err.Error(), "rolled back",
		"the error must say the copies were removed, or an operator hunts for them")

	// The original is untouched: same path, same rows, still primary.
	after, err := base.GetBookByID(f.book.ID)
	require.NoError(t, err)
	require.Equal(t, f.srcDir, after.FilePath, "the original book must not be repointed")
	if after.IsPrimaryVersion != nil {
		require.True(t, *after.IsPrimaryVersion,
			"a failed organize must not demote the original -- it still owns the only audio")
	}

	rows, err := base.GetBookFiles(f.book.ID)
	require.NoError(t, err)
	require.Len(t, rows, len(f.srcFiles))
	for _, r := range rows {
		require.Contains(t, f.srcFiles, r.FilePath,
			"the original's rows must still name its source files")
	}

	// Every source file survives, and nothing this organize wrote is left
	// under the library root.
	for _, p := range f.srcFiles {
		_, statErr := os.Stat(p)
		require.NoError(t, statErr, "organize copies, never moves; the source %s must survive a rollback", p)
	}
	//
	// The rollback also removes the directories it emptied, up to and
	// including the library root when the root had nothing else in it -- so a
	// missing root is the strongest possible form of "nothing was left
	// behind", not a failure.
	var leftover []string
	if _, statErr := os.Stat(f.root); statErr == nil {
		require.NoError(t, filepath.WalkDir(f.root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err //nolint:wrapcheck // walk callback
			}
			leftover = append(leftover, path)
			return nil
		}))
	}
	require.Empty(t, leftover, "the copies this organize created must be removed on rollback")
}

// rowWritersReporter captures what an op writes to its OPERATION record, which
// is the only observable an op's Run function offers a test.
type rowWritersReporter struct {
	opsregistry.Reporter
	mu   sync.Mutex
	logs []string
}

func (r *rowWritersReporter) UpdateProgress(_, _ int, message string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, message)
	return nil
}

func (r *rowWritersReporter) Log(_ slog.Level, message string, _ ...slog.Attr) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, message)
	return nil
}

func (r *rowWritersReporter) joined() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.logs, "\n")
}

// TestFolderAutoScanOpDelegatesToTheOrganizeHook is the delegation half, and
// it is deliberately independent of metadata extraction and author
// resolution: it asserts only that the op REACHES autoOrganizeScannedBooks,
// whose completion line it cannot produce any other way. Deleting the call --
// which is what the op did before 2026-09-02, in favour of its own loop --
// makes this line disappear.
func TestFolderAutoScanOpDelegatesToTheOrganizeHook(t *testing.T) {
	if testing.Short() {
		t.Skip("scans a directory")
	}
	store := rowWritersStore(t)

	folder := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(folder, "Some Author - Some Title.m4b"),
		[]byte("audio-bytes"), 0o644))

	scanner.SetStore(store)
	t.Cleanup(func() { scanner.SetStore(nil) })

	old := config.AppConfig
	t.Cleanup(func() { config.AppConfig = old })
	config.AppConfig.RootDir = t.TempDir()
	config.AppConfig.AutoOrganize = true
	config.AppConfig.OrganizationStrategy = "copy"
	// The scan matches on this list; unset (the zero AppConfig a test binary
	// starts with) means every file is skipped and the op finds zero books,
	// which would make the assertion below vacuous.
	config.AppConfig.SupportedExtensions = []string{".m4b"}

	reg := opsregistry.New(store, slog.New(slog.DiscardHandler), 1, nil)
	srv := &Server{store: store, organizeService: NewOrganizeService(store)}
	require.NoError(t, srv.RegisterFolderAutoScanOp(reg))
	def, ok := reg.Def("library.folder-auto-scan")
	require.True(t, ok)

	params, err := json.Marshal(folderAutoScanOpParams{FolderPath: folder})
	require.NoError(t, err)

	reporter := &rowWritersReporter{}
	require.NoError(t, def.Run(context.Background(), params, reporter))

	// "Organizing: n/n books" is PerformOrganize's own progress line. The op's
	// pre-2026-09-02 inline loop called OrganizeBookDirectory directly and
	// never reached PerformOrganize, so it could not produce this.
	require.Contains(t, reporter.joined(), "Organizing:",
		"the op must hand its scanned books to autoOrganizeScannedBooks -> PerformOrganize; "+
			"no other code path in this op reports that phase")

	// And the outcome, not just the call: the scanned book has a library copy
	// whose book_file rows name files under the root. Asserting the rows here
	// too is what keeps this test from passing against a delegation that
	// organizes zero books.
	core, err := store.GetAllBooksCore(0, 0)
	require.NoError(t, err)
	var organizedID string
	for i := range core {
		if strings.HasPrefix(core[i].FilePath, config.AppConfig.RootDir) {
			organizedID = core[i].ID
		}
	}
	require.NotEmpty(t, organizedID, "the scan found books but none were organized into the library")

	rows, err := store.GetBookFiles(organizedID)
	require.NoError(t, err)
	require.NotEmpty(t, rows, "the organized book must own at least one book_file row")
	for _, r := range rows {
		require.True(t, strings.HasPrefix(r.FilePath, config.AppConfig.RootDir),
			"book_file %s still names a path outside the library: %s", r.ID, r.FilePath)
	}
}
