// file: internal/importer/service_torrent_hash_test.go
// version: 1.1.0
// guid: 7b0f6d2e-3a41-4c85-9e17-5d8a2c4b1f90
// last-edited: 2026-10-10

package importer

import (
	"errors"
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

	resp, err := is.ImportFile(&ImportFileRequest{FilePath: path, TorrentHash: testTorrentHash})
	if err != nil {
		t.Fatalf("import failed: %v", err)
	}
	if !resp.TorrentLinked || resp.TorrentLinkNote != "" {
		t.Errorf("want TorrentLinked with no note, got linked=%v note=%q", resp.TorrentLinked, resp.TorrentLinkNote)
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

	resp, err := is.ImportFile(&ImportFileRequest{FilePath: path})
	if err != nil {
		t.Fatalf("import failed: %v", err)
	}
	if resp.TorrentLinked || resp.TorrentLinkNote != "" {
		t.Errorf("no hash given: want unlinked with no note, got linked=%v note=%q", resp.TorrentLinked, resp.TorrentLinkNote)
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
		// MD5 length: not an infohash (v1 is SHA-1, v2 is SHA-256).
		"32 chars": testTorrentHash[:32],
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

// A purged or blocked fingerprint is a logged warning, never an import
// failure, and the purged torrent is never linked: no version, no DelugeHash
// on the file row, and the response says so.
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
	if created != 1 {
		t.Errorf("the book is still created exactly once, got %d CreateBook calls", created)
	}
	if len(rows) != 1 {
		t.Fatalf("the book_file row is still written, got %d", len(rows))
	}
	if rows[0].DelugeHash != "" {
		t.Errorf("purged content must not be linked: DelugeHash = %q", rows[0].DelugeHash)
	}
	if resp.TorrentLinked {
		t.Error("purged content must not be reported as linked")
	}
	if resp.TorrentLinkNote != TorrentLinkNotePurged {
		t.Errorf("TorrentLinkNote = %q, want %q", resp.TorrentLinkNote, TorrentLinkNotePurged)
	}
}

// The version carrying the hash was created but the file row was not: the link
// is incomplete, so it must not be reported as linked.
func TestImportFile_TorrentHashFileRowFailureIsNotLinked(t *testing.T) {
	withSupportedExt(t)
	dir, path := stageFixture(t)

	var rows []*database.BookFile
	var vers []*database.BookVersion
	var created int
	st := torrentStore(dir, &rows, &vers, &created)
	st.CreateBookFileFunc = func(*database.BookFile) error { return errors.New("synthetic write failure") }
	is := NewImportService(st)

	resp, err := is.ImportFile(&ImportFileRequest{FilePath: path, TorrentHash: testTorrentHash})
	if err != nil {
		t.Fatalf("a book_file failure must not fail the import: %v", err)
	}
	if len(vers) != 1 {
		t.Fatalf("control: the version is still created, got %d", len(vers))
	}
	if resp.TorrentLinked {
		t.Error("no book_file row was written, so the torrent must not be reported linked")
	}
	if resp.TorrentLinkNote != TorrentLinkNoteFileFailed {
		t.Errorf("TorrentLinkNote = %q, want %q", resp.TorrentLinkNote, TorrentLinkNoteFileFailed)
	}
}

// The version could not be created: neither half of the link exists, so the
// file row must not carry the hash on its own.
func TestImportFile_TorrentHashVersionFailureDropsFileHash(t *testing.T) {
	withSupportedExt(t)
	dir, path := stageFixture(t)

	var rows []*database.BookFile
	var vers []*database.BookVersion
	var created int
	st := torrentStore(dir, &rows, &vers, &created)
	st.CreateBookVersionFunc = func(*database.BookVersion) (*database.BookVersion, error) {
		return nil, errors.New("synthetic version failure")
	}
	is := NewImportService(st)

	resp, err := is.ImportFile(&ImportFileRequest{FilePath: path, TorrentHash: testTorrentHash})
	if err != nil {
		t.Fatalf("a version failure must not fail the import: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("the book_file row is still written, got %d", len(rows))
	}
	if rows[0].DelugeHash != "" {
		t.Errorf("DelugeHash = %q, want empty when no version carries the hash", rows[0].DelugeHash)
	}
	if resp.TorrentLinked || resp.TorrentLinkNote != TorrentLinkNoteVersionFailed {
		t.Errorf("want unlinked with note %q, got linked=%v note=%q", TorrentLinkNoteVersionFailed, resp.TorrentLinked, resp.TorrentLinkNote)
	}
}
