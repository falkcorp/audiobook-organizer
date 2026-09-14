// file: internal/fileops/write_tags_safe_rescan_test.go
// version: 1.0.2
// guid: 9d4a2c61-7e38-4b05-a1f6-52c8e0b7d493
// last-edited: 2026-09-13

package fileops

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/filehash"
)

// B1: a tag write-back followed by a library rescan must not cost the file its
// fingerprint, transcript or duration. WriteTagsSafe records the new canonical
// FileHash, so the scanner-shaped upsert that follows sees the same hash and
// the audio-derived data survives.
func TestWriteTagsSafe_ThenScannerRescan_KeepsAudioDerivedData(t *testing.T) {
	s, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer s.Close()

	path := filepath.Join(t.TempDir(), "01.m4b")
	if err := os.WriteFile(path, []byte("OLD-TAG-HEADER|identical-audio-payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	firstHash, err := filehash.BookFileHash(path)
	if err != nil {
		t.Fatal(err)
	}

	book, err := s.CreateBook(&database.Book{Title: "Write Then Rescan"})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	intro, status := "This is a reading of Write Then Rescan.", "ok"
	if err := s.CreateBookFile(&database.BookFile{
		BookID:              book.ID,
		FilePath:            path,
		FileHash:            firstHash,
		OriginalFileHash:    firstHash,
		Duration:            3600,
		Codec:               "aac",
		AcoustIDFingerprint: []byte{9, 8, 7, 6},
		IntroTranscription:  &intro,
		TranscribeStatus:    &status,
		RawTags:             map[string]string{"TITLE": "old"},
	}); err != nil {
		t.Fatalf("CreateBookFile: %v", err)
	}
	seeded, err := s.GetBookFiles(book.ID)
	if err != nil || len(seeded) != 1 {
		t.Fatalf("GetBookFiles: err=%v len=%d", err, len(seeded))
	}
	fileID := seeded[0].ID

	// The write-back: new tag bytes, same audio.
	if _, _, err := WriteTagsSafe(path, func(tmp string) error {
		return os.WriteFile(tmp, []byte("NEW-LONGER-TAG-HEADER|identical-audio-payload"), 0o644)
	}, WriteTagsSafeOptions{BookFileID: fileID, Store: s}); err != nil {
		t.Fatalf("WriteTagsSafe: %v", err)
	}
	newHash, err := filehash.BookFileHash(path)
	if err != nil {
		t.Fatal(err)
	}
	if newHash == firstHash {
		t.Fatal("fixture error: the write did not change the bytes")
	}
	// The book_file_hash: secondary index must follow the new hash. The raw
	// db.Set that UpdateBookFileHashes used before never refreshed it.
	if byHash, herr := s.GetBookBySegmentFileHash(newHash); herr != nil || byHash == nil || byHash.ID != book.ID {
		t.Errorf("GetBookBySegmentFileHash(new hash) = %v, %v; want book %s (hash index not refreshed)", byHash, herr, book.ID)
	}

	// The rescan, shaped exactly like createBookFilesForBook's row.
	row := &database.BookFile{
		ID:               "01RESCANFRESHULID0000000000",
		BookID:           book.ID,
		FilePath:         path,
		OriginalFilename: "01.m4b",
		Format:           "m4b",
		FileSize:         int64(len("NEW-LONGER-TAG-HEADER|identical-audio-payload")),
		TrackNumber:      1,
		FileHash:         newHash,
		OriginalFileHash: newHash,
		RawTags:          map[string]string{"TITLE": "new"},
	}
	if err := s.BatchUpsertScannedBookFiles([]database.ScannedBookFile{{File: row, Present: true}}); err != nil {
		t.Fatalf("BatchUpsertScannedBookFiles: %v", err)
	}

	got, err := s.GetBookFiles(book.ID)
	if err != nil || len(got) != 1 {
		t.Fatalf("GetBookFiles: err=%v len=%d", err, len(got))
	}
	after := got[0]
	if after.FileHash != newHash {
		t.Errorf("FileHash = %q, want the new bytes' hash %q", after.FileHash, newHash)
	}
	if after.OriginalFileHash != firstHash || after.OriginalFileHashKind != database.FileHashKindSampled {
		t.Errorf("OriginalFileHash = %q/%q, want the first-seen sampled %q", after.OriginalFileHash, after.OriginalFileHashKind, firstHash)
	}
	if string(after.AcoustIDFingerprint) != string([]byte{9, 8, 7, 6}) {
		t.Errorf("fingerprint lost after write-back + rescan: %v", after.AcoustIDFingerprint)
	}
	if after.IntroTranscription == nil || *after.IntroTranscription != intro {
		t.Errorf("transcript lost after write-back + rescan: %v", after.IntroTranscription)
	}
	if after.TranscribeStatus == nil || *after.TranscribeStatus != status {
		t.Errorf("transcribe status lost after write-back + rescan: %v", after.TranscribeStatus)
	}
	if after.Duration != 3600 || after.Codec != "aac" {
		t.Errorf("media info lost after write-back + rescan: duration=%d codec=%q", after.Duration, after.Codec)
	}
	if after.RawTags["TITLE"] != "new" {
		t.Errorf("RawTags = %v, want the rescan's new tags", after.RawTags)
	}
	if after.NeedsRescan != nil && *after.NeedsRescan {
		t.Error("hash matched after the write-back, so the row must not be marked NeedsRescan")
	}
}
