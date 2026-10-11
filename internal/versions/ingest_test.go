// file: internal/versions/ingest_test.go
// version: 1.9.0
// guid: 4f2a3b0c-5d6e-4a70-b8c5-3d7e0f1b9a99
// last-edited: 2026-10-11

package versions

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/filehash"
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o775); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// allowDir registers dir as an import path so paths beneath it pass the
// CreateIngestVersion allow-list gate (go/path-injection). Temp dirs live
// outside the default allow-list prefixes, so tests must opt them in.
//
// It first waits for PebbleStore's async memdb warmup to publish. Without
// this, CreateImportPath's memdb write-through can land while mem() is still
// nil (warmup in progress) and get silently dropped; when warmup later
// publishes its Pebble snapshot — taken before this import path was written —
// GetAllImportPaths (which reads from mem() once published) never sees it,
// and ValidateUserPath's allow-list check fails intermittently with
// ErrPathNotAllowed. See PebbleStore.WaitForWarmup's doc comment for the full
// race description; this is the same class of flake it exists to prevent.
func allowDir(t *testing.T, store *database.PebbleStore, dir string) {
	t.Helper()
	store.WaitForWarmup()
	if _, err := store.CreateImportPath(dir, "test-allow"); err != nil {
		t.Fatalf("allow import path: %v", err)
	}
}

func TestCreateIngestVersion_NewBook(t *testing.T) {
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	dir := t.TempDir()
	allowDir(t, store, dir)
	filePath := filepath.Join(dir, "Book.m4b")
	writeTestFile(t, filePath, "audio-data-for-hash")

	book, _ := store.CreateBook(&database.Book{
		Title: "New Book", FilePath: filePath, Format: "m4b",
	})

	ver, _, err := CreateIngestVersion(store, IngestVersionParams{
		BookID: book.ID, FilePath: filePath, Format: "m4b", Source: "imported",
	})
	if err != nil {
		t.Fatalf("create version: %v", err)
	}
	if ver.Status != database.BookVersionStatusActive {
		t.Errorf("first version status = %q, want active", ver.Status)
	}
	if ver.Source != "imported" {
		t.Errorf("source = %q", ver.Source)
	}
}

func TestCreateIngestVersion_SecondVersionIsAlt(t *testing.T) {
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	dir := t.TempDir()
	allowDir(t, store, dir)
	book, _ := store.CreateBook(&database.Book{
		Title: "Book", FilePath: filepath.Join(dir, "Book.m4b"), Format: "m4b",
	})

	// First version → active.
	v1, _, _ := CreateIngestVersion(store, IngestVersionParams{
		BookID: book.ID, FilePath: filepath.Join(dir, "Book.m4b"), Format: "m4b", Source: "imported",
	})
	if v1.Status != database.BookVersionStatusActive {
		t.Fatalf("v1 status = %q, want active", v1.Status)
	}

	// Second version → alt.
	v2, _, err := CreateIngestVersion(store, IngestVersionParams{
		BookID: book.ID, FilePath: filepath.Join(dir, "Book.mp3"), Format: "mp3", Source: "deluge",
	})
	if err != nil {
		t.Fatalf("v2: %v", err)
	}
	if v2.Status != database.BookVersionStatusAlt {
		t.Errorf("v2 status = %q, want alt", v2.Status)
	}
}

func TestCreateIngestVersion_FingerprintBlocksPurged(t *testing.T) {
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	// Use an allowed path so the rejection under test is the fingerprint gate,
	// not the path allow-list gate.
	dir := t.TempDir()
	allowDir(t, store, dir)
	filePath := filepath.Join(dir, "new.m4b")

	// Create a purged version with a known torrent hash.
	_, _ = store.CreateBookVersion(&database.BookVersion{
		BookID: "old-book", Status: database.BookVersionStatusInactivePurged,
		Format: "m4b", Source: "deluge", TorrentHash: "blocked-hash",
	})

	book, _ := store.CreateBook(&database.Book{
		Title: "New Import", FilePath: filePath, Format: "m4b",
	})

	_, _, err = CreateIngestVersion(store, IngestVersionParams{
		BookID: book.ID, FilePath: filePath, Format: "m4b",
		Source: "deluge", TorrentHash: "blocked-hash",
	})
	if err == nil {
		t.Error("expected fingerprint rejection")
	}
}

// writeLargeSparseFile creates a file larger than filehash.Threshold without
// writing anything like that many bytes: the middle is a hole (APFS/ext4 both
// support sparse files, and a hole reads back as zeros either way).
//
// A test fixture at or below the threshold CANNOT observe the bug this file
// guards: the chunked and whole-file algorithms return the same string there.
// Distinct head and tail markers mean a digest that reads the wrong window is
// also caught, not just one that reads the wrong number of bytes.
func writeLargeSparseFile(t *testing.T, path string, size int64) {
	t.Helper()
	if size <= filehash.Threshold {
		t.Fatalf("fixture size %d is not above filehash.Threshold %d; the two hash strategies agree below it and the test would be vacuous", size, int64(filehash.Threshold))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o775); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()
	if _, err := f.Write([]byte("HEAD-MARKER-ingest")); err != nil {
		t.Fatalf("write head: %v", err)
	}
	if err := f.Truncate(size); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := f.Seek(size-int64(len("TAIL-MARKER-ingest")), 0); err != nil {
		t.Fatalf("seek: %v", err)
	}
	if _, err := f.Write([]byte("TAIL-MARKER-ingest")); err != nil {
		t.Fatalf("write tail: %v", err)
	}
}

// fullFileSHA256 is the WRONG algorithm for book_files.file_hash, reproduced
// here on purpose so the test can assert the stored value is not it.
func fullFileSHA256(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatalf("copy: %v", err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// TestCreateIngestVersion_StoresChunkedHashAboveThreshold is the regression
// test for the ingest half of the file-hash algorithm split.
//
// CreateIngestVersion used to call a local whole-file SHA-256 (HashFile) and
// store the result in book_files.file_hash. For any file over 100 MB that value
// can never equal the digest the scanner and the backfill job write, so
// dedup's exact-file collector — which reports Confidence 1.0 on a match — was
// comparing two different alphabets and silently found nothing.
func TestCreateIngestVersion_StoresChunkedHashAboveThreshold(t *testing.T) {
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	dir := t.TempDir()
	allowDir(t, store, dir)
	filePath := filepath.Join(dir, "Big Book.m4b")
	writeLargeSparseFile(t, filePath, filehash.Threshold+(1<<20))

	book, _ := store.CreateBook(&database.Book{
		Title: "Big", FilePath: filePath, Format: "m4b",
	})
	if err := store.CreateBookFile(&database.BookFile{
		ID: "big1", BookID: book.ID, FilePath: filePath, Format: "m4b",
	}); err != nil {
		t.Fatalf("create book file: %v", err)
	}

	if _, _, err := CreateIngestVersion(store, IngestVersionParams{
		BookID: book.ID, FilePath: filePath, Format: "m4b", Source: "imported",
	}); err != nil {
		t.Fatalf("CreateIngestVersion: %v", err)
	}

	want, err := filehash.BookFileHash(filePath)
	if err != nil {
		t.Fatalf("BookFileHash: %v", err)
	}
	notWant := fullFileSHA256(t, filePath)
	if want == notWant {
		t.Fatalf("fixture is degenerate: chunked and whole-file digests agree, so this test cannot observe the bug")
	}

	files, _ := store.GetBookFiles(book.ID)
	var got string
	for _, f := range files {
		if f.ID == "big1" {
			got = f.FileHash
		}
	}
	if got != want {
		t.Errorf("stored file_hash = %q, want the canonical chunked digest %q", got, want)
	}
	if got == notWant {
		t.Errorf("stored file_hash is a whole-file SHA-256 (%q); book_files.file_hash must hold filehash.BookFileHash", got)
	}
}

func TestCreateIngestVersion_FileHashUpdated(t *testing.T) {
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	dir := t.TempDir()
	allowDir(t, store, dir)
	filePath := filepath.Join(dir, "Book.m4b")
	writeTestFile(t, filePath, "audio-content-to-hash")

	book, _ := store.CreateBook(&database.Book{
		Title: "Hash Test", FilePath: filePath, Format: "m4b",
	})
	_ = store.CreateBookFile(&database.BookFile{
		ID: "f1", BookID: book.ID, FilePath: filePath, Format: "m4b",
	})

	ver, _, _ := CreateIngestVersion(store, IngestVersionParams{
		BookID: book.ID, FilePath: filePath, Format: "m4b", Source: "imported",
	})

	files, _ := store.GetBookFiles(book.ID)
	found := false
	for _, f := range files {
		if f.ID == "f1" {
			found = true
			if f.FileHash == "" {
				t.Errorf("file hash not populated")
			}
			if f.VersionID != ver.ID {
				t.Errorf("version_id = %q, want %q", f.VersionID, ver.ID)
			}
		}
	}
	if !found {
		t.Error("file f1 not found")
	}
}

// TestCreateIngestVersion_LinksVersionEvenWhenHashingFails is the regression
// test for an orphaned version row.
//
// The hash and the version linkage used to share one error gate: the row update
// lived in the `else` of the hash check. So when hashing failed — the file moved
// by a concurrent organize, EACCES, EIO on a NAS — `f.VersionID = ver.ID` was
// skipped too, and CreateIngestVersion returned (ver, nil). A version row
// existed that nothing pointed at, and the caller was told it succeeded.
//
// The book_file row here names a path that does not exist, so BookFileHash
// fails for certain. The version must still be linked, and the hash must be
// left empty for the backfill job rather than the whole update being abandoned.
func TestCreateIngestVersion_LinksVersionEvenWhenHashingFails(t *testing.T) {
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	dir := t.TempDir()
	allowDir(t, store, dir)
	missing := filepath.Join(dir, "Vanished.m4b")

	book, _ := store.CreateBook(&database.Book{
		Title: "Gone", FilePath: missing, Format: "m4b",
	})
	if cerr := store.CreateBookFile(&database.BookFile{
		ID: "f-missing", BookID: book.ID, FilePath: missing, Format: "m4b",
	}); cerr != nil {
		t.Fatalf("CreateBookFile: %v", cerr)
	}

	ver, linked, err := CreateIngestVersion(store, IngestVersionParams{
		BookID: book.ID, FilePath: missing, Format: "m4b", Source: "imported",
	})
	if err != nil {
		t.Fatalf("CreateIngestVersion: %v", err)
	}
	if ver == nil {
		t.Fatal("CreateIngestVersion returned a nil version")
	}
	if !linked {
		t.Error("CreateIngestVersion reported the row unlinked")
	}

	files, gerr := store.GetBookFiles(book.ID)
	if gerr != nil {
		t.Fatalf("GetBookFiles: %v", gerr)
	}
	var got *database.BookFile
	for i := range files {
		if files[i].ID == "f-missing" {
			got = &files[i]
		}
	}
	if got == nil {
		t.Fatal("book file f-missing not found")
	}
	if got.VersionID != ver.ID {
		t.Errorf("version_id = %q, want %q — a hash failure orphaned the version row", got.VersionID, ver.ID)
	}
	if got.FileHash != "" {
		t.Errorf("file_hash = %q, want empty: the file could not be read, so any value here is invented", got.FileHash)
	}
}

// ingestFixture opens a PebbleStore with an allowed dir holding one audio file,
// a book, and a book_file row for that file (ID "f1").
func ingestFixture(t *testing.T, row database.BookFile) (*database.PebbleStore, *database.Book, string) {
	t.Helper()
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	dir := t.TempDir()
	allowDir(t, store, dir)
	filePath := filepath.Join(dir, "Book.m4b")
	writeTestFile(t, filePath, "audio-content-to-hash")

	book, err := store.CreateBook(&database.Book{Title: "Ingest", FilePath: filePath, Format: "m4b"})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	row.ID = "f1"
	row.BookID = book.ID
	row.FilePath = filePath
	row.Format = "m4b"
	if err := store.CreateBookFile(&row); err != nil {
		t.Fatalf("CreateBookFile: %v", err)
	}
	return store, book, filePath
}

func storedRow(t *testing.T, store *database.PebbleStore, bookID, id string) database.BookFile {
	t.Helper()
	files, err := store.GetBookFiles(bookID)
	if err != nil {
		t.Fatalf("GetBookFiles: %v", err)
	}
	for _, f := range files {
		if f.ID == id {
			return f
		}
	}
	t.Fatalf("book file %s not found", id)
	return database.BookFile{}
}

// Re-running ingest over a row that is already linked must not mint a second
// version: that is what makes a row left behind by a failed import safe to
// repair by simply running ingest again.
func TestCreateIngestVersion_RerunOverLinkedRowCreatesNothing(t *testing.T) {
	store, book, filePath := ingestFixture(t, database.BookFile{})
	params := IngestVersionParams{BookID: book.ID, BookFileID: "f1", FilePath: filePath, Format: "m4b", Source: "imported"}

	first, linked, err := CreateIngestVersion(store, params)
	if err != nil || !linked {
		t.Fatalf("first ingest: linked=%v err=%v", linked, err)
	}
	again, linked, err := CreateIngestVersion(store, params)
	if err != nil || !linked {
		t.Fatalf("second ingest: linked=%v err=%v", linked, err)
	}
	if again.ID != first.ID {
		t.Errorf("re-run returned version %q, want the existing %q", again.ID, first.ID)
	}
	if vers, _ := store.GetBookVersionsByBookID(book.ID); len(vers) != 1 {
		t.Errorf("want 1 version after a re-run, got %d", len(vers))
	}
	if got := storedRow(t, store, book.ID, "f1"); got.VersionID != first.ID {
		t.Errorf("row VersionID = %q, want %q", got.VersionID, first.ID)
	}
}

// A VersionID naming a version that no longer exists is stale, not a link: the
// row is relinked to a new version rather than reported linked to nothing.
func TestCreateIngestVersion_RelinksStaleVersionID(t *testing.T) {
	store, book, filePath := ingestFixture(t, database.BookFile{VersionID: "01JGONEVERSION0000000000"})

	ver, linked, err := CreateIngestVersion(store, IngestVersionParams{
		BookID: book.ID, BookFileID: "f1", FilePath: filePath, Format: "m4b", Source: "imported",
	})
	if err != nil || !linked {
		t.Fatalf("ingest: linked=%v err=%v", linked, err)
	}
	if ver.ID == "01JGONEVERSION0000000000" {
		t.Fatal("returned the missing version")
	}
	if got := storedRow(t, store, book.ID, "f1"); got.VersionID != ver.ID {
		t.Errorf("row VersionID = %q, want the new version %q", got.VersionID, ver.ID)
	}
}

// A row that already carries the identity hash (the importer writes it at
// CreateBookFile) is trusted, so ingest reads the file once, not twice. The
// sentinel value proves no rehash overwrote it.
func TestCreateIngestVersion_KeepsHashTheRowAlreadyCarries(t *testing.T) {
	const preset = "caller-computed-hash"
	store, book, filePath := ingestFixture(t, database.BookFile{FileHash: preset})

	if _, linked, err := CreateIngestVersion(store, IngestVersionParams{
		BookID: book.ID, BookFileID: "f1", FilePath: filePath, Format: "m4b", Source: "imported",
	}); err != nil || !linked {
		t.Fatalf("ingest: linked=%v err=%v", linked, err)
	}
	if got := storedRow(t, store, book.ID, "f1"); got.FileHash != preset {
		t.Errorf("FileHash = %q, want the row's own %q (the file was hashed again)", got.FileHash, preset)
	}
}

// The torrent hash lands on the row in the same write as its VersionID, and
// BookFileID picks the named row even when another row shares the path.
func TestCreateIngestVersion_LinksNamedRowWithDelugeHash(t *testing.T) {
	const torrent = "0123456789abcdef0123456789abcdef01234567"
	store, book, filePath := ingestFixture(t, database.BookFile{})
	if err := store.CreateBookFile(&database.BookFile{ID: "f0", BookID: book.ID, FilePath: filePath, Format: "m4b"}); err != nil {
		t.Fatalf("CreateBookFile twin: %v", err)
	}

	ver, linked, err := CreateIngestVersion(store, IngestVersionParams{
		BookID: book.ID, BookFileID: "f1", FilePath: filePath, Format: "m4b", Source: "deluge", TorrentHash: torrent,
	})
	if err != nil || !linked {
		t.Fatalf("ingest: linked=%v err=%v", linked, err)
	}
	got := storedRow(t, store, book.ID, "f1")
	if got.VersionID != ver.ID || got.DelugeHash != torrent {
		t.Errorf("named row: VersionID=%q DelugeHash=%q, want %q and %q", got.VersionID, got.DelugeHash, ver.ID, torrent)
	}
	if got.FileHash == "" {
		t.Error("named row: FileHash empty")
	}
	if twin := storedRow(t, store, book.ID, "f0"); twin.VersionID != "" || twin.DelugeHash != "" {
		t.Errorf("unnamed twin row was linked: VersionID=%q DelugeHash=%q", twin.VersionID, twin.DelugeHash)
	}
}

// failFirstLinkStore is a PebbleStore whose first link write (ModifyBookFile,
// CreateIngestVersion's compare-and-set) fails, which is the "version created,
// link write failed" state an import can be left in.
type failFirstLinkStore struct {
	*database.PebbleStore
	failed bool
}

func (s *failFirstLinkStore) ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error) {
	if !s.failed {
		s.failed = true
		return nil, errors.New("synthetic link failure")
	}
	return s.PebbleStore.ModifyBookFile(bookID, fileID, fn)
}

// A link write that failed leaves a version nothing points at and a row with
// no VersionID. Re-running ingest over that row must ADOPT the stranded
// version, not mint a second one: otherwise the first stays orphaned and
// active, and the new one is created as an alt that steals the torrent index.
func TestCreateIngestVersion_RerunAdoptsVersionStrandedByFailedLink(t *testing.T) {
	const torrent = "0123456789abcdef0123456789abcdef01234567"
	pebble, book, filePath := ingestFixture(t, database.BookFile{})
	store := &failFirstLinkStore{PebbleStore: pebble}
	params := IngestVersionParams{
		BookID: book.ID, BookFileID: "f1", FilePath: filePath, Format: "m4b", Source: "deluge", TorrentHash: torrent,
	}

	first, linked, err := CreateIngestVersion(store, params)
	if err != nil || linked {
		t.Fatalf("first ingest: want created-but-unlinked, got linked=%v err=%v", linked, err)
	}
	again, linked, err := CreateIngestVersion(store, params)
	if err != nil || !linked {
		t.Fatalf("re-run: linked=%v err=%v", linked, err)
	}
	if again.ID != first.ID {
		t.Errorf("re-run returned version %q, want the stranded %q adopted", again.ID, first.ID)
	}
	if vers, _ := pebble.GetBookVersionsByBookID(book.ID); len(vers) != 1 {
		t.Errorf("want exactly 1 version after the re-run, got %d", len(vers))
	}
	got := storedRow(t, pebble, book.ID, "f1")
	if got.VersionID != first.ID || got.DelugeHash != torrent {
		t.Errorf("row: VersionID=%q DelugeHash=%q, want %q and %q", got.VersionID, got.DelugeHash, first.ID, torrent)
	}
	if v, _ := pebble.GetBookVersion(first.ID); v == nil || v.Status != database.BookVersionStatusActive {
		t.Errorf("the adopted version must stay active, got %+v", v)
	}
	if idx, _ := pebble.GetBookVersionByTorrentHash(torrent); idx == nil || idx.ID != first.ID {
		t.Errorf("torrent index must still name %q, got %+v", first.ID, idx)
	}
}

// A row whose VersionID names ANOTHER book's live version (merged rows keep
// the absorbed book's VersionID) is stale for this book, but the torrent that
// version holds is not up for grabs: relinking with that hash would move the
// one-to-one torrent index off a version that is still live, which is the
// theft the importer's already-linked guard exists to prevent. The function
// must refuse rather than rely on every caller to have checked first.
func TestCreateIngestVersion_ForeignVersionIDDoesNotTakeLiveTorrent(t *testing.T) {
	const torrent = "0123456789abcdef0123456789abcdef01234567"
	store, keeper, filePath := ingestFixture(t, database.BookFile{})

	absorbed, err := store.CreateBook(&database.Book{Title: "Absorbed", FilePath: filePath, Format: "m4b"})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	held, err := store.CreateBookVersion(&database.BookVersion{
		BookID: absorbed.ID, Status: database.BookVersionStatusActive, Format: "m4b", Source: "deluge", TorrentHash: torrent,
	})
	if err != nil {
		t.Fatalf("CreateBookVersion: %v", err)
	}
	row := storedRow(t, store, keeper.ID, "f1")
	row.VersionID = held.ID
	if err := store.UpdateBookFile(row.ID, &row); err != nil {
		t.Fatalf("UpdateBookFile: %v", err)
	}

	_, linked, err := CreateIngestVersion(store, IngestVersionParams{
		BookID: keeper.ID, BookFileID: "f1", FilePath: filePath, Format: "m4b", Source: "deluge", TorrentHash: torrent,
	})
	if !errors.Is(err, ErrTorrentLinkedElsewhere) {
		t.Errorf("err = %v, want ErrTorrentLinkedElsewhere", err)
	}
	if linked {
		t.Error("reported linked")
	}
	if idx, _ := store.GetBookVersionByTorrentHash(torrent); idx == nil || idx.ID != held.ID {
		t.Errorf("torrent index must still name the absorbed book's live version %q, got %+v", held.ID, idx)
	}
	if vers, _ := store.GetBookVersionsByBookID(keeper.ID); len(vers) != 0 {
		t.Errorf("no version may be created for the keeper, got %d", len(vers))
	}
	if got := storedRow(t, store, keeper.ID, "f1"); got.DelugeHash != "" {
		t.Errorf("row must not claim the torrent: DelugeHash = %q", got.DelugeHash)
	}
}

// Two live, unreferenced versions with the same torrent hash is not the simple
// failed-link case, so adoption must not pick one: a new version is created
// and linked, and both candidates are left as they were.
func TestCreateIngestVersion_TwoStrandedCandidatesAreNotAdopted(t *testing.T) {
	store, book, filePath := ingestFixture(t, database.BookFile{})
	var candidates []string
	for range 2 {
		v, err := store.CreateBookVersion(&database.BookVersion{
			BookID: book.ID, Status: database.BookVersionStatusAlt, Format: "m4b", Source: "imported",
		})
		if err != nil {
			t.Fatalf("CreateBookVersion: %v", err)
		}
		candidates = append(candidates, v.ID)
	}

	ver, linked, err := CreateIngestVersion(store, IngestVersionParams{
		BookID: book.ID, BookFileID: "f1", FilePath: filePath, Format: "m4b", Source: "imported",
	})
	if err != nil || !linked {
		t.Fatalf("ingest: linked=%v err=%v", linked, err)
	}
	for _, id := range candidates {
		if ver.ID == id {
			t.Fatalf("adopted candidate %q although two were eligible", id)
		}
	}
	if vers, _ := store.GetBookVersionsByBookID(book.ID); len(vers) != 3 {
		t.Errorf("want the 2 candidates plus 1 new version, got %d", len(vers))
	}
	if got := storedRow(t, store, book.ID, "f1"); got.VersionID != ver.ID {
		t.Errorf("row VersionID = %q, want the new version %q", got.VersionID, ver.ID)
	}
}

// failStrandedLookupStore fails the one read strandedVersion makes.
type failStrandedLookupStore struct{ *database.PebbleStore }

func (failStrandedLookupStore) GetBookVersionsByBookID(string) ([]database.BookVersion, error) {
	return nil, errors.New("synthetic version-list failure")
}

// NewBook means the caller created the book moments ago, so nothing can be
// stranded and the stranded-version lookup is skipped: a failure of that read
// must not cost the version, and with it the torrent index entry that turns a
// retried discover import into a 409. Without NewBook the same failure aborts
// the create (the control half), so the test cannot pass because the store
// stopped failing.
func TestCreateIngestVersion_NewBookSkipsStrandedLookup(t *testing.T) {
	const torrent = "0123456789abcdef0123456789abcdef01234567"
	pebble, book, filePath := ingestFixture(t, database.BookFile{})
	store := failStrandedLookupStore{PebbleStore: pebble}
	params := IngestVersionParams{
		BookID: book.ID, BookFileID: "f1", FilePath: filePath, Format: "m4b", Source: "deluge", TorrentHash: torrent,
	}

	if _, _, err := CreateIngestVersion(store, params); err == nil {
		t.Fatal("control: without NewBook a failed version-list read must abort the create")
	}
	if idx, _ := pebble.GetBookVersionByTorrentHash(torrent); idx != nil {
		t.Fatalf("control: the aborted create must leave no torrent index entry, got %+v", idx)
	}

	params.NewBook = true
	ver, linked, err := CreateIngestVersion(store, params)
	if err != nil || !linked || ver == nil {
		t.Fatalf("NewBook: ver=%v linked=%v err=%v, want a created, linked version", ver, linked, err)
	}
	if idx, _ := pebble.GetBookVersionByTorrentHash(torrent); idx == nil || idx.ID != ver.ID {
		t.Errorf("torrent index must name the new version %q, got %+v", ver.ID, idx)
	}
	if got := storedRow(t, pebble, book.ID, "f1"); got.VersionID != ver.ID || got.DelugeHash != torrent {
		t.Errorf("row: VersionID=%q DelugeHash=%q, want %q and %q", got.VersionID, got.DelugeHash, ver.ID, torrent)
	}
}

// strandVersion creates a version of book carrying torrent that no row names:
// the state every import before 2026-10-10 left behind.
func strandVersion(t *testing.T, store *database.PebbleStore, bookID, torrent string) *database.BookVersion {
	t.Helper()
	status := database.BookVersionStatusActive
	if a, _ := store.GetActiveVersionForBook(bookID); a != nil {
		status = database.BookVersionStatusAlt
	}
	v, err := store.CreateBookVersion(&database.BookVersion{BookID: bookID, Status: status,
		Format: "m4b", Source: "deluge", TorrentHash: torrent})
	if err != nil {
		t.Fatalf("CreateBookVersion: %v", err)
	}
	return v
}

// An adopt-only call (AdoptVersionID) links the row to the named stranded
// version and writes the row's hash, exactly as the plain re-run does.
func TestCreateIngestVersion_AdoptOnlyLinksTheNamedStrandedVersion(t *testing.T) {
	const torrent = "1123456789abcdef0123456789abcdef01234567"
	store, book, filePath := ingestFixture(t, database.BookFile{DelugeHash: torrent})
	v := strandVersion(t, store, book.ID, torrent)

	got, linked, err := CreateIngestVersion(store, IngestVersionParams{
		BookID: book.ID, BookFileID: "f1", FilePath: filePath, TorrentHash: torrent, AdoptVersionID: v.ID,
	})
	if err != nil || !linked || got == nil || got.ID != v.ID {
		t.Fatalf("adopt-only: got=%+v linked=%v err=%v, want %s linked", got, linked, err, v.ID)
	}
	row := storedRow(t, store, book.ID, "f1")
	if row.VersionID != v.ID || row.DelugeHash != torrent || row.FileHash == "" {
		t.Errorf("row: VersionID=%q DelugeHash=%q FileHash=%q", row.VersionID, row.DelugeHash, row.FileHash)
	}
}

// An adopt-only call never creates a version: with nothing stranded, or a
// different version stranded, it refuses and leaves the row and the book's
// versions exactly as they were.
func TestCreateIngestVersion_AdoptOnlyNeverCreates(t *testing.T) {
	t.Run("nothing stranded", func(t *testing.T) {
		store, book, filePath := ingestFixture(t, database.BookFile{})
		_, linked, err := CreateIngestVersion(store, IngestVersionParams{
			BookID: book.ID, BookFileID: "f1", FilePath: filePath, AdoptVersionID: "no-such-version",
		})
		if !errors.Is(err, ErrVersionNotAdoptable) || linked {
			t.Fatalf("want ErrVersionNotAdoptable, got linked=%v err=%v", linked, err)
		}
		if vers, _ := store.GetBookVersionsByBookID(book.ID); len(vers) != 0 {
			t.Errorf("adopt-only created %d version(s)", len(vers))
		}
		if row := storedRow(t, store, book.ID, "f1"); row.VersionID != "" || row.FileHash != "" {
			t.Errorf("row written: VersionID=%q FileHash=%q", row.VersionID, row.FileHash)
		}
	})
	t.Run("another version stranded", func(t *testing.T) {
		store, book, filePath := ingestFixture(t, database.BookFile{})
		v := strandVersion(t, store, book.ID, "")
		_, _, err := CreateIngestVersion(store, IngestVersionParams{
			BookID: book.ID, BookFileID: "f1", FilePath: filePath, AdoptVersionID: v.ID + "-other",
		})
		if !errors.Is(err, ErrVersionNotAdoptable) {
			t.Fatalf("want ErrVersionNotAdoptable, got %v", err)
		}
		if vers, _ := store.GetBookVersionsByBookID(book.ID); len(vers) != 1 {
			t.Errorf("want the 1 stranded version only, got %d", len(vers))
		}
		if row := storedRow(t, store, book.ID, "f1"); row.VersionID != "" {
			t.Errorf("row linked to %q", row.VersionID)
		}
	})
	t.Run("two stranded", func(t *testing.T) {
		store, book, filePath := ingestFixture(t, database.BookFile{})
		v := strandVersion(t, store, book.ID, "")
		strandVersion(t, store, book.ID, "")
		_, _, err := CreateIngestVersion(store, IngestVersionParams{
			BookID: book.ID, BookFileID: "f1", FilePath: filePath, AdoptVersionID: v.ID,
		})
		if !errors.Is(err, ErrVersionNotAdoptable) {
			t.Fatalf("want ErrVersionNotAdoptable for an ambiguous book, got %v", err)
		}
		if vers, _ := store.GetBookVersionsByBookID(book.ID); len(vers) != 2 {
			t.Errorf("want 2 versions, got %d", len(vers))
		}
	})
}
