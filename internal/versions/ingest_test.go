// file: internal/versions/ingest_test.go
// version: 1.4.0
// guid: 4f2a3b0c-5d6e-4a70-b8c5-3d7e0f1b9a99
// last-edited: 2026-09-01

package versions

import (
	"crypto/sha256"
	"encoding/hex"
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

	ver, err := CreateIngestVersion(store, IngestVersionParams{
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
	v1, _ := CreateIngestVersion(store, IngestVersionParams{
		BookID: book.ID, FilePath: filepath.Join(dir, "Book.m4b"), Format: "m4b", Source: "imported",
	})
	if v1.Status != database.BookVersionStatusActive {
		t.Fatalf("v1 status = %q, want active", v1.Status)
	}

	// Second version → alt.
	v2, err := CreateIngestVersion(store, IngestVersionParams{
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

	_, err = CreateIngestVersion(store, IngestVersionParams{
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

	if _, err := CreateIngestVersion(store, IngestVersionParams{
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

	ver, _ := CreateIngestVersion(store, IngestVersionParams{
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

	ver, err := CreateIngestVersion(store, IngestVersionParams{
		BookID: book.ID, FilePath: missing, Format: "m4b", Source: "imported",
	})
	if err != nil {
		t.Fatalf("CreateIngestVersion: %v", err)
	}
	if ver == nil {
		t.Fatal("CreateIngestVersion returned a nil version")
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
