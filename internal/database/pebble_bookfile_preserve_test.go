// file: internal/database/pebble_bookfile_preserve_test.go
// version: 1.3.0
// guid: 7e1a9c43-2b86-4d05-9f71-3c6e8a0d2b54
// last-edited: 2026-07-06

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

// UpsertBookFile must NOT wipe the raw AcoustID fingerprint when the incoming
// row carries an empty fingerprint (PERF-7). This is the same footgun as the
// BatchUpsertBookFiles variant: a caller reads a BookFile from memdb (stripped),
// sets some non-fingerprint fields, then calls UpsertBookFile — without the
// preserve guard, the nil AcoustIDFingerprint overwrites the real value in Pebble.
func TestUpsertBookFile_PreservesFingerprintOnEmptyIncoming(t *testing.T) {
	s, err := NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer s.Close()

	book, err := s.CreateBook(&Book{Title: "Upsert FP Book"})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	path := "/lib/Upsert FP Book/01.m4b"
	detail := "checksum mismatch"
	if err := s.CreateBookFile(&BookFile{
		BookID:                    book.ID,
		FilePath:                  path,
		FileHash:                  "cafebabe",
		Duration:                  7200,
		AcoustIDFingerprint:       []byte{10, 20, 30, 40},
		FingerprintFailureReason:  nil,
		FingerprintFailureDetail:  &detail,
		FingerprintDiagnosticJSON: strPtr(`{"codec":"mp3"}`),
	}); err != nil {
		t.Fatalf("CreateBookFile: %v", err)
	}

	// Simulate a memdb-sourced caller: fingerprint fields are nil/empty, but
	// non-fingerprint metadata fields have new values to write.
	if err := s.UpsertBookFile(&BookFile{
		BookID:   book.ID,
		FilePath: path,
		FileHash: "cafebabe",
		Duration: 7200,
		RawTags:  map[string]string{"ALBUM": "Upsert FP Book", "TRACKNUMBER": "1"},
	}); err != nil {
		t.Fatalf("UpsertBookFile: %v", err)
	}

	files, err := s.GetBookFiles(book.ID)
	if err != nil || len(files) != 1 {
		t.Fatalf("GetBookFiles: err=%v len=%d", err, len(files))
	}
	got := files[0]
	if string(got.AcoustIDFingerprint) != string([]byte{10, 20, 30, 40}) {
		t.Errorf("AcoustIDFingerprint WIPED by UpsertBookFile: got %v", got.AcoustIDFingerprint)
	}
	if got.FingerprintFailureDetail == nil || *got.FingerprintFailureDetail != detail {
		t.Errorf("FingerprintFailureDetail not preserved: %v", got.FingerprintFailureDetail)
	}
	if got.FingerprintDiagnosticJSON == nil {
		t.Error("FingerprintDiagnosticJSON not preserved")
	}
	if len(got.RawTags) != 2 {
		t.Errorf("RawTags not written: %v", got.RawTags)
	}
}

// A genuine fingerprint write via UpsertBookFile must still overwrite — the
// preserve guard fires only when the incoming value is empty.
func TestUpsertBookFile_OverwritesFingerprintWhenProvided(t *testing.T) {
	s, err := NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer s.Close()

	book, _ := s.CreateBook(&Book{Title: "Upsert FP Book 2"})
	path := "/lib/Upsert FP Book 2/01.m4b"
	if err := s.CreateBookFile(&BookFile{BookID: book.ID, FilePath: path, AcoustIDFingerprint: []byte{1, 1, 1}}); err != nil {
		t.Fatalf("CreateBookFile: %v", err)
	}
	if err := s.UpsertBookFile(&BookFile{
		BookID: book.ID, FilePath: path, AcoustIDFingerprint: []byte{9, 9, 9, 9},
	}); err != nil {
		t.Fatalf("UpsertBookFile: %v", err)
	}
	files, _ := s.GetBookFiles(book.ID)
	if len(files) != 1 || string(files[0].AcoustIDFingerprint) != string([]byte{9, 9, 9, 9}) {
		t.Errorf("fresh fingerprint not written: %v", files[0].AcoustIDFingerprint)
	}
}

// UpsertBookFile's iTunes PID lookup path must also preserve fingerprint data.
func TestUpsertBookFile_PreservesFingerprintViaPIDLookup(t *testing.T) {
	s, err := NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer s.Close()

	book, _ := s.CreateBook(&Book{Title: "iTunes FP Book"})
	pid := "PID123ABC"
	if err := s.CreateBookFile(&BookFile{
		BookID:              book.ID,
		FilePath:            "/lib/iTunes FP Book/01.m4b",
		ITunesPersistentID:  pid,
		AcoustIDFingerprint: []byte{5, 6, 7, 8},
	}); err != nil {
		t.Fatalf("CreateBookFile: %v", err)
	}

	// Incoming via PID with no fingerprint.
	if err := s.UpsertBookFile(&BookFile{
		BookID:             book.ID,
		FilePath:           "/lib/iTunes FP Book/01.m4b",
		ITunesPersistentID: pid,
		Duration:           1800,
	}); err != nil {
		t.Fatalf("UpsertBookFile via PID: %v", err)
	}

	files, _ := s.GetBookFiles(book.ID)
	if len(files) != 1 || string(files[0].AcoustIDFingerprint) != string([]byte{5, 6, 7, 8}) {
		t.Errorf("AcoustIDFingerprint WIPED via PID path: got %v", files[0].AcoustIDFingerprint)
	}
}

// UpdateBookFile must NOT wipe the raw AcoustID fingerprint when a caller writes
// back a memdb-slim struct (AcoustIDFingerprint nil'd by stripBookFileForMemdb).
// This is the recompute_itunes_paths / enrich_book_files / fix_book_file_paths /
// repair_missing_files footgun: those jobs read via GetAllBookFiles (memdb view),
// tweak one unrelated field, and write the whole struct back via UpdateBookFile —
// without the preserve-on-empty guard the nil fingerprint erases the stored value.
func TestUpdateBookFile_PreservesFingerprintOnEmptyIncoming(t *testing.T) {
	s, err := NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer s.Close()

	book, err := s.CreateBook(&Book{Title: "Update FP Book"})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	path := "/lib/Update FP Book/01.m4b"
	if err := s.CreateBookFile(&BookFile{
		BookID:              book.ID,
		FilePath:            path,
		FileHash:            "feedface",
		Duration:            5400,
		AcoustIDFingerprint: []byte{11, 22, 33, 44},
	}); err != nil {
		t.Fatalf("CreateBookFile: %v", err)
	}
	created, err := s.GetBookFiles(book.ID)
	if err != nil || len(created) != 1 {
		t.Fatalf("GetBookFiles(created): err=%v len=%d", err, len(created))
	}
	id := created[0].ID

	// Simulate a memdb-sourced maintenance write: fingerprint nil, an unrelated
	// field (ITunesPath) changed. This is exactly what recompute_itunes_paths feeds.
	slim := &BookFile{
		ID:         id,
		BookID:     book.ID,
		FilePath:   path,
		FileHash:   "feedface",
		Duration:   5400,
		ITunesPath: "/Music/Update FP Book/01.m4b",
	}
	if err := s.UpdateBookFile(id, slim); err != nil {
		t.Fatalf("UpdateBookFile: %v", err)
	}

	got, err := s.GetBookFiles(book.ID) // pebble-direct → stored row
	if err != nil || len(got) != 1 {
		t.Fatalf("GetBookFiles: err=%v len=%d", err, len(got))
	}
	if string(got[0].AcoustIDFingerprint) != string([]byte{11, 22, 33, 44}) {
		t.Errorf("AcoustIDFingerprint WIPED by UpdateBookFile: got %v, want [11 22 33 44]", got[0].AcoustIDFingerprint)
	}
	if got[0].ITunesPath != "/Music/Update FP Book/01.m4b" {
		t.Errorf("ITunesPath not written: %q", got[0].ITunesPath)
	}
}

// The other direction: UpdateBookFile is the fingerprint WRITE path
// (internal/plugins/acoustid/backfill.go), which supplies a fresh non-empty
// AcoustIDFingerprint AND nil'd diagnostic fields to clear a prior failure on
// success. The guard must fire ONLY on empty incoming, so a genuine write still
// overwrites the fingerprint and the intentional failure-diagnostic clear survives.
// (This is the regression the guard could have introduced if it mirrored
// UpsertBookFile's 4-field guard — it must not.)
func TestUpdateBookFile_WritesFreshFingerprintAndClearsFailureDiagnostics(t *testing.T) {
	s, err := NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer s.Close()

	book, _ := s.CreateBook(&Book{Title: "Update FP Book 2"})
	path := "/lib/Update FP Book 2/01.m4b"
	reason := "fpcalc_missing"
	detail := "binary not on PATH"
	if err := s.CreateBookFile(&BookFile{
		BookID:                   book.ID,
		FilePath:                 path,
		FingerprintFailureReason: &reason, // a prior failure tombstone
		FingerprintFailureDetail: &detail,
	}); err != nil {
		t.Fatalf("CreateBookFile: %v", err)
	}
	created, _ := s.GetBookFiles(book.ID)
	id := created[0].ID

	// Mimic backfill.go's success path: fresh non-empty fingerprint, diagnostics nil.
	if err := s.UpdateBookFile(id, &BookFile{
		ID:                       id,
		BookID:                   book.ID,
		FilePath:                 path,
		AcoustIDFingerprint:      []byte{7, 7, 7, 7},
		FingerprintFailureReason: nil,
		FingerprintFailureDetail: nil,
	}); err != nil {
		t.Fatalf("UpdateBookFile: %v", err)
	}

	got, _ := s.GetBookFiles(book.ID)
	if len(got) != 1 || string(got[0].AcoustIDFingerprint) != string([]byte{7, 7, 7, 7}) {
		t.Errorf("fresh fingerprint not written: %v", got[0].AcoustIDFingerprint)
	}
	if got[0].FingerprintFailureReason != nil {
		t.Errorf("failure reason NOT cleared on success (guard over-fired): %v", *got[0].FingerprintFailureReason)
	}
	if got[0].FingerprintFailureDetail != nil {
		t.Errorf("failure detail NOT cleared on success (guard over-fired): %v", *got[0].FingerprintFailureDetail)
	}
}

// strPtr is a local test helper — returns a pointer to the given string.
func strPtr(s string) *string { return &s }

// BatchUpsertBookFiles must refresh the memdb view so batch-written rows are
// immediately visible to memdb-backed reads (GetAllBookFiles / the UI). Without
// the post-commit UpsertBookFileToMemDB, the row would be absent from memdb until
// the next warmup — the tag-backfill non-convergence bug.
func TestBatchUpsertBookFiles_RefreshesMemDB(t *testing.T) {
	s, err := NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer s.Close()
	if !s.UseMemDB {
		t.Skip("memdb disabled")
	}
	book, err := s.CreateBook(&Book{Title: "MemDB Book"})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	path := "/lib/MemDB Book/01.mp3"
	if err := s.BatchUpsertBookFiles([]*BookFile{{
		BookID: book.ID, FilePath: path, RawTags: map[string]string{"ALBUM": "X"},
	}}); err != nil {
		t.Fatalf("BatchUpsertBookFiles: %v", err)
	}
	all, err := s.GetAllBookFiles() // memdb-backed view
	if err != nil {
		t.Fatalf("GetAllBookFiles: %v", err)
	}
	found := false
	for i := range all {
		if all[i].FilePath == path {
			found = true
			if len(all[i].RawTags) == 0 {
				t.Error("RawTags missing from memdb view after batch write")
			}
		}
	}
	if !found {
		t.Error("batch-written file not visible via GetAllBookFiles — memdb not refreshed")
	}
}
