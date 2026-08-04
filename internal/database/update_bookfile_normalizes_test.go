// file: internal/database/update_bookfile_normalizes_test.go
// version: 1.0.0
// guid: 1ba802ee-d7e8-478f-a5e3-a82dbef6ac2c
// last-edited: 2026-08-04

package database

import "testing"

// 🔴 UpdateBookFile was the LAST write path that did not normalise Duration to the
// stored standard (seconds). CreateBookFile, UpsertBookFile and BatchUpsertBookFiles
// all called normalizeBookFileDuration; an update did not, so it could reintroduce
// the millisecond corruption those three exist to prevent.
//
// That is not theoretical: production held ~6,000 millisecond rows, and a book
// reading 9,906h was 34 rows of milliseconds (9,906h/1000 ≈ 9.9h — a real audiobook).
func TestUpdateBookFile_NormalizesMillisecondDurationToSeconds(t *testing.T) {
	s, err := NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer s.Close()
	s.WaitForWarmup()

	book, err := s.CreateBook(&Book{Title: "Unit Book"})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	// 1,048,000 bytes at 1,048,000 "seconds" implies 8 bits/sec — impossible. Read as
	// milliseconds it is 1,048s ≈ 8 kbps, an ordinary spoken-word MP3.
	f := &BookFile{BookID: book.ID, FilePath: "/lib/Unit Book/01.mp3", Duration: 1048, FileSize: 1048000}
	if err := s.CreateBookFile(f); err != nil {
		t.Fatalf("CreateBookFile: %v", err)
	}

	// Now push a millisecond value through UPDATE, the path that used to accept it.
	f.Duration = 1048000
	if err := s.UpdateBookFile(f.ID, f); err != nil {
		t.Fatalf("UpdateBookFile: %v", err)
	}

	got, err := s.GetBookFiles(book.ID)
	if err != nil {
		t.Fatalf("GetBookFiles: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 row, got %d", len(got))
	}
	if got[0].Duration != 1048 {
		t.Fatalf("stored duration = %d, want 1048 — UpdateBookFile must normalise "+
			"milliseconds to seconds like every other write path", got[0].Duration)
	}
}

// 🔑 The guard must be inert on correct data. A plausible duration is never touched,
// so this can be applied unconditionally on every update without risk.
func TestUpdateBookFile_LeavesPlausibleDurationsAlone(t *testing.T) {
	s, err := NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer s.Close()
	s.WaitForWarmup()

	book, err := s.CreateBook(&Book{Title: "Sane Book"})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	// 3600s at 58MB ≈ 129 kbps — an ordinary audiobook.
	f := &BookFile{BookID: book.ID, FilePath: "/lib/Sane Book/01.m4b", Duration: 3600, FileSize: 58_000_000}
	if err := s.CreateBookFile(f); err != nil {
		t.Fatalf("CreateBookFile: %v", err)
	}

	f.Title = "touched"
	if err := s.UpdateBookFile(f.ID, f); err != nil {
		t.Fatalf("UpdateBookFile: %v", err)
	}
	got, _ := s.GetBookFiles(book.ID)
	if got[0].Duration != 3600 {
		t.Fatalf("duration = %d, want 3600 untouched — the guard must not fire on good data",
			got[0].Duration)
	}
}

// 🔴 IDEMPOTENCE. An already-repaired row must never be divided a second time; that
// would turn a correct 9,906-second book into 9 seconds. Updating twice must be a
// no-op after the first conversion.
func TestUpdateBookFile_NormalizationIsIdempotent(t *testing.T) {
	s, err := NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer s.Close()
	s.WaitForWarmup()

	book, err := s.CreateBook(&Book{Title: "Idem Book"})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	f := &BookFile{BookID: book.ID, FilePath: "/lib/Idem Book/01.mp3", Duration: 1048, FileSize: 1048000}
	if err := s.CreateBookFile(f); err != nil {
		t.Fatalf("CreateBookFile: %v", err)
	}

	f.Duration = 1048000 // ms
	if err := s.UpdateBookFile(f.ID, f); err != nil {
		t.Fatalf("UpdateBookFile #1: %v", err)
	}
	after1, _ := s.GetBookFiles(book.ID)
	row := after1[0]

	if err := s.UpdateBookFile(row.ID, &row); err != nil {
		t.Fatalf("UpdateBookFile #2: %v", err)
	}
	after2, _ := s.GetBookFiles(book.ID)
	if after2[0].Duration != 1048 {
		t.Fatalf("duration = %d after a second update, want 1048 — the conversion "+
			"must not compound", after2[0].Duration)
	}
}

// The guard must not wipe the fingerprint it travels alongside. UpdateBookFile writes
// the whole struct, and this repo has already shipped a fingerprint-wipe bug once.
func TestUpdateBookFile_NormalizationPreservesFingerprint(t *testing.T) {
	s, err := NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer s.Close()
	s.WaitForWarmup()

	book, err := s.CreateBook(&Book{Title: "FP Book"})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	f := &BookFile{
		BookID: book.ID, FilePath: "/lib/FP Book/01.mp3",
		Duration: 1048, FileSize: 1048000,
		AcoustIDFingerprint: []byte{0x01, 0x02, 0x03, 0x04},
	}
	if err := s.CreateBookFile(f); err != nil {
		t.Fatalf("CreateBookFile: %v", err)
	}

	f.Duration = 1048000
	if err := s.UpdateBookFile(f.ID, f); err != nil {
		t.Fatalf("UpdateBookFile: %v", err)
	}
	got, _ := s.GetBookFiles(book.ID)
	if got[0].Duration != 1048 {
		t.Fatalf("duration = %d, want 1048", got[0].Duration)
	}
	if len(got[0].AcoustIDFingerprint) != 4 {
		t.Fatalf("fingerprint lost during a normalising update: %v", got[0].AcoustIDFingerprint)
	}
}
