// file: internal/database/set_book_file_hash_index_test.go
// version: 1.0.0
// guid: 1d6f3b82-9a47-4e0c-b5d8-2c7e9f4a1b63
// last-edited: 2026-09-13

package database

import "testing"

// Review round 5, item 4: SetBookFileHash wrote the row with a raw db.Set, so
// the book_file_hash: and book_file_orig_hash: indexes and the memdb
// projection never saw the hash, and GetBookBySegmentFileHash missed every
// hash backfill-file-hashes or extract-wav-clips recorded.
func TestSetBookFileHash_UpdatesHashIndex(t *testing.T) {
	s := openRescanStore(t)
	book, err := s.CreateBook(&Book{Title: "Backfill"})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	if err := s.CreateBookFile(&BookFile{BookID: book.ID, FilePath: "/lib/S/01.m4b"}); err != nil {
		t.Fatalf("CreateBookFile: %v", err)
	}
	f := onlyBookFile(t, s, book.ID)

	if err := s.SetBookFileHash(f.ID, "sampled-new"); err != nil {
		t.Fatalf("SetBookFileHash: %v", err)
	}
	got, err := s.GetBookBySegmentFileHash("sampled-new")
	if err != nil {
		t.Fatalf("GetBookBySegmentFileHash: %v", err)
	}
	if got == nil || got.ID != book.ID {
		t.Fatalf("GetBookBySegmentFileHash = %v, want book %s: the hash index was not written", got, book.ID)
	}

	row, err := s.GetBookFileByID(book.ID, f.ID)
	if err != nil || row == nil {
		t.Fatalf("GetBookFileByID: row=%v err=%v", row, err)
	}
	if row.FileHash != "sampled-new" || row.OriginalFileHash != "sampled-new" {
		t.Errorf("hashes = %q/%q, want sampled-new for both (original was empty)", row.FileHash, row.OriginalFileHash)
	}
}
