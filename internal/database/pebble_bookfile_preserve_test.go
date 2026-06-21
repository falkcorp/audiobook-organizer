// file: internal/database/pebble_bookfile_preserve_test.go
// version: 1.0.0
// guid: 7e1a9c43-2b86-4d05-9f71-3c6e8a0d2b54
// last-edited: 2026-06-21

package database

import (
	"testing"
)

// BatchUpsertBookFiles must NOT wipe the raw AcoustID fingerprint when the
// incoming row leaves it empty. This is the maintenance.tag-backfill footgun:
// that op sources rows from the memdb view (GetAllBookFiles → stripBookFileForMemdb,
// which nils AcoustIDFingerprint) and writes them back via BatchUpsertBookFiles.
// Without the preserve-on-empty guard the whole-library backfill would erase the
// ~275K-fingerprint library. GetBookFiles is pebble-direct so it reflects the
// actually-stored row, not the stripped memdb copy.
func TestBatchUpsertBookFiles_PreservesFingerprintOnEmptyIncoming(t *testing.T) {
	s, err := NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer s.Close()

	book, err := s.CreateBook(&Book{Title: "FP Book"})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	path := "/lib/FP Book/01.m4b"
	reason := "corrupt_audio"
	if err := s.CreateBookFile(&BookFile{
		BookID:                   book.ID,
		FilePath:                 path,
		FileHash:                 "deadbeef",
		Duration:                 3600,
		AcoustIDFingerprint:      []byte{1, 2, 3, 4, 5},
		FingerprintFailureReason: &reason,
	}); err != nil {
		t.Fatalf("CreateBookFile: %v", err)
	}

	// Simulate a memdb-sourced backfill write: the row carries the fields memdb
	// PRESERVES (FileHash, Duration) plus the new RawTags, but the fields memdb
	// STRIPS (AcoustIDFingerprint, fingerprint diagnostics) are empty — exactly
	// what tag-backfill feeds in.
	if err := s.BatchUpsertBookFiles([]*BookFile{{
		BookID:   book.ID,
		FilePath: path,
		FileHash: "deadbeef",
		Duration: 3600,
		RawTags:  map[string]string{"ALBUM": "FP Book", "TRACKNUMBER": "1"},
	}}); err != nil {
		t.Fatalf("BatchUpsertBookFiles: %v", err)
	}

	files, err := s.GetBookFiles(book.ID) // pebble-direct → the stored row
	if err != nil || len(files) != 1 {
		t.Fatalf("GetBookFiles: err=%v len=%d", err, len(files))
	}
	got := files[0]
	if len(got.RawTags) != 2 {
		t.Errorf("RawTags not written: %v", got.RawTags)
	}
	if string(got.AcoustIDFingerprint) != string([]byte{1, 2, 3, 4, 5}) {
		t.Errorf("AcoustIDFingerprint WIPED: got %v, want [1 2 3 4 5]", got.AcoustIDFingerprint)
	}
	if got.FingerprintFailureReason == nil || *got.FingerprintFailureReason != reason {
		t.Errorf("FingerprintFailureReason not preserved: %v", got.FingerprintFailureReason)
	}
	if got.FileHash != "deadbeef" {
		t.Errorf("FileHash not preserved: %q", got.FileHash)
	}
}

// A legitimate fingerprint WRITE (non-empty incoming) must still overwrite —
// the preserve guard only fires when the incoming value is empty.
func TestBatchUpsertBookFiles_OverwritesFingerprintWhenProvided(t *testing.T) {
	s, err := NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer s.Close()

	book, _ := s.CreateBook(&Book{Title: "FP Book 2"})
	path := "/lib/FP Book 2/01.m4b"
	if err := s.CreateBookFile(&BookFile{BookID: book.ID, FilePath: path, AcoustIDFingerprint: []byte{1, 1, 1}}); err != nil {
		t.Fatalf("CreateBookFile: %v", err)
	}
	if err := s.BatchUpsertBookFiles([]*BookFile{{
		BookID: book.ID, FilePath: path, AcoustIDFingerprint: []byte{9, 9, 9, 9},
	}}); err != nil {
		t.Fatalf("BatchUpsertBookFiles: %v", err)
	}
	files, _ := s.GetBookFiles(book.ID)
	if len(files) != 1 || string(files[0].AcoustIDFingerprint) != string([]byte{9, 9, 9, 9}) {
		t.Errorf("fresh fingerprint not written: %v", files[0].AcoustIDFingerprint)
	}
}
