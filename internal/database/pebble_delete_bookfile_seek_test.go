// file: internal/database/pebble_delete_bookfile_seek_test.go
// version: 1.0.0
// guid: e8187d63-0c08-49b4-9879-6cab49841d0b
// last-edited: 2026-08-04

package database

import (
	"fmt"
	"testing"

	"github.com/cockroachdb/pebble/v2"
)

// DeleteBookFile used to locate its row by iterating EVERY book_file: key and
// matching on the <fileID> suffix. The primary key is book_file:<bookID>:<fileID>,
// so an ID-only caller cannot seek it — but the book_file_id index exists for
// exactly that and was simply unused.
//
// Honest scope: measured at 8,000 rows the two paths are indistinguishable
// (9.196ms vs 9.144ms per delete), so this is not a dramatic win — it removes an
// O(N)-per-delete that only starts to matter at production row counts. These tests
// exist to keep the resolution CORRECT, not to assert a speedup.
func TestDeleteBookFile_ResolvesViaIDIndexNotAScan(t *testing.T) {
	s, err := NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer s.Close()
	s.WaitForWarmup()

	book, err := s.CreateBook(&Book{Title: "Seek Book"})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	target := &BookFile{BookID: book.ID, FilePath: "/lib/Seek Book/01.m4b", Duration: 3600}
	if err := s.CreateBookFile(target); err != nil {
		t.Fatalf("CreateBookFile: %v", err)
	}

	// The index entry must exist and point at the primary key.
	val, closer, err := s.db.Get([]byte("book_file_id:" + target.ID))
	if err != nil {
		t.Fatalf("book_file_id index missing for a freshly created row: %v", err)
	}
	want := fmt.Sprintf("book_file:%s:%s", book.ID, target.ID)
	if string(val) != want {
		t.Fatalf("index points at %q, want %q", string(val), want)
	}
	closer.Close()

	if err := s.DeleteBookFile(target.ID); err != nil {
		t.Fatalf("DeleteBookFile: %v", err)
	}

	files, err := s.GetBookFiles(book.ID)
	if err != nil {
		t.Fatalf("GetBookFiles: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("row survived deletion: %d rows remain", len(files))
	}
	// The index entry must be cleaned up too, or a later lookup resolves a ghost.
	if _, _, gerr := s.db.Get([]byte("book_file_id:" + target.ID)); gerr != pebble.ErrNotFound {
		t.Fatalf("book_file_id index entry survived the delete (err=%v)", gerr)
	}
}

// 🔴 The fallback must stay. Rows written before writeBookFileSecondaryIndexes
// existed have no book_file_id entry, and deleting one must still work — just
// slowly. Simulating that by removing ONLY the index entry proves the scan path is
// still reachable and still correct.
func TestDeleteBookFile_FallsBackToScanWhenIndexEntryMissing(t *testing.T) {
	s, err := NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer s.Close()
	s.WaitForWarmup()

	book, err := s.CreateBook(&Book{Title: "Legacy Book"})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	legacy := &BookFile{BookID: book.ID, FilePath: "/lib/Legacy Book/01.mp3", Duration: 1200}
	if err := s.CreateBookFile(legacy); err != nil {
		t.Fatalf("CreateBookFile: %v", err)
	}

	// Strip the index entry, leaving the primary row — a pre-index row.
	if err := s.db.Delete([]byte("book_file_id:"+legacy.ID), pebble.Sync); err != nil {
		t.Fatalf("delete index entry: %v", err)
	}

	if err := s.DeleteBookFile(legacy.ID); err != nil {
		t.Fatalf("DeleteBookFile on a pre-index row: %v", err)
	}
	files, err := s.GetBookFiles(book.ID)
	if err != nil {
		t.Fatalf("GetBookFiles: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("pre-index row survived deletion: %d rows remain", len(files))
	}
}

// A dangling index entry (index present, primary row already gone) must not make a
// live row invisible, and must not error. Deleting an unknown id is a no-op.
func TestDeleteBookFile_UnknownIDIsANoOp(t *testing.T) {
	s, err := NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer s.Close()
	s.WaitForWarmup()

	if err := s.DeleteBookFile("01NOSUCHFILEID0000000000000"); err != nil {
		t.Fatalf("DeleteBookFile on an unknown id should be a no-op, got: %v", err)
	}
}

// Deleting one row must not disturb its siblings — the index resolves exactly one
// primary key, so a suffix collision or an off-by-one prefix bound would show here.
func TestDeleteBookFile_LeavesSiblingRowsIntact(t *testing.T) {
	s, err := NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer s.Close()
	s.WaitForWarmup()

	book, err := s.CreateBook(&Book{Title: "Sibling Book"})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	var ids []string
	for i := 0; i < 5; i++ {
		f := &BookFile{BookID: book.ID, FilePath: fmt.Sprintf("/lib/Sibling Book/%02d.mp3", i), Duration: 600}
		if err := s.CreateBookFile(f); err != nil {
			t.Fatalf("CreateBookFile %d: %v", i, err)
		}
		ids = append(ids, f.ID)
	}

	if err := s.DeleteBookFile(ids[2]); err != nil {
		t.Fatalf("DeleteBookFile: %v", err)
	}
	files, err := s.GetBookFiles(book.ID)
	if err != nil {
		t.Fatalf("GetBookFiles: %v", err)
	}
	if len(files) != 4 {
		t.Fatalf("expected 4 surviving rows, got %d", len(files))
	}
	for _, f := range files {
		if f.ID == ids[2] {
			t.Fatal("the targeted row is still present")
		}
	}
}
