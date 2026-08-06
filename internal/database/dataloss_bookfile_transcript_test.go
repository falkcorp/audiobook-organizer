// file: internal/database/dataloss_bookfile_transcript_test.go
// version: 1.0.0
// guid: 6c1e94a7-8f20-4d63-b5a1-9e07c2f8d4b6
// last-edited: 2026-08-06

package database

import (
	"testing"
)

// The per-file intro transcript is memdb-stripped (stripBookFileForMemdb nils
// IntroTranscription), which puts it in this repo's dominant data-loss shape: a
// whole-library job reads a SLIM BookFile, edits an unrelated field, and writes
// the struct back — silently blanking a transcript that costs a Whisper run to
// regenerate.
//
// TestRoundTrip_UpdateBookFile does NOT cover this. It writes a FULLY-POPULATED
// struct, so every field is non-nil on the way in and the preserve-on-nil branch
// is never exercised. These tests drive the nil-incoming case specifically, once
// per write path, because the three paths guard independently:
//
//	UpdateBookFile        — guards against `old`
//	UpsertBookFile        — guards against `existing`, then delegates to UpdateBookFile
//	BatchUpsertBookFiles  — guards against `existing` and writes STRAIGHT into the
//	                        batch (no UpdateBookFile delegation), so its guard is
//	                        the only protection on that path.

const dlTranscript = "Overlord, Book 7, by Kugane Maruyama, read by Emily Woo Zeller."

// seedTranscribedFile creates a book + one BookFile carrying a stored transcript.
func seedTranscribedFile(t *testing.T, ps *PebbleStore) (bookID string, file *BookFile) {
	t.Helper()
	book, err := ps.CreateBook(&Book{Title: "dl-transcript-book", FilePath: "/lib/dl-transcript.m4b"})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	tr := dlTranscript
	f := &BookFile{
		BookID:             book.ID,
		FilePath:           "/lib/dl-transcript/01.mp3",
		IntroTranscription: &tr,
	}
	if err := ps.CreateBookFile(f); err != nil {
		t.Fatalf("CreateBookFile: %v", err)
	}
	return book.ID, f
}

// readBackTranscript fetches the stored file and returns its transcript.
func readBackTranscript(t *testing.T, ps *PebbleStore, bookID, fileID string) *string {
	t.Helper()
	files, err := ps.GetBookFiles(bookID)
	if err != nil {
		t.Fatalf("GetBookFiles: %v", err)
	}
	for i := range files {
		if files[i].ID == fileID {
			return files[i].IntroTranscription
		}
	}
	t.Fatalf("book file %s not found", fileID)
	return nil
}

// assertTranscriptSurvived fails with the data-loss framing when the guard leaks.
func assertTranscriptSurvived(t *testing.T, got *string, path string) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s WIPED the stored intro transcript on a nil-incoming write "+
			"(memdb round-trip data loss); the preserve-on-nil guard is missing or broken", path)
	}
	if *got != dlTranscript {
		t.Errorf("%s: transcript = %q, want %q", path, *got, dlTranscript)
	}
}

// TestUpdateBookFile_PreservesTranscriptOnNilIncoming simulates the exact bug
// shape: read a slim (memdb-stripped) row, change an unrelated field, write back.
func TestUpdateBookFile_PreservesTranscriptOnNilIncoming(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	ps := store.(*PebbleStore)

	bookID, seed := seedTranscribedFile(t, ps)

	// A slim row exactly as GetAllBookFiles would hand it back.
	slim := stripBookFileForMemdb(seed)
	if slim.IntroTranscription != nil {
		t.Fatal("stripBookFileForMemdb must nil IntroTranscription — the strip is the premise of this test")
	}
	slim.TrackNumber = 42 // the unrelated edit the maintenance job makes

	if err := ps.UpdateBookFile(seed.ID, slim); err != nil {
		t.Fatalf("UpdateBookFile: %v", err)
	}
	assertTranscriptSurvived(t, readBackTranscript(t, ps, bookID, seed.ID), "UpdateBookFile")
}

// TestUpsertBookFile_PreservesTranscriptOnNilIncoming covers the path-matched
// upsert, which guards once itself and again via UpdateBookFile.
func TestUpsertBookFile_PreservesTranscriptOnNilIncoming(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	ps := store.(*PebbleStore)

	bookID, seed := seedTranscribedFile(t, ps)

	slim := stripBookFileForMemdb(seed)
	slim.TrackNumber = 42

	if err := ps.UpsertBookFile(slim); err != nil {
		t.Fatalf("UpsertBookFile: %v", err)
	}
	assertTranscriptSurvived(t, readBackTranscript(t, ps, bookID, seed.ID), "UpsertBookFile")
}

// TestBatchUpsertBookFiles_PreservesTranscriptOnNilIncoming covers the batch
// path. This is the highest-blast-radius one: it writes directly into the Pebble
// batch, so a missing guard wipes every transcript in the library in one commit.
func TestBatchUpsertBookFiles_PreservesTranscriptOnNilIncoming(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	ps := store.(*PebbleStore)

	bookID, seed := seedTranscribedFile(t, ps)

	slim := stripBookFileForMemdb(seed)
	slim.TrackNumber = 42

	if err := ps.BatchUpsertBookFiles([]*BookFile{slim}); err != nil {
		t.Fatalf("BatchUpsertBookFiles: %v", err)
	}
	assertTranscriptSurvived(t, readBackTranscript(t, ps, bookID, seed.ID), "BatchUpsertBookFiles")
}

// TestUpdateBookFile_StillWritesANewTranscript is the counterweight: the guard
// must not become a write-block. A real (re)transcription supplies a non-nil
// value and MUST overwrite the stored one.
func TestUpdateBookFile_StillWritesANewTranscript(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	ps := store.(*PebbleStore)

	bookID, seed := seedTranscribedFile(t, ps)

	updated := *seed
	fresh := "This part includes Chapter 2."
	updated.IntroTranscription = &fresh

	if err := ps.UpdateBookFile(seed.ID, &updated); err != nil {
		t.Fatalf("UpdateBookFile: %v", err)
	}
	got := readBackTranscript(t, ps, bookID, seed.ID)
	if got == nil || *got != fresh {
		t.Errorf("a fresh non-nil transcript must overwrite the stored one: got %v, want %q", got, fresh)
	}
}
