// file: internal/server/organize_row_writers_test.go
// version: 1.3.0
// guid: 7f4c1a92-53d8-4a06-9c7e-1b0d2e6f84a3
// last-edited: 2026-09-12

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
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	maintenanceplugin "github.com/falkcorp/audiobook-organizer/internal/plugins/maintenance"
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

	srv := &Server{store: store, organizeService: NewOrganizeService(store), metadataFetchService: lockedMetafetch(store)}
	srv.organizeService.VersionGroupLocker = writeBackPathLocks.lock
	events := traceWriteBackLocks(t)
	moved, err := srv.organizeAfterWriteBack(f.book.ID, "op-batch-save", logger.New("test"))
	require.NoError(t, err)
	require.True(t, moved, "the book was outside the library; organize must report that it moved")

	assertRowsLandedInLibrary(t, f)

	// The organize runs in the file work's lock order: the book's key, then
	// the key of the path it re-read under it; and the new version is made
	// under the version-group key. It used to take a path lock alone, keyed on
	// a FilePath read before the write-back, and the version no key at all.
	ev := events()
	require.Contains(t, ev, "+"+bookLockKeyForTest(f.book.ID), "the organize must take the book's file-work lock: %v", ev)
	require.Contains(t, ev, "+"+filepath.Clean(f.srcDir), "the organize must lock the book's path: %v", ev)
	require.True(t, hasEventPrefix(ev, "+vg:"), "CreateOrganizedVersion must hold the version-group key: %v", ev)
	requireFileWorkLockOrder(t, ev)
}

// lockedMetafetch is a metafetch service on store sharing writeBackPathLocks,
// as server.go wires it.
func lockedMetafetch(store *database.PebbleStore) *metafetch.Service {
	mfs := metafetch.NewService(store)
	mfs.SetPathLocker(writeBackPathLocks.lock)
	return mfs
}

// bookLockKeyForTest is metafetch's per-book lock-table key.
func bookLockKeyForTest(id string) string { return "book:" + id }

func hasEventPrefix(events []string, prefix string) bool {
	for _, e := range events {
		if strings.HasPrefix(e, prefix) {
			return true
		}
	}
	return false
}

// traceWriteBackLocks records every acquire ("+key") and release ("-key") in
// writeBackPathLocks, the table the server and metafetch share.
func traceWriteBackLocks(t *testing.T) func() []string {
	t.Helper()
	var mu sync.Mutex
	var events []string
	writeBackPathLocks.setTrace(func(ev string) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	})
	t.Cleanup(func() { writeBackPathLocks.setTrace(nil) })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), events...)
	}
}

// requireFileWorkLockOrder fails on a book key taken while a path key is held
// -- path-then-book, the reverse of metafetch's lockBook order -- and on a
// path key taken with no book key held, a file lock no book lock covers.
// Version-group keys are leaves and may come anywhere.
func requireFileWorkLockOrder(t *testing.T, events []string) {
	t.Helper()
	held := map[string]int{}
	holding := func(match func(string) bool) bool {
		for k, n := range held {
			if n > 0 && match(k) {
				return true
			}
		}
		return false
	}
	isBook := func(k string) bool { return strings.HasPrefix(k, "book:") }
	isPath := func(k string) bool { return !isBook(k) && !strings.HasPrefix(k, "vg:") }
	for _, ev := range events {
		key := ev[1:]
		if ev[0] == '-' {
			held[key]--
			continue
		}
		if isBook(key) {
			require.False(t, holding(isPath), "book key %s taken while a path key was held: %v", key, events)
		}
		if isPath(key) {
			require.True(t, holding(isBook), "path key %s taken with no book key held: %v", key, events)
		}
		held[key]++
	}
}

// The bulk write-back holds no key of the shared lock table around
// RunApplyPipelineRenameOnly or WriteBackMetadataForBook: each takes the
// book's key and then path keys itself. A path lock put back around either is
// path-then-book, a deadlock against an apply of the same book, and on a book
// that is its own target a self-deadlock on the non-reentrant table.
func TestBulkWriteBack_HoldsNoFileLockAroundTheFileWork(t *testing.T) {
	if testing.Short() {
		t.Skip("touches the filesystem and the full write-back pipeline")
	}
	store := rowWritersStore(t)
	f := newRowWritersFixture(t, store)
	srv := &Server{store: store, metadataFetchService: lockedMetafetch(store)}
	events := traceWriteBackLocks(t)

	done := make(chan error, 1)
	go func() {
		done <- srv.runBulkWriteBack(context.Background(), "op-lock-order", []string{f.book.ID}, true, 0,
			registryProgressAdapter{r: &sdReporter{id: "op-lock-order"}}, nil)
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("the bulk write-back deadlocked on its own lock table")
	}
	ev := events()
	n := 0
	for _, e := range ev {
		if e == "+"+bookLockKeyForTest(f.book.ID) {
			n++
		}
	}
	require.Equal(t, 2, n, "the rename and the write-back each take the book's lock: %v", ev)
	requireFileWorkLockOrder(t, ev)
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
	srv := &Server{store: base, organizeService: NewOrganizeService(failing), metadataFetchService: lockedMetafetch(base)}

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

	// The op ID is placed only the way production places it: the registry's
	// run-context decorator calls maintenanceplugin.WithOpID.
	const autoScanOp = "op-folder-auto-scan-under-test"
	reporter := &rowWritersReporter{}
	require.NoError(t, def.Run(maintenanceplugin.WithOpID(context.Background(), autoScanOp), params, reporter))

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

	requireChangesUnderOp(t, store, autoScanOp)
}

// requireChangesUnderOp asserts organize wrote at least one change row and
// every row it wrote carries opID.
func requireChangesUnderOp(t *testing.T, store database.Store, opID string) {
	t.Helper()
	changes, err := store.GetOperationChanges(opID)
	require.NoError(t, err)
	require.NotEmpty(t, changes, "organize inside a tracked op must record its changes under that op's ID")
	for _, c := range changes {
		require.Equal(t, opID, c.OperationID)
	}
}

// TestPerformScanOrganizeRecordsChangesUnderRunContextOpID drives the real
// library.scan / library.import path: ScanService.PerformScan with the
// server's hook as AutoOrganizeFn. The op ID is on ctx only via
// maintenanceplugin.WithOpID, which is what the registry's run-context
// decorator installs. PerformScan passes "" as its own op ID, so a hook that
// read a scanner-side key recorded nothing in production while its unit test,
// which set that key by hand, passed.
func TestPerformScanOrganizeRecordsChangesUnderRunContextOpID(t *testing.T) {
	if testing.Short() {
		t.Skip("scans a directory and runs the full organize pipeline")
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
	config.AppConfig.SupportedExtensions = []string{".m4b"}

	srv := &Server{store: store, organizeService: NewOrganizeService(store)}
	svc := scanner.NewScanService(store)
	svc.AutoOrganizeFn = srv.autoOrganizeScannedBooks

	const scanOp = "op-library-scan-under-test"
	require.NoError(t, svc.PerformScan(maintenanceplugin.WithOpID(context.Background(), scanOp),
		&scanner.ScanRequest{FolderPath: &folder}, logger.New("test")))

	requireChangesUnderOp(t, store, scanOp)
}

// TestAutoOrganizeScannedBooksRecordsChangesUnderScanOp: organize inside
// library.scan records its change rows under the scan's operation ID. The hook
// used to build its Request with no OperationID, so every scan-path rename,
// adopt, _copyN move and skip wrote no row at all.
func TestAutoOrganizeScannedBooksRecordsChangesUnderScanOp(t *testing.T) {
	if testing.Short() {
		t.Skip("touches the filesystem and the full organize pipeline")
	}
	store := rowWritersStore(t)
	f := newRowWritersFixture(t, store)

	const scanOp = "op-library-scan-under-test"
	srv := &Server{store: store, organizeService: NewOrganizeService(store)}
	srv.autoOrganizeScannedBooks(maintenanceplugin.WithOpID(context.Background(), scanOp),
		[]scanner.Book{{FilePath: f.srcDir}}, logger.New("test"))

	assertRowsLandedInLibrary(t, f)
	requireChangesUnderOp(t, store, scanOp)
}
