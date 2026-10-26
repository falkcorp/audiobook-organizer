// file: internal/database/bookfile_intro_provenance_test.go
// version: 1.1.0
// guid: 3f8a2c6e-91d4-4b7a-a5e0-6d2b4c8f1e73
// last-edited: 2026-10-25

package database

import (
	"testing"
	"time"
)

// The per-file intro group is written by exactly one op
// (maintenance.transcribe-file-intros) and read back as its resume state: a
// file is done when IntroSourceHash == FileHash, and a run is reversible
// through IntroRunOpID. A library rescan hands the upserts a bare row, so the
// whole group (the original seven fields, the transcript, and the provenance
// fields added with the op) must survive it.
//
// bookfile_rescan_preserve_test.go already proves this for every BookFile field
// by reflection. These tests name the intro group on purpose, so a regression
// reads as "a rescan wiped the intro results" rather than as an entry in a
// generic field list, and they pin the two behaviours the op depends on that
// the generic test does not: a changed file drops the done marker, and the
// memdb projection carries the marker but not the heavy members.

func introSeed(t *testing.T, s *PebbleStore) (*BookFile, string) {
	t.Helper()
	book, err := s.CreateBook(&Book{Title: "Intro Provenance"})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	str := func(v string) *string { return &v }
	seed := &BookFile{
		BookID:                 book.ID,
		FilePath:               "/lib/Intro Provenance/01.m4b",
		Format:                 "m4b",
		Codec:                  "aac",
		Duration:               3600,
		FileHash:               "hash-of-the-bytes",
		OriginalFileHash:       "hash-of-the-bytes",
		OriginalFileHashKind:   FileHashKindSampled,
		IntroTranscription:     str("Example Title by Example Author, read by Example Reader."),
		TranscribedTitle:       str("Example Title"),
		TranscribedAuthor:      str("Example Author"),
		TranscribedNarrator:    str("Example Reader"),
		TranscribedTranslator:  str("Example Translator"),
		TranscribedCoverArtist: str("Example Artist"),
		IntroTranscribedAt:     &at,
		TranscribeStatus:       str("ok"),
		TranscribeError:        str("none"),
		TranscribeAttemptedAt:  &at,
		IntroSourceHash:        "hash-of-the-bytes",
		IntroRunOpID:           "op-run-1",
		IntroTimeouts:          1,
		IntroTimeoutHash:       "hash-of-the-bytes",
		IntroMeta:              &IntroMeta{Model: "model-x", Language: "en", ClipStartSec: 0, ClipLengthSec: 90},
	}
	if err := s.CreateBookFile(seed); err != nil {
		t.Fatalf("CreateBookFile: %v", err)
	}
	got, err := s.GetBookFiles(book.ID)
	if err != nil || len(got) != 1 {
		t.Fatalf("GetBookFiles: err=%v len=%d", err, len(got))
	}
	return &got[0], book.ID
}

func assertIntroGroupKept(t *testing.T, want, got *BookFile) {
	t.Helper()
	strEq := func(name string, a, b *string) {
		if a == nil || b == nil || *a != *b {
			t.Errorf("%s was not kept: want %v, got %v", name, a, b)
		}
	}
	strEq("IntroTranscription", want.IntroTranscription, got.IntroTranscription)
	strEq("TranscribedTitle", want.TranscribedTitle, got.TranscribedTitle)
	strEq("TranscribedAuthor", want.TranscribedAuthor, got.TranscribedAuthor)
	strEq("TranscribedNarrator", want.TranscribedNarrator, got.TranscribedNarrator)
	strEq("TranscribedTranslator", want.TranscribedTranslator, got.TranscribedTranslator)
	strEq("TranscribedCoverArtist", want.TranscribedCoverArtist, got.TranscribedCoverArtist)
	strEq("TranscribeStatus", want.TranscribeStatus, got.TranscribeStatus)
	strEq("TranscribeError", want.TranscribeError, got.TranscribeError)
	if got.IntroTranscribedAt == nil || !got.IntroTranscribedAt.Equal(*want.IntroTranscribedAt) {
		t.Errorf("IntroTranscribedAt was not kept: want %v, got %v", want.IntroTranscribedAt, got.IntroTranscribedAt)
	}
	if got.TranscribeAttemptedAt == nil || !got.TranscribeAttemptedAt.Equal(*want.TranscribeAttemptedAt) {
		t.Errorf("TranscribeAttemptedAt was not kept: want %v, got %v", want.TranscribeAttemptedAt, got.TranscribeAttemptedAt)
	}
	if got.IntroSourceHash != want.IntroSourceHash {
		t.Errorf("IntroSourceHash (the done marker) was not kept: want %q, got %q", want.IntroSourceHash, got.IntroSourceHash)
	}
	if got.IntroRunOpID != want.IntroRunOpID {
		t.Errorf("IntroRunOpID was not kept: want %q, got %q", want.IntroRunOpID, got.IntroRunOpID)
	}
	if got.IntroTimeouts != want.IntroTimeouts || got.IntroTimeoutHash != want.IntroTimeoutHash {
		t.Errorf("timeout strikes were not kept: want %d/%q, got %d/%q",
			want.IntroTimeouts, want.IntroTimeoutHash, got.IntroTimeouts, got.IntroTimeoutHash)
	}
	if got.IntroMeta == nil || *got.IntroMeta != *want.IntroMeta {
		t.Errorf("IntroMeta was not kept: want %+v, got %+v", want.IntroMeta, got.IntroMeta)
	}
}

// scanRow is what the scanner builds for a file it re-reads: the same bytes
// (same FileHash), nothing about transcription.
func scanRow(bookID string, before *BookFile) *BookFile {
	return &BookFile{
		BookID:           bookID,
		FilePath:         before.FilePath,
		OriginalFilename: "01.m4b",
		Format:           "m4b",
		FileSize:         4242,
		FileHash:         before.FileHash,
	}
}

func TestRescan_KeepsPerFileIntroGroup(t *testing.T) {
	paths := map[string]func(s *PebbleStore, row *BookFile) error{
		"BatchUpsertBookFiles": func(s *PebbleStore, row *BookFile) error {
			return s.BatchUpsertBookFiles([]*BookFile{row})
		},
		"BatchUpsertScannedBookFiles": func(s *PebbleStore, row *BookFile) error {
			return s.BatchUpsertScannedBookFiles([]ScannedBookFile{{File: row, Present: true}})
		},
		"UpsertBookFile": func(s *PebbleStore, row *BookFile) error {
			return s.UpsertBookFile(row)
		},
	}
	for name, write := range paths {
		t.Run(name, func(t *testing.T) {
			s, err := NewPebbleStoreInMemory("db")
			if err != nil {
				t.Fatalf("NewPebbleStore: %v", err)
			}
			defer s.Close()
			before, bookID := introSeed(t, s)
			if err := write(s, scanRow(bookID, before)); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			after, err := s.GetBookFiles(bookID)
			if err != nil || len(after) != 1 {
				t.Fatalf("GetBookFiles: err=%v len=%d", err, len(after))
			}
			assertIntroGroupKept(t, before, &after[0])
		})
	}
}

// A file replaced outside the app (new bytes, different length) must not keep
// the old file's done marker: the op has to see it as not done and transcribe
// the new audio. The transcript and its provenance go with the marker.
func TestRescan_ReplacedFileDropsPerFileIntroGroup(t *testing.T) {
	s, err := NewPebbleStoreInMemory("db")
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer s.Close()
	before, bookID := introSeed(t, s)

	row := scanRow(bookID, before)
	row.FileHash = "hash-of-different-bytes"
	row.Duration = 7200       // a different recording, not a re-wrap of the same audio
	row.FileSize = 90_000_000 // a plausible size for it, so the duration is not read as milliseconds
	if err := s.BatchUpsertBookFiles([]*BookFile{row}); err != nil {
		t.Fatalf("BatchUpsertBookFiles: %v", err)
	}
	after, err := s.GetBookFiles(bookID)
	if err != nil || len(after) != 1 {
		t.Fatalf("GetBookFiles: err=%v len=%d", err, len(after))
	}
	got := after[0]
	if got.IntroSourceHash == got.FileHash {
		t.Errorf("a replaced file still reads as done: IntroSourceHash=%q FileHash=%q", got.IntroSourceHash, got.FileHash)
	}
	if got.IntroTranscription != nil || got.IntroMeta != nil || got.IntroRunOpID != "" {
		t.Errorf("the old file's transcript/provenance survived the replacement: transcript=%v meta=%+v run=%q",
			got.IntroTranscription, got.IntroMeta, got.IntroRunOpID)
	}
	if got.IntroTimeouts != 0 || got.IntroTimeoutHash != "" {
		t.Errorf("the old file's timeout strikes survived the replacement: %d/%q", got.IntroTimeouts, got.IntroTimeoutHash)
	}
}

// A job that read the slim memdb projection (no IntroMeta, no transcript) and
// wrote it back through UpdateBookFile must not blank the memdb-stripped pair.
func TestUpdateBookFile_SlimRoundTripKeepsIntroMeta(t *testing.T) {
	s, err := NewPebbleStoreInMemory("db")
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer s.Close()
	before, bookID := introSeed(t, s)

	slim := stripBookFileForMemdb(before)
	slim.TrackNumber = 9
	if err := s.UpdateBookFile(slim.ID, slim); err != nil {
		t.Fatalf("UpdateBookFile: %v", err)
	}
	after, err := s.GetBookFiles(bookID)
	if err != nil || len(after) != 1 {
		t.Fatalf("GetBookFiles: err=%v len=%d", err, len(after))
	}
	assertIntroGroupKept(t, before, &after[0])
}

// The memdb projection drops the heavy members and keeps the two small fields
// work selection and clear-by-run-id read from BookFileCore.
func TestStripBookFileForMemdb_IntroProvenance(t *testing.T) {
	src := &BookFile{
		ID: "bf-1", FileHash: "h", IntroSourceHash: "h", IntroRunOpID: "op-1",
		IntroMeta: &IntroMeta{Model: "m", ClipLengthSec: 90},
	}
	stripped := stripBookFileForMemdb(src)
	if stripped.IntroMeta != nil {
		t.Errorf("IntroMeta must be stripped from memdb, got %+v", stripped.IntroMeta)
	}
	core := stripped.Core()
	if core.IntroSourceHash != "h" || core.IntroRunOpID != "op-1" {
		t.Errorf("Core must carry the done marker and run id, got %q / %q", core.IntroSourceHash, core.IntroRunOpID)
	}
	if src.IntroMeta == nil {
		t.Error("source mutated: IntroMeta nil on src")
	}
}
