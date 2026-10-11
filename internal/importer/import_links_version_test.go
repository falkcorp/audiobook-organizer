// file: internal/importer/import_links_version_test.go
// version: 1.1.0
// guid: 2b7d4e91-6c3a-4f08-9a5e-81d0c7f3b264
// last-edited: 2026-10-11

package importer

import (
	"bytes"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/filehash"
)

// syncBuffer is a bytes.Buffer safe for the slog handler and the test to share.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureSlog routes the default slog logger into a buffer for the rest of the
// test. Not parallel-safe, so no test using it calls t.Parallel.
func captureSlog(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// pebbleImportStore opens a real PebbleStore with dir registered as an import
// path. A real store, not the MockStore the other importer tests use: the mock
// returns no rows from GetBookFiles and drops UpdateBookFile, so the version
// link this file is about could never be observed through it -- which is how
// every import orphaning its version went unnoticed.
func pebbleImportStore(t *testing.T, dir string) *database.PebbleStore {
	t.Helper()
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	// Same warmup race as versions.allowDir: an import path written before the
	// memdb publishes can be dropped from the allow-list.
	store.WaitForWarmup()
	if _, err := store.CreateImportPath(dir, "staging"); err != nil {
		t.Fatalf("allow import path: %v", err)
	}
	return store
}

// storedImportRow returns the one book_file row the store holds for bookID.
func storedImportRow(t *testing.T, store *database.PebbleStore, bookID string) database.BookFile {
	t.Helper()
	files, err := store.GetBookFiles(bookID)
	if err != nil {
		t.Fatalf("GetBookFiles: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("want exactly 1 stored book_file row, got %d", len(files))
	}
	return files[0]
}

// THE REGRESSION (IMPORT-ORPHANED-VERSIONS). ImportFile created the version
// BEFORE the book_file row, so versions.CreateIngestVersion found no row to
// link, logged "version is orphaned", and the stored row was left with no
// VersionID and no FileHash -- `unverifiable` to the torrent cleanup fixer
// until a hash backfill ran. Every import did this, torrent or not.
func TestImportFile_StoredRowLinksVersionAndCarriesHash(t *testing.T) {
	withSupportedExt(t)
	dir, path := stageFixture(t)
	store := pebbleImportStore(t, dir)
	logs := captureSlog(t)

	resp, err := NewImportService(store).ImportFile(&ImportFileRequest{FilePath: path})
	if err != nil {
		t.Fatalf("import failed: %v", err)
	}

	row := storedImportRow(t, store, resp.ID)
	active, err := store.GetActiveVersionForBook(resp.ID)
	if err != nil || active == nil {
		t.Fatalf("want an active version for the imported book, got %v (err %v)", active, err)
	}
	if row.VersionID != active.ID {
		t.Errorf("stored row VersionID = %q, want the import's version %q -- the version is orphaned", row.VersionID, active.ID)
	}
	want, err := filehash.BookFileHash(path)
	if err != nil {
		t.Fatalf("control hash: %v", err)
	}
	if row.FileHash != want {
		t.Errorf("stored row FileHash = %q, want %q", row.FileHash, want)
	}
	if row.DelugeHash != "" {
		t.Errorf("no torrent was given, so DelugeHash must stay empty, got %q", row.DelugeHash)
	}
	if strings.Contains(logs.String(), "version is orphaned") {
		t.Errorf("import logged an orphaned version:\n%s", logs.String())
	}
	if vers, _ := store.GetBookVersionsByBookID(resp.ID); len(vers) != 1 {
		t.Errorf("want exactly 1 version for the book, got %d", len(vers))
	}
}

// The torrent half of the same bug: the stored row must carry DelugeHash, set
// in the same write that links the version carrying that hash.
func TestImportFile_StoredRowLinksTorrentVersion(t *testing.T) {
	withSupportedExt(t)
	dir, path := stageFixture(t)
	store := pebbleImportStore(t, dir)
	logs := captureSlog(t)

	resp, err := NewImportService(store).ImportFile(&ImportFileRequest{FilePath: path, TorrentHash: testTorrentHash})
	if err != nil {
		t.Fatalf("import failed: %v", err)
	}
	if !resp.TorrentLinked || resp.TorrentLinkNote != "" {
		t.Errorf("want linked with no note, got linked=%v note=%q", resp.TorrentLinked, resp.TorrentLinkNote)
	}

	row := storedImportRow(t, store, resp.ID)
	ver, err := store.GetBookVersionByTorrentHash(testTorrentHash)
	if err != nil || ver == nil {
		t.Fatalf("want a version indexed by the torrent hash, got %v (err %v)", ver, err)
	}
	if ver.BookID != resp.ID {
		t.Errorf("torrent version belongs to book %q, want %q", ver.BookID, resp.ID)
	}
	if row.VersionID != ver.ID {
		t.Errorf("stored row VersionID = %q, want the torrent version %q", row.VersionID, ver.ID)
	}
	if row.DelugeHash != testTorrentHash {
		t.Errorf("stored row DelugeHash = %q, want %q", row.DelugeHash, testTorrentHash)
	}
	if row.FileHash == "" {
		t.Error("stored row FileHash is empty -- the row is unverifiable for torrent cleanup")
	}
	if strings.Contains(logs.String(), "version is orphaned") {
		t.Errorf("import logged an orphaned version:\n%s", logs.String())
	}
}

// repointStore repoints the new book's row once, inside CreateIngestVersion's
// read-to-write window (at CreateBookVersion, after its GetBookFiles read).
type repointStore struct {
	*database.PebbleStore
	done bool
}

func (s *repointStore) CreateBookVersion(v *database.BookVersion) (*database.BookVersion, error) {
	if !s.done {
		s.done = true
		if rows, err := s.PebbleStore.GetBookFiles(v.BookID); err == nil && len(rows) == 1 {
			_, _ = s.PebbleStore.ModifyBookFile(v.BookID, rows[0].ID, func(r *database.BookFile) error {
				r.FilePath += ".moved"
				return nil
			})
		}
	}
	return s.PebbleStore.CreateBookVersion(v)
}

// A link write the compare-and-set refuses during an import (the row was
// repointed inside CreateIngestVersion's window): the import still succeeds
// with the book created and reports the torrent not linked; the version holds
// the torrent index and the row is left unlinked with the other writer's
// change; and a retry of the same torrent is refused (a 409 at the handler)
// naming that book, so there is still one book.
func TestImportFile_LinkRefusedByCompareAndSetThenRetryIsRefused(t *testing.T) {
	withSupportedExt(t)
	dir, path := stageFixture(t)
	store := &repointStore{PebbleStore: pebbleImportStore(t, dir)}
	is := NewImportService(store)

	resp, err := is.ImportFile(&ImportFileRequest{FilePath: path, TorrentHash: testTorrentHash})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if resp.TorrentLinked || resp.TorrentLinkNote != TorrentLinkNoteLinkFailed {
		t.Errorf("want not linked with note %q, got linked=%v note=%q", TorrentLinkNoteLinkFailed, resp.TorrentLinked, resp.TorrentLinkNote)
	}
	rows, _ := store.PebbleStore.GetBookFiles(resp.ID)
	if len(rows) != 1 || rows[0].VersionID != "" || rows[0].DelugeHash != "" || !strings.HasSuffix(rows[0].FilePath, ".moved") {
		t.Errorf("row: want unlinked, no hash, the concurrent path kept; got %+v", rows)
	}
	if holder, _ := store.PebbleStore.GetBookVersionByTorrentHash(testTorrentHash); holder == nil || holder.BookID != resp.ID {
		t.Errorf("torrent index: %+v", holder)
	}

	_, err2 := is.ImportFile(&ImportFileRequest{FilePath: path, TorrentHash: testTorrentHash})
	var linkedErr *TorrentAlreadyLinkedError
	if !errors.As(err2, &linkedErr) || linkedErr.BookID != resp.ID {
		t.Errorf("retry: want ErrTorrentAlreadyLinked naming %s, got %v", resp.ID, err2)
	}
	if books, _ := store.PebbleStore.GetAllBooksCore(0, 0); len(books) != 1 {
		t.Errorf("want 1 book, got %d", len(books))
	}
}

// An import's version records no Format: the importer passes Book.Format,
// which it never sets, while the book_file row carries the file's format.
// Pinned because maintenance.relink-pre-fix-imports' format evidence relies on
// it: it treats an empty version format as "not recorded", not as a
// disagreement. If imports start recording a version format, revisit that
// check (pfrLinkEvidence) rather than only this test.
func TestImportFile_VersionFormatIsNotRecorded(t *testing.T) {
	withSupportedExt(t)
	dir, path := stageFixture(t)
	store := pebbleImportStore(t, dir)
	resp, err := NewImportService(store).ImportFile(&ImportFileRequest{FilePath: path})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	vers, _ := store.GetBookVersionsByBookID(resp.ID)
	if len(vers) != 1 || vers[0].Format != "" {
		t.Errorf("want one version with an empty Format, got %+v", vers)
	}
	rows, _ := store.GetBookFiles(resp.ID)
	if len(rows) != 1 || rows[0].Format == "" {
		t.Errorf("want one row carrying the file's format, got %+v", rows)
	}
}
