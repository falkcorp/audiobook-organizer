// file: internal/tagger/safe_write_hash_test.go
// version: 1.1.0
// guid: 3d7f1a92-6b4e-4c85-a0d3-e29b8c5f7146
// last-edited: 2026-09-13

package tagger

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// pathRecorder is a fileops.BookFileHashRecorder over a fixed path -> row map.
type pathRecorder struct {
	rows    map[string]string // path -> book_file ID
	lookups []string
}

func (r *pathRecorder) GetBookFileByPath(p string) (*database.BookFile, error) {
	r.lookups = append(r.lookups, p)
	if id, ok := r.rows[p]; ok {
		return &database.BookFile{ID: id, FilePath: p}, nil
	}
	return nil, nil
}

func (r *pathRecorder) UpdateBookFileHashes(string, string, string, string) error { return nil }

// TestHashOptions_RedirectRecordsOnLibraryCopyRow pins the one rule every
// tagger write follows: hashes go on the row found at the path actually
// written. A protected-path redirect writes the library copy, so the row is
// looked up there. In production ImportToLibrary has repointed the caller's
// row to the copy, so the lookup returns that same row; the fixture gives the
// copy's path a different row ID only so the test can tell a lookup at the
// written path from reuse of the caller's ID. Before this rule tagger recorded
// nothing on a redirect while the native taglib writer recorded at the
// resolved path, so the two build variants disagreed.
func TestHashOptions_RedirectRecordsOnLibraryCopyRow(t *testing.T) {
	t.Parallel()
	const src, dst = "/deluge/books/a.m4b", "/library/books/a.m4b"
	rec := &pathRecorder{rows: map[string]string{src: "bf-src", dst: "bf-copy"}}
	deps := SafeWriteDeps{BookFileID: "bf-src", HashStore: rec}

	got := deps.hashOptions(src, dst)
	if got.BookFileID != "bf-copy" {
		t.Fatalf("redirected write recorded on %q; want the library copy's row %q", got.BookFileID, "bf-copy")
	}
	if got.Store == nil {
		t.Fatal("redirected write has no Store; its hashes would not be recorded")
	}
}

// TestHashOptions_RedirectWithoutCopyRowRecordsNothing is a contract guard,
// not a case production reaches: ImportToLibrary always repoints the row to
// the copy, so a redirected write finds it. It pins what hashOptions does if an
// importer ever leaves the copy without a row: nothing is recorded, and the
// row the caller named is not used in its place.
func TestHashOptions_RedirectWithoutCopyRowRecordsNothing(t *testing.T) {
	t.Parallel()
	rec := &pathRecorder{rows: map[string]string{"/deluge/b.m4b": "bf-src"}}
	deps := SafeWriteDeps{BookFileID: "bf-src", HashStore: rec}

	if got := deps.hashOptions("/deluge/b.m4b", "/library/b.m4b"); got.BookFileID != "" {
		t.Fatalf("recorded on %q; want nothing (the copy has no row, the source is unchanged)", got.BookFileID)
	}
}

// TestHashOptions_KnownRowSkipsLookup: with no redirect a caller-supplied row
// is used as is, and no by-path lookup is made.
func TestHashOptions_KnownRowSkipsLookup(t *testing.T) {
	t.Parallel()
	rec := &pathRecorder{rows: map[string]string{}}
	deps := SafeWriteDeps{BookFileID: "bf-known", HashStore: rec}

	got := deps.hashOptions("/library/c.m4b", "/library/c.m4b")
	if got.BookFileID != "bf-known" || got.Store == nil {
		t.Fatalf("got %+v; want the known row bf-known with a store", got)
	}
	if len(rec.lookups) != 0 {
		t.Fatalf("looked up %v; want no lookup when the row is known", rec.lookups)
	}
}

// TestHashOptions_StoreOnlyLooksUpWrittenPath: a caller that knows only the
// path gets the row at that path.
func TestHashOptions_StoreOnlyLooksUpWrittenPath(t *testing.T) {
	t.Parallel()
	rec := &pathRecorder{rows: map[string]string{"/library/d.m4b": "bf-d"}}
	deps := SafeWriteDeps{HashStore: rec}

	if got := deps.hashOptions("/library/d.m4b", "/library/d.m4b"); got.BookFileID != "bf-d" {
		t.Fatalf("recorded on %q; want bf-d", got.BookFileID)
	}
}

// TestHashOptions_NoStoreRecordsNothing: zero deps keep the plain write.
func TestHashOptions_NoStoreRecordsNothing(t *testing.T) {
	t.Parallel()
	deps := SafeWriteDeps{BookFileID: "bf-e"}
	if got := deps.hashOptions("/x.m4b", "/x.m4b"); got.BookFileID != "" || got.Store != nil {
		t.Fatalf("got %+v; want zero options without a store", got)
	}
}
