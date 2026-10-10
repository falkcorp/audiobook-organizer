// file: internal/importer/service_torrent_hash_test.go
// version: 1.0.0
// guid: 7b0f6d2e-3a41-4c85-9e17-5d8a2c4b1f90
// last-edited: 2026-10-10

package importer

import (
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

const testTorrentHash = "0123456789abcdef0123456789abcdef01234567"

// torrentStore is importStore plus capture of the created BookVersion(s) and a
// count of CreateBook calls.
func torrentStore(dir string, rows *[]*database.BookFile, vers *[]*database.BookVersion, createBooks *int) *database.MockStore {
	st := importStore(dir, rows)
	inner := st.CreateBookFunc
	st.CreateBookFunc = func(b *database.Book) (*database.Book, error) {
		*createBooks++
		return inner(b)
	}
	st.CreateBookVersionFunc = func(v *database.BookVersion) (*database.BookVersion, error) {
		cp := *v
		cp.ID = "01JIMPORTEDVERSION00000"
		*vers = append(*vers, &cp)
		return &cp, nil
	}
	return st
}

func TestImportFile_TorrentHashReachesVersionAndBookFile(t *testing.T) {
	withSupportedExt(t)
	dir, path := stageFixture(t)

	var rows []*database.BookFile
	var vers []*database.BookVersion
	var created int
	is := NewImportService(torrentStore(dir, &rows, &vers, &created))

	if _, err := is.ImportFile(&ImportFileRequest{FilePath: path, TorrentHash: testTorrentHash}); err != nil {
		t.Fatalf("import failed: %v", err)
	}
	if len(vers) != 1 {
		t.Fatalf("want 1 version, got %d", len(vers))
	}
	if vers[0].TorrentHash != testTorrentHash {
		t.Errorf("version TorrentHash = %q, want %q", vers[0].TorrentHash, testTorrentHash)
	}
	if vers[0].Source != "deluge" {
		t.Errorf("version Source = %q, want deluge", vers[0].Source)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 book_file row, got %d", len(rows))
	}
	bf := rows[0]
	if bf.DelugeHash != testTorrentHash {
		t.Errorf("DelugeHash = %q, want %q", bf.DelugeHash, testTorrentHash)
	}
	if bf.ImportedFromDelugeAt != nil {
		t.Errorf("ImportedFromDelugeAt must stay nil (the file has not been copied), got %v", bf.ImportedFromDelugeAt)
	}
	if bf.DelugeOriginalPath != "" {
		t.Errorf("DelugeOriginalPath must stay empty, got %q", bf.DelugeOriginalPath)
	}
	if bf.DownloadHash != "" {
		t.Errorf("DownloadHash must stay empty, got %q", bf.DownloadHash)
	}
}

func TestImportFile_NoTorrentHashIsUnchanged(t *testing.T) {
	withSupportedExt(t)
	dir, path := stageFixture(t)

	var rows []*database.BookFile
	var vers []*database.BookVersion
	var created int
	is := NewImportService(torrentStore(dir, &rows, &vers, &created))

	if _, err := is.ImportFile(&ImportFileRequest{FilePath: path}); err != nil {
		t.Fatalf("import failed: %v", err)
	}
	if len(vers) != 1 || vers[0].Source != "imported" || vers[0].TorrentHash != "" {
		t.Errorf("want one imported version with no hash, got %+v", vers)
	}
	if len(rows) != 1 || rows[0].DelugeHash != "" {
		t.Errorf("want one book_file with empty DelugeHash, got %+v", rows)
	}
}

func TestImportFile_RejectsMalformedTorrentHash(t *testing.T) {
	withSupportedExt(t)
	dir, path := stageFixture(t)

	cases := map[string]string{
		"non-hex":    "zz",
		"upper-case": strings.ToUpper(testTorrentHash),
		"41 chars":   testTorrentHash + "a",
		"spaces":     strings.Repeat(" ", 40),
		"39 chars":   testTorrentHash[:39],
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			var rows []*database.BookFile
			var vers []*database.BookVersion
			var created int
			is := NewImportService(torrentStore(dir, &rows, &vers, &created))

			if _, err := is.ImportFile(&ImportFileRequest{FilePath: path, TorrentHash: h}); err == nil {
				t.Fatalf("hash %q must be rejected", h)
			}
			if created != 0 || len(rows) != 0 || len(vers) != 0 {
				t.Errorf("a rejected hash must write nothing: CreateBook=%d rows=%d versions=%d", created, len(rows), len(vers))
			}
		})
	}
}

func TestImportFile_AcceptsSha256Length(t *testing.T) {
	withSupportedExt(t)
	dir, path := stageFixture(t)

	var rows []*database.BookFile
	var vers []*database.BookVersion
	var created int
	is := NewImportService(torrentStore(dir, &rows, &vers, &created))
	h := strings.Repeat("ab", 32)
	if _, err := is.ImportFile(&ImportFileRequest{FilePath: path, TorrentHash: h}); err != nil {
		t.Fatalf("64-hex hash must be accepted: %v", err)
	}
	if rows[0].DelugeHash != h {
		t.Errorf("DelugeHash = %q", rows[0].DelugeHash)
	}
}

// Pins current behaviour: a purged-fingerprint refusal from CreateIngestVersion
// is a logged warning, never an import failure.
func TestImportFile_PurgedFingerprintDoesNotFailImport(t *testing.T) {
	withSupportedExt(t)
	dir, path := stageFixture(t)

	var rows []*database.BookFile
	var vers []*database.BookVersion
	var created int
	st := torrentStore(dir, &rows, &vers, &created)
	st.GetBookVersionByTorrentHashFunc = func(hash string) (*database.BookVersion, error) {
		return &database.BookVersion{ID: "v-old", BookID: "b-old", Status: database.BookVersionStatusInactivePurged, TorrentHash: hash}, nil
	}
	is := NewImportService(st)

	resp, err := is.ImportFile(&ImportFileRequest{FilePath: path, TorrentHash: testTorrentHash})
	if err != nil {
		t.Fatalf("a fingerprint refusal must not fail the import: %v", err)
	}
	if resp == nil || resp.ID == "" {
		t.Fatalf("the created book must be returned, got %+v", resp)
	}
	if len(vers) != 0 {
		t.Errorf("the refused version must not be created, got %d", len(vers))
	}
	if len(rows) != 1 {
		t.Errorf("the book_file row is still written, got %d", len(rows))
	}
}
