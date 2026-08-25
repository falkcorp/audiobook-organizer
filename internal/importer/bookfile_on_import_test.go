// file: internal/importer/bookfile_on_import_test.go
// version: 1.0.0
// guid: 5c1e93a7-4f28-4b60-a8d3-e29b7c046f51
// last-edited: 2026-08-25

package importer

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// stageFixture writes a synthetic .m4b into a temp dir and registers that dir
// as the only allowed import path, so ValidateUserPath admits it. It returns
// the dir and the file path.
//
// DELIBERATELY NOT the repo's testdata/fixtures/test_sample.m4b, and the
// reasoning is worth keeping because the obvious choice is wrong twice over:
//
//  1. It would not test more. ExtractMetadata does NOT error on an unparseable
//     file — measured, not assumed: given 74 bytes of ASCII it returns a nil
//     error and derives Title from the filename. So nothing on this path
//     depends on the bytes being real audio. (The committed fixture carries no
//     artist/title/track tags either, so even fetched it exercises no tag
//     parsing.) An earlier version of this comment claimed a stub "would fail
//     out early"; that was false.
//
//  2. It would test LESS, in CI. testdata/fixtures/*.m4b is Git LFS-tracked
//     (.gitattributes:1) and NO workflow passes `lfs: true` to
//     actions/checkout, so on CI that path holds a 129-byte pointer beginning
//     "version https://git-lfs.github.com/spec/v1". Every assertion below
//     would still pass against it — Format comes from the extension, FileSize
//     is 129 which is > 0 — i.e. green for the wrong reason, invisibly.
//
// What these tests actually need is a file that exists, has a supported
// extension, and has a known size. Synthesising it says so honestly and makes
// the tests independent of whether LFS was fetched.
//
// The repo-wide half of (2) — nine other test files and testutil.CopyFixture
// share the blind spot — is filed in todo.d rather than fixed here.
func stageFixture(t *testing.T) (dir, path string) {
	t.Helper()
	dir = t.TempDir()
	path = filepath.Join(dir, "sample-audiobook.m4b")
	// Enough bytes to have a distinctive, non-zero size. The leading ftyp box
	// is cosmetic — nothing on this path parses it.
	body := append([]byte("\x00\x00\x00\x1cftypM4A \x00\x00\x02\x00M4A isomiso2"),
		bytes.Repeat([]byte("audio-payload"), 64)...)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("stage fixture: %v", err)
	}
	return dir, path
}

// importStore returns a MockStore wired so ImportFile reaches CreateBook, with
// every created book_file row appended to *rows.
func importStore(dir string, rows *[]*database.BookFile) *database.MockStore {
	return &database.MockStore{
		GetAllImportPathsFunc: func() ([]database.ImportPath, error) {
			return []database.ImportPath{{ID: 1, Path: dir, Name: "staging", Enabled: true}}, nil
		},
		CreateBookFunc: func(b *database.Book) (*database.Book, error) {
			out := *b
			out.ID = "01JIMPORTEDBOOK000000000"
			return &out, nil
		},
		CreateBookFileFunc: func(f *database.BookFile) error {
			*rows = append(*rows, f)
			return nil
		},
	}
}

func withSupportedExt(t *testing.T) {
	t.Helper()
	prev := config.AppConfig.SupportedExtensions
	config.AppConfig.SupportedExtensions = []string{".m4b", ".mp3", ".m4a"}
	t.Cleanup(func() { config.AppConfig.SupportedExtensions = prev })
}

// ---------------------------------------------------------------------------

// THE REGRESSION. An imported book got a book row and left the audio on disk
// with NOTHING connecting the two — no book_file row, ever. The gap was
// invisible because it was an INTERFACE gap: CreateBookFile was not on
// importBookStore, so no call site could exist to look broken.
//
// Downstream, organizer.FilterBooksNeedingOrganization (service.go:689-696)
// drops any book outside RootDir with zero book_files. An imported file is
// outside RootDir by definition, so organize-on-import was inert.
func TestImportFile_CreatesABookFileRow(t *testing.T) {
	withSupportedExt(t)
	dir, path := stageFixture(t)

	var rows []*database.BookFile
	is := NewImportService(importStore(dir, &rows))

	resp, err := is.ImportFile(&ImportFileRequest{FilePath: path})
	if err != nil {
		t.Fatalf("import failed: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want exactly 1 book_file row, got %d — the imported book has no route to its audio", len(rows))
	}
	got := rows[0]
	if got.BookID != resp.ID {
		t.Errorf("row must point at the created book %q, got %q", resp.ID, got.BookID)
	}
	if got.FilePath != path {
		t.Errorf("row must point at the imported file %q, got %q", path, got.FilePath)
	}
	if got.Format != "m4b" {
		t.Errorf("format must be the extension without its dot, got %q", got.Format)
	}
	if got.FileSize <= 0 {
		t.Errorf("row must carry the real file size, got %d", got.FileSize)
	}
	if got.TrackNumber < 1 {
		t.Errorf("a single-file book is track 1 at minimum, got %d", got.TrackNumber)
	}
	if got.OriginalFilename != "sample-audiobook.m4b" {
		t.Errorf("row must record the original filename, got %q", got.OriginalFilename)
	}
}

// The size must come from the file on disk, not a constant. Staging the fixture
// twice at different sizes and asserting the row tracks it kills a hard-coded
// value that a single-fixture test would accept.
func TestImportFile_BookFileSizeTracksTheRealFile(t *testing.T) {
	withSupportedExt(t)
	dir, path := stageFixture(t)

	orig, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read staged fixture: %v", err)
	}
	// Append a byte: still a valid enough file for extraction, provably larger.
	grown := filepath.Join(dir, "grown-audiobook.m4b")
	if err := os.WriteFile(grown, append(append([]byte{}, orig...), 0x00), 0o600); err != nil {
		t.Fatalf("stage grown fixture: %v", err)
	}

	var rows []*database.BookFile
	is := NewImportService(importStore(dir, &rows))

	if _, err := is.ImportFile(&ImportFileRequest{FilePath: path}); err != nil {
		t.Fatalf("import original: %v", err)
	}
	if _, err := is.ImportFile(&ImportFileRequest{FilePath: grown}); err != nil {
		t.Fatalf("import grown: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
	if rows[1].FileSize != rows[0].FileSize+1 {
		t.Fatalf("file_size must be read from disk per file: got %d then %d (want +1)",
			rows[0].FileSize, rows[1].FileSize)
	}
}

// A book_file failure must NOT fail the import. The book row is already
// committed at that point, so a 4xx would tell the caller to retry an import
// that already succeeded — duplicating the book.
func TestImportFile_BookFileFailureDoesNotFailTheImport(t *testing.T) {
	withSupportedExt(t)
	dir, path := stageFixture(t)

	store := importStore(dir, &[]*database.BookFile{})
	store.CreateBookFileFunc = func(_ *database.BookFile) error {
		return os.ErrPermission
	}
	is := NewImportService(store)

	resp, err := is.ImportFile(&ImportFileRequest{FilePath: path})
	if err != nil {
		t.Fatalf("a book_file failure must not fail the import: %v", err)
	}
	if resp == nil || resp.ID == "" {
		t.Fatalf("the created book must still be returned, got %+v", resp)
	}
}
