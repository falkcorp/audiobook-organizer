// file: internal/importer/bookfile_on_import_test.go
// version: 1.0.0
// guid: 5c1e93a7-4f28-4b60-a8d3-e29b7c046f51
// last-edited: 2026-08-25

package importer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// stageFixture copies the repo's sample audiobook into a temp dir and registers
// that dir as the only allowed import path, so ValidateUserPath admits it.
//
// A real audio file, not a stub: ImportFile runs metadata.ExtractMetadata
// before it ever reaches CreateBook, so a zero-byte .m4b would fail out early
// and the test would pass for the wrong reason — never exercising the code
// under test at all.
func stageFixture(t *testing.T) (dir, path string) {
	t.Helper()
	src := filepath.Join("..", "..", "testdata", "fixtures", "test_sample.m4b")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Skipf("fixture %s unavailable: %v", src, err)
	}
	if len(data) == 0 {
		t.Fatalf("fixture %s is empty — it cannot exercise metadata extraction", src)
	}
	dir = t.TempDir()
	path = filepath.Join(dir, "sample-audiobook.m4b")
	if err := os.WriteFile(path, data, 0o600); err != nil {
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
