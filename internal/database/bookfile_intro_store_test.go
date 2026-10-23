// file: internal/database/bookfile_intro_store_test.go
// version: 1.0.0
// guid: 5d2f8b13-a7e4-4c90-96b1-e3c07a4d5f28
// last-edited: 2026-10-22

package database

import (
	"testing"
	"time"
)

func newIntroStoreFixture(t *testing.T) (*PebbleStore, *BookFile) {
	t.Helper()
	s, err := NewPebbleStoreInMemory("db")
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	book, err := s.CreateBook(&Book{Title: "Intro Store"})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	f := &BookFile{BookID: book.ID, FilePath: "/lib/Intro Store/01.m4b", Format: "m4b", Duration: 1800, FileHash: "hash-a"}
	if err := s.CreateBookFile(f); err != nil {
		t.Fatalf("CreateBookFile: %v", err)
	}
	rows, err := s.GetBookFiles(book.ID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("GetBookFiles: err=%v len=%d", err, len(rows))
	}
	return s, &rows[0]
}

func introOK(hash, run, text string) BookFileIntroResult {
	return BookFileIntroResult{
		SourceHash: hash, RunOpID: run, Status: "ok", Transcript: &text,
		Title: "Example Title", Author: "Example Author",
		Meta: &IntroMeta{Model: "model-x", Language: "en", ClipLengthSec: 90},
		At:   time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC), Done: true,
	}
}

func reread(t *testing.T, s *PebbleStore, f *BookFile) *BookFile {
	t.Helper()
	row, err := s.getBookFileByID(f.BookID, f.ID)
	if err != nil || row == nil {
		t.Fatalf("getBookFileByID: row=%v err=%v", row, err)
	}
	return row
}

func TestSetBookFileIntro_WritesGroupAndMarker(t *testing.T) {
	s, f := newIntroStoreFixture(t)
	res, err := s.SetBookFileIntro(f.BookID, f.ID, introOK("hash-a", "run-1", "Hello there."))
	if err != nil || res != IntroWriteWrote {
		t.Fatalf("res=%v err=%v", res, err)
	}
	got := reread(t, s, f)
	if got.IntroTranscription == nil || *got.IntroTranscription != "Hello there." {
		t.Fatalf("transcript = %v", got.IntroTranscription)
	}
	if got.IntroSourceHash != "hash-a" || got.IntroRunOpID != "run-1" {
		t.Fatalf("marker=%q run=%q", got.IntroSourceHash, got.IntroRunOpID)
	}
	if got.TranscribedTitle == nil || *got.TranscribedTitle != "Example Title" || got.TranscribedNarrator != nil {
		t.Fatalf("parsed fields wrong: %v / %v", got.TranscribedTitle, got.TranscribedNarrator)
	}
	if got.IntroMeta == nil || got.IntroMeta.Model != "model-x" {
		t.Fatalf("meta = %+v", got.IntroMeta)
	}
	// The memdb projection (what selection reads) carries the marker.
	cores, err := s.GetAllBookFilesCore()
	if err != nil {
		t.Fatalf("GetAllBookFilesCore: %v", err)
	}
	var found bool
	for _, c := range cores {
		if c.ID == f.ID {
			found = true
			if c.IntroSourceHash != "hash-a" || c.IntroRunOpID != "run-1" {
				t.Fatalf("memdb core marker=%q run=%q", c.IntroSourceHash, c.IntroRunOpID)
			}
		}
	}
	if !found {
		t.Fatal("file missing from memdb core")
	}
	// Identical repeat is a no-op.
	if res, err := s.SetBookFileIntro(f.BookID, f.ID, introOK("hash-a", "run-1", "Hello there.")); err != nil || res != IntroWriteSkipped {
		t.Fatalf("repeat: res=%v err=%v", res, err)
	}
}

func TestSetBookFileIntro_StaleWhenHashChanged(t *testing.T) {
	s, f := newIntroStoreFixture(t)
	res, err := s.SetBookFileIntro(f.BookID, f.ID, introOK("some-other-hash", "run-1", "x"))
	if err != nil || res != IntroWriteStale {
		t.Fatalf("res=%v err=%v", res, err)
	}
	if got := reread(t, s, f); got.IntroTranscription != nil || got.IntroSourceHash != "" {
		t.Fatalf("stale result was written: %+v", got)
	}
	if _, err := s.SetBookFileIntro(f.BookID, f.ID, BookFileIntroResult{}); err == nil {
		t.Fatal("empty source hash must be an error")
	}
	if res, err := s.SetBookFileIntro(f.BookID, "no-such-file", introOK("hash-a", "run-1", "x")); err != nil || res != IntroWriteStale {
		t.Fatalf("missing row: res=%v err=%v", res, err)
	}
}

func TestSetBookFileIntro_FillOnlyNeverOverwrites(t *testing.T) {
	s, f := newIntroStoreFixture(t)
	if _, err := s.SetBookFileIntro(f.BookID, f.ID, introOK("hash-a", "run-1", "first")); err != nil {
		t.Fatal(err)
	}
	second := introOK("hash-a", "run-2", "second")
	second.FillOnly = true
	res, err := s.SetBookFileIntro(f.BookID, f.ID, second)
	if err != nil || res != IntroWriteSkipped {
		t.Fatalf("res=%v err=%v", res, err)
	}
	got := reread(t, s, f)
	if *got.IntroTranscription != "first" || got.IntroRunOpID != "run-1" {
		t.Fatalf("fill-only overwrote: %q run=%q", *got.IntroTranscription, got.IntroRunOpID)
	}
	// A forced (non-fill-only) result does replace, and clears stale parsed fields.
	forced := introOK("hash-a", "run-3", "third")
	forced.Title, forced.Author = "", ""
	if res, err := s.SetBookFileIntro(f.BookID, f.ID, forced); err != nil || res != IntroWriteWrote {
		t.Fatalf("forced: res=%v err=%v", res, err)
	}
	got = reread(t, s, f)
	if *got.IntroTranscription != "third" || got.TranscribedTitle != nil || got.IntroRunOpID != "run-3" {
		t.Fatalf("forced result wrong: %+v", got)
	}
}

func TestSetBookFileIntro_FailureKeepsTranscriptAndIsChangeGuarded(t *testing.T) {
	s, f := newIntroStoreFixture(t)
	fail := BookFileIntroResult{SourceHash: "hash-a", RunOpID: "run-1", Status: "whisper_error", Error: "boom", At: time.Now()}
	if res, err := s.SetBookFileIntro(f.BookID, f.ID, fail); err != nil || res != IntroWriteWrote {
		t.Fatalf("first failure: res=%v err=%v", res, err)
	}
	got := reread(t, s, f)
	if got.IntroSourceHash != "" || got.IntroTranscription != nil {
		t.Fatalf("failure stamped marker or transcript: %+v", got)
	}
	if got.IntroRunOpID != "run-1" {
		t.Fatalf("failure on an empty row should name the run, got %q", got.IntroRunOpID)
	}
	if res, _ := s.SetBookFileIntro(f.BookID, f.ID, fail); res != IntroWriteSkipped {
		t.Fatalf("repeated identical failure should skip, got %v", res)
	}
	// A failure never takes over or wipes an earlier run's result.
	if _, err := s.SetBookFileIntro(f.BookID, f.ID, introOK("hash-a", "run-2", "kept")); err != nil {
		t.Fatal(err)
	}
	fail.RunOpID, fail.Error = "run-3", "boom again"
	if _, err := s.SetBookFileIntro(f.BookID, f.ID, fail); err != nil {
		t.Fatal(err)
	}
	got = reread(t, s, f)
	if got.IntroTranscription == nil || *got.IntroTranscription != "kept" || got.IntroRunOpID != "run-2" || got.IntroSourceHash != "hash-a" {
		t.Fatalf("failure damaged an earlier result: %+v", got)
	}
}

func TestClearBookFileIntro_ByRunID(t *testing.T) {
	s, f := newIntroStoreFixture(t)
	if _, err := s.SetBookFileIntro(f.BookID, f.ID, introOK("hash-a", "run-1", "text")); err != nil {
		t.Fatal(err)
	}
	if before, cleared, err := s.ClearBookFileIntro(f.BookID, f.ID, "run-other"); err != nil || cleared || before != nil {
		t.Fatalf("other run id: before=%v cleared=%v err=%v", before, cleared, err)
	}
	if got := reread(t, s, f); got.IntroTranscription == nil {
		t.Fatal("a different run id cleared the row")
	}
	before, cleared, err := s.ClearBookFileIntro(f.BookID, f.ID, "run-1")
	if err != nil || !cleared || before == nil || before.IntroTranscription == nil || *before.IntroTranscription != "text" {
		t.Fatalf("clear: before=%+v cleared=%v err=%v", before, cleared, err)
	}
	got := reread(t, s, f)
	if got.IntroTranscription != nil || got.IntroSourceHash != "" || got.IntroRunOpID != "" || got.IntroMeta != nil ||
		got.TranscribeStatus != nil || got.IntroTranscribedAt != nil || got.TranscribedTitle != nil {
		t.Fatalf("clear left residue: %+v", got)
	}
	if got.FileHash != "hash-a" || got.Duration != 1800 {
		t.Fatalf("clear touched unrelated fields: %+v", got)
	}
	// Second clear: nothing names the run any more.
	if _, cleared, _ := s.ClearBookFileIntro(f.BookID, f.ID, "run-1"); cleared {
		t.Fatal("second clear reported cleared")
	}
	if _, _, err := s.ClearBookFileIntro(f.BookID, f.ID, ""); err == nil {
		t.Fatal("empty run id must be an error")
	}
	// The memdb projection no longer carries the marker, so selection sees the file as undone.
	cores, _ := s.GetAllBookFilesCore()
	for _, c := range cores {
		if c.ID == f.ID && (c.IntroSourceHash != "" || c.IntroRunOpID != "") {
			t.Fatalf("memdb still marks file done: %+v", c)
		}
	}
}

func TestFileIntroStats_RoundTripAndSeparateKey(t *testing.T) {
	s, _ := newIntroStoreFixture(t)
	if got, err := s.GetFileIntroStats(); err != nil || got != nil {
		t.Fatalf("empty: %v %v", got, err)
	}
	if err := s.PutTranscribeStats(&TranscribeStats{RunOpID: "book-run"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutFileIntroStats(&FileIntroStats{RunOpID: "file-run", Eligible: 7, OK: 3}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetFileIntroStats()
	if err != nil || got == nil || got.RunOpID != "file-run" || got.Eligible != 7 || got.OK != 3 {
		t.Fatalf("got %+v err=%v", got, err)
	}
	book, _ := s.GetTranscribeStats()
	if book == nil || book.RunOpID != "book-run" {
		t.Fatalf("book stats were overwritten: %+v", book)
	}
}
