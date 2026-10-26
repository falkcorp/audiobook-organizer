// file: internal/database/bookfile_intro_timeout_test.go
// version: 1.2.0
// guid: 3a7d1f58-c2e6-4b09-8d41-6f0b9e2a7c35
// last-edited: 2026-10-26

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

func TestRecordIntroTimeout_ASecondTimeoutFromTheSameRunIsNotASecondStrike(t *testing.T) {
	s, f := newIntroStoreFixture(t)
	if out, err := s.RecordIntroTimeout(f.BookID, f.ID, strike("hash-a", "run-1", 2)); err != nil || out.Strikes != 1 || out.SameRun {
		t.Fatalf("first: out=%+v err=%v", out, err)
	}
	before := reread(t, s, f)
	out, err := s.RecordIntroTimeout(f.BookID, f.ID, strike("hash-a", "run-1", 2))
	if err != nil {
		t.Fatal(err)
	}
	if !out.SameRun || out.Strikes != 1 || out.Permanent || out.Skipped {
		t.Fatalf("a repeat in the same run must report SameRun with the stored count: %+v", out)
	}
	after := reread(t, s, f)
	if after.IntroTimeouts != 1 || after.TranscribeStatus != nil || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Errorf("a same-run repeat wrote: strikes=%d status=%v updated %v -> %v", after.IntroTimeouts, after.TranscribeStatus, before.UpdatedAt, after.UpdatedAt)
	}
	// The next run is a new, independent piece of evidence.
	out, err = s.RecordIntroTimeout(f.BookID, f.ID, strike("hash-a", "run-2", 2))
	if err != nil || out.Strikes != 2 || !out.Permanent || out.SameRun {
		t.Fatalf("second run: out=%+v err=%v", out, err)
	}
	// And once permanent, the same run asking again is still just a repeat.
	out, err = s.RecordIntroTimeout(f.BookID, f.ID, strike("hash-a", "run-2", 2))
	if err != nil || !out.SameRun || !out.Permanent || out.Strikes != 2 {
		t.Fatalf("repeat after permanent: out=%+v err=%v", out, err)
	}
}

func TestRecordIntroTimeout_ANewHashInTheSameRunCountsFromOne(t *testing.T) {
	s, f := newIntroStoreFixture(t)
	if _, err := s.RecordIntroTimeout(f.BookID, f.ID, strike("hash-a", "run-1", 3)); err != nil {
		t.Fatal(err)
	}
	row := reread(t, s, f)
	row.FileHash = "hash-b"
	if err := s.UpdateBookFile(f.ID, row); err != nil {
		t.Fatal(err)
	}
	out, err := s.RecordIntroTimeout(f.BookID, f.ID, strike("hash-b", "run-1", 3))
	if err != nil || out.SameRun || out.Strikes != 1 {
		t.Fatalf("the run id only dedups strikes against the same bytes: out=%+v err=%v", out, err)
	}
}

func TestRecordIntroTimeout_AnEmptyRunIDCannotBeDeduplicated(t *testing.T) {
	s, f := newIntroStoreFixture(t)
	for want := 1; want <= 2; want++ {
		out, err := s.RecordIntroTimeout(f.BookID, f.ID, strike("hash-a", "", 5))
		if err != nil || out.SameRun || out.Strikes != want {
			t.Fatalf("strike %d: out=%+v err=%v", want, out, err)
		}
	}
}

func TestRecordIntroTimeout_KeepsAConcurrentWritersFields(t *testing.T) {
	s, f := newIntroStoreFixture(t)
	if _, err := s.RecordIntroTimeout(f.BookID, f.ID, strike("hash-a", "run-1", 3)); err != nil {
		t.Fatal(err)
	}
	// Another writer changes an unrelated field, then a strike lands: both stay.
	row := reread(t, s, f)
	row.Duration = 4321
	if err := s.UpdateBookFile(f.ID, row); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordIntroTimeout(f.BookID, f.ID, strike("hash-a", "run-2", 3)); err != nil {
		t.Fatal(err)
	}
	got := reread(t, s, f)
	if got.Duration != 4321 || got.IntroTimeouts != 2 || got.IntroTimeoutRunID != "run-2" {
		t.Errorf("duration=%d strikes=%d run=%q", got.Duration, got.IntroTimeouts, got.IntroTimeoutRunID)
	}
}

func TestClearIntroTimeouts_DropsStrikesWhenTheHashMatches(t *testing.T) {
	s, f := newIntroStoreFixture(t)
	if _, err := s.RecordIntroTimeout(f.BookID, f.ID, strike("hash-a", "run-1", 3)); err != nil {
		t.Fatal(err)
	}
	cleared, err := s.ClearIntroTimeouts(f.BookID, f.ID, "hash-a")
	if err != nil || !cleared {
		t.Fatalf("cleared=%v err=%v", cleared, err)
	}
	got := reread(t, s, f)
	if got.IntroTimeouts != 0 || got.IntroTimeoutHash != "" || got.IntroTimeoutRunID != "" {
		t.Errorf("strikes survived: %d/%q/%q", got.IntroTimeouts, got.IntroTimeoutHash, got.IntroTimeoutRunID)
	}
	// Nothing left: a second call is a no-op.
	if cleared, err := s.ClearIntroTimeouts(f.BookID, f.ID, "hash-a"); err != nil || cleared {
		t.Errorf("second call: cleared=%v err=%v", cleared, err)
	}
}

func TestClearIntroTimeouts_DropsAPermanentVerdictTheStrikesProduced(t *testing.T) {
	s, f := newIntroStoreFixture(t)
	if _, err := s.RecordIntroTimeout(f.BookID, f.ID, strike("hash-a", "run-1", 1)); err != nil {
		t.Fatal(err)
	}
	if cleared, err := s.ClearIntroTimeouts(f.BookID, f.ID, "hash-a"); err != nil || !cleared {
		t.Fatalf("cleared=%v err=%v", cleared, err)
	}
	got := reread(t, s, f)
	if got.TranscribeStatus != nil || got.TranscribeError != nil || got.IntroRunOpID != "" {
		t.Errorf("a decoded file kept its transcribe_timeout verdict: status=%v err=%v run=%q", got.TranscribeStatus, got.TranscribeError, got.IntroRunOpID)
	}
}

func TestClearIntroTimeouts_LeavesAnotherStatusAndAMismatchedHashAlone(t *testing.T) {
	s, f := newIntroStoreFixture(t)
	if _, err := s.RecordIntroTimeout(f.BookID, f.ID, strike("hash-a", "run-1", 3)); err != nil {
		t.Fatal(err)
	}
	if cleared, err := s.ClearIntroTimeouts(f.BookID, f.ID, "hash-other"); err != nil || cleared {
		t.Fatalf("mismatched hash: cleared=%v err=%v", cleared, err)
	}
	if got := reread(t, s, f); got.IntroTimeouts != 1 {
		t.Errorf("a mismatched hash cleared strikes: %d", got.IntroTimeouts)
	}
	// A different status on the row is not ours to remove.
	row := reread(t, s, f)
	st := "whisper_error"
	row.TranscribeStatus = &st
	if err := s.UpdateBookFile(f.ID, row); err != nil {
		t.Fatal(err)
	}
	if cleared, err := s.ClearIntroTimeouts(f.BookID, f.ID, "hash-a"); err != nil || !cleared {
		t.Fatalf("cleared=%v err=%v", cleared, err)
	}
	if got := reread(t, s, f); got.TranscribeStatus == nil || *got.TranscribeStatus != "whisper_error" || got.IntroTimeouts != 0 {
		t.Errorf("status=%v strikes=%d", got.TranscribeStatus, got.IntroTimeouts)
	}
	if _, err := s.ClearIntroTimeouts(f.BookID, f.ID, ""); err == nil {
		t.Error("empty hash must be an error")
	}
}

func TestClearIntroTimeouts_StrikesCountedAgainstOtherBytesAreNotTouched(t *testing.T) {
	s, f := newIntroStoreFixture(t)
	if _, err := s.RecordIntroTimeout(f.BookID, f.ID, strike("hash-a", "run-1", 3)); err != nil {
		t.Fatal(err)
	}
	row := reread(t, s, f)
	row.FileHash = "hash-b" // replaced; the strikes still name hash-a
	if err := s.UpdateBookFile(f.ID, row); err != nil {
		t.Fatal(err)
	}
	before := reread(t, s, f)
	cleared, err := s.ClearIntroTimeouts(f.BookID, f.ID, "hash-b")
	if err != nil || cleared {
		t.Fatalf("cleared=%v err=%v", cleared, err)
	}
	if got := reread(t, s, f); got.IntroTimeoutHash != "hash-a" || !got.UpdatedAt.Equal(before.UpdatedAt) {
		t.Errorf("a clear for bytes that never struck wrote the row: hash=%q", got.IntroTimeoutHash)
	}
}

// NB1: when a file already holds a result the strike is refused, and the outcome
// says WHAT it holds, read under the same row lock as the refusal, so the caller
// can use a real transcript instead of guessing.
func TestRecordIntroTimeout_ReportsWhatTheHeldResultIs(t *testing.T) {
	s, f := newIntroStoreFixture(t)
	if _, err := s.SetBookFileIntro(f.BookID, f.ID, introOK("hash-a", "run-1", "real words")); err != nil {
		t.Fatal(err)
	}
	out, err := s.RecordIntroTimeout(f.BookID, f.ID, strike("hash-a", "run-2", 1))
	if err != nil || !out.Skipped || out.Stale {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	if out.HeldText != "real words" || out.HeldStatus != IntroStatusOK {
		t.Fatalf("the outcome must carry the held transcript and status: %+v", out)
	}
}

func TestRecordIntroTimeout_AStaleOrStrikelessOutcomeCarriesNoHeldResult(t *testing.T) {
	s, f := newIntroStoreFixture(t)
	out, err := s.RecordIntroTimeout(f.BookID, f.ID, strike("some-other-hash", "run-1", 2))
	if err != nil || !out.Stale || out.HeldText != "" || out.HeldStatus != "" {
		t.Fatalf("stale: out=%+v err=%v", out, err)
	}
	out, err = s.RecordIntroTimeout(f.BookID, f.ID, strike("hash-a", "run-1", 2))
	if err != nil || out.Skipped || out.HeldText != "" || out.HeldStatus != "" {
		t.Fatalf("counted: out=%+v err=%v", out, err)
	}
}

// NS2: two ops (or two runs) that see one stall within MinGap of each other
// are one piece of evidence, not two. The gap is measured from the stored time
// of the previous strike, so it holds across ops whatever their run ids are.
func TestRecordIntroTimeout_AStrikeFromAnotherRunInsideTheMinGapIsNotCounted(t *testing.T) {
	s, f := newIntroStoreFixture(t)
	at := time.Date(2026, 10, 25, 2, 0, 0, 0, time.UTC)
	mk := func(run string, at time.Time) IntroTimeoutStrike {
		st := strike("hash-a", run, 2)
		st.At, st.MinGap = at, 12*time.Hour
		return st
	}
	if out, err := s.RecordIntroTimeout(f.BookID, f.ID, mk("op-book", at)); err != nil || out.Strikes != 1 {
		t.Fatalf("first: out=%+v err=%v", out, err)
	}
	before := reread(t, s, f)
	out, err := s.RecordIntroTimeout(f.BookID, f.ID, mk("op-file", at.Add(20*time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	if !out.TooSoon || out.Strikes != 1 || out.Permanent || out.Skipped || out.SameRun {
		t.Fatalf("a strike 20 minutes after another run's must report TooSoon with the stored count: %+v", out)
	}
	if after := reread(t, s, f); after.IntroTimeouts != 1 || after.TranscribeStatus != nil || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("a too-soon strike wrote: strikes=%d status=%v", after.IntroTimeouts, after.TranscribeStatus)
	}
	// Past the gap the second run is independent evidence again.
	out, err = s.RecordIntroTimeout(f.BookID, f.ID, mk("op-file-2", at.Add(24*time.Hour)))
	if err != nil || out.Strikes != 2 || !out.Permanent || out.TooSoon {
		t.Fatalf("a day later: out=%+v err=%v", out, err)
	}
}

// A zero MinGap keeps the old rule (run id only), and a row struck before the
// time was recorded has no previous time to measure from.
func TestRecordIntroTimeout_NoMinGapAndNoStoredTimeCountNormally(t *testing.T) {
	s, f := newIntroStoreFixture(t)
	for want := 1; want <= 2; want++ {
		out, err := s.RecordIntroTimeout(f.BookID, f.ID, strike("hash-a", "run-"+string(rune('0'+want)), 5))
		if err != nil || out.TooSoon || out.Strikes != want {
			t.Fatalf("strike %d: out=%+v err=%v", want, out, err)
		}
	}
	row := reread(t, s, f)
	row.IntroTimeoutAt = nil
	if err := s.UpdateBookFile(f.ID, row); err != nil {
		t.Fatal(err)
	}
	st := strike("hash-a", "run-9", 5)
	st.MinGap = 12 * time.Hour
	if out, err := s.RecordIntroTimeout(f.BookID, f.ID, st); err != nil || out.TooSoon || out.Strikes != 3 {
		t.Fatalf("no stored time: out=%+v err=%v", out, err)
	}
}
