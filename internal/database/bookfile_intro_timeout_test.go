// file: internal/database/bookfile_intro_timeout_test.go
// version: 1.0.0
// guid: 3a7d1f58-c2e6-4b09-8d41-6f0b9e2a7c35
// last-edited: 2026-10-25

package database

import (
	"testing"
	"time"
)

func strike(hash, run string, limit int) IntroTimeoutStrike {
	return IntroTimeoutStrike{
		SourceHash: hash, RunOpID: run, Limit: limit,
		Detail: "transcribe_timeout: no result within 90s",
		At:     time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC),
	}
}

func TestRecordIntroTimeout_FirstStrikeIsCountedWithoutLabellingTheFileAFailure(t *testing.T) {
	s, f := newIntroStoreFixture(t)
	out, err := s.RecordIntroTimeout(f.BookID, f.ID, strike("hash-a", "run-1", 2))
	if err != nil || out.Strikes != 1 || out.Permanent || out.Skipped {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	got := reread(t, s, f)
	if got.IntroTimeouts != 1 || got.IntroTimeoutHash != "hash-a" {
		t.Fatalf("strikes=%d hash=%q", got.IntroTimeouts, got.IntroTimeoutHash)
	}
	if got.TranscribeStatus != nil {
		t.Errorf("a first strike must not write a status (it may be a transient stall): %q", *got.TranscribeStatus)
	}
	if got.TranscribeAttemptedAt == nil {
		t.Errorf("attempt time not recorded")
	}
}

func TestRecordIntroTimeout_SecondStrikeMakesItPermanentAndSurvivesTheMemdbProjection(t *testing.T) {
	s, f := newIntroStoreFixture(t)
	if _, err := s.RecordIntroTimeout(f.BookID, f.ID, strike("hash-a", "run-1", 2)); err != nil {
		t.Fatal(err)
	}
	out, err := s.RecordIntroTimeout(f.BookID, f.ID, strike("hash-a", "run-2", 2))
	if err != nil || out.Strikes != 2 || !out.Permanent {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	got := reread(t, s, f)
	if got.TranscribeStatus == nil || *got.TranscribeStatus != IntroStatusTranscribeTimeout {
		t.Fatalf("status = %v", got.TranscribeStatus)
	}
	if got.TranscribeError == nil || got.IntroRunOpID != "run-2" {
		t.Errorf("error=%v run=%q", got.TranscribeError, got.IntroRunOpID)
	}
	// Selection reads the Core projection: it must carry the verdict.
	cores, err := s.GetAllBookFilesCore()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cores {
		if c.ID == f.ID {
			if c.TranscribeStatus == nil || *c.TranscribeStatus != IntroStatusTranscribeTimeout {
				t.Fatalf("core status = %v", c.TranscribeStatus)
			}
			if c.HasIntroResult() {
				t.Errorf("a timeout verdict is not a result")
			}
			return
		}
	}
	t.Fatal("file missing from core projection")
}

func TestRecordIntroTimeout_ReplacedFileStartsAtZero(t *testing.T) {
	s, f := newIntroStoreFixture(t)
	if _, err := s.RecordIntroTimeout(f.BookID, f.ID, strike("hash-a", "run-1", 2)); err != nil {
		t.Fatal(err)
	}
	// The file is replaced: new bytes, new hash (the strikes were counted
	// against hash-a). Rescan/replace behaviour of the stored strikes is covered
	// in bookfile_intro_provenance_test.go.
	row := reread(t, s, f)
	row.FileHash = "hash-b"
	if err := s.UpdateBookFile(f.ID, row); err != nil {
		t.Fatalf("UpdateBookFile: %v", err)
	}
	out, err := s.RecordIntroTimeout(f.BookID, f.ID, strike("hash-b", "run-2", 2))
	if err != nil || out.Strikes != 1 || out.Permanent {
		t.Fatalf("a replaced file must restart its strikes: out=%+v err=%v", out, err)
	}
}

func TestRecordIntroTimeout_StaleHashAndMissingRowWriteNothing(t *testing.T) {
	s, f := newIntroStoreFixture(t)
	out, err := s.RecordIntroTimeout(f.BookID, f.ID, strike("some-other-hash", "run-1", 2))
	if err != nil || !out.Stale || !out.Skipped {
		t.Fatalf("stale hash: out=%+v err=%v", out, err)
	}
	if got := reread(t, s, f); got.IntroTimeouts != 0 {
		t.Errorf("stale strike was written")
	}
	out, err = s.RecordIntroTimeout(f.BookID, "no-such-file", strike("hash-a", "run-1", 2))
	if err != nil || !out.Stale {
		t.Fatalf("missing row: out=%+v err=%v", out, err)
	}
}

func TestRecordIntroTimeout_NeverOverwritesATranscript(t *testing.T) {
	s, f := newIntroStoreFixture(t)
	if _, err := s.SetBookFileIntro(f.BookID, f.ID, introOK("hash-a", "run-1", "real words")); err != nil {
		t.Fatal(err)
	}
	out, err := s.RecordIntroTimeout(f.BookID, f.ID, strike("hash-a", "run-2", 1))
	if err != nil || !out.Skipped || out.Permanent {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	got := reread(t, s, f)
	if got.TranscribeStatus == nil || *got.TranscribeStatus != "ok" || got.IntroTimeouts != 0 || got.IntroRunOpID != "run-1" {
		t.Errorf("a file with a transcript was relabelled: status=%v strikes=%d run=%q", got.TranscribeStatus, got.IntroTimeouts, got.IntroRunOpID)
	}
}

func TestRecordIntroTimeout_ValidatesInput(t *testing.T) {
	s, f := newIntroStoreFixture(t)
	if _, err := s.RecordIntroTimeout(f.BookID, f.ID, strike("", "run-1", 2)); err == nil {
		t.Error("empty hash must be an error")
	}
	if _, err := s.RecordIntroTimeout(f.BookID, f.ID, strike("hash-a", "run-1", 0)); err == nil {
		t.Error("limit < 1 must be an error")
	}
}

func TestRecordIntroTimeout_ClearByRunRemovesTheVerdictAndTheStrikes(t *testing.T) {
	s, f := newIntroStoreFixture(t)
	if _, err := s.RecordIntroTimeout(f.BookID, f.ID, strike("hash-a", "run-1", 1)); err != nil {
		t.Fatal(err)
	}
	if _, cleared, err := s.ClearBookFileIntro(f.BookID, f.ID, "run-1"); err != nil || !cleared {
		t.Fatalf("cleared=%v err=%v", cleared, err)
	}
	got := reread(t, s, f)
	if got.TranscribeStatus != nil || got.IntroTimeouts != 0 || got.IntroTimeoutHash != "" {
		t.Errorf("clear left timeout state: status=%v strikes=%d", got.TranscribeStatus, got.IntroTimeouts)
	}
}

func TestRecordIntroTimeout_ARealTranscriptResetsStrikes(t *testing.T) {
	s, f := newIntroStoreFixture(t)
	if _, err := s.RecordIntroTimeout(f.BookID, f.ID, strike("hash-a", "run-1", 3)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetBookFileIntro(f.BookID, f.ID, introOK("hash-a", "run-2", "now it decodes")); err != nil {
		t.Fatal(err)
	}
	if got := reread(t, s, f); got.IntroTimeouts != 0 || got.IntroTimeoutHash != "" {
		t.Errorf("strikes survived a successful transcript: %d", got.IntroTimeouts)
	}
}
