// file: internal/database/bookfile_intro_timeout_test.go
// version: 1.6.0
// guid: 3a7d1f58-c2e6-4b09-8d41-6f0b9e2a7c35
// last-edited: 2026-10-26

package database

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// strike is one timeout strike. Each run is answered by its own worker unless a
// test says otherwise, so the different-worker rule stays out of tests about
// something else.
func strike(hash, run string, limit int) IntroTimeoutStrike {
	return IntroTimeoutStrike{
		SourceHash: hash, RunOpID: run, Limit: limit,
		Detail: "transcribe_timeout: no result within 90s",
		At:     time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC),
		Worker: fmt.Sprintf("m:%x", "w"+run), // a known machine per run, in the stored id alphabet
	}
}

// S2 (review of #4176): a file becomes permanent only on a second strike from a
// DIFFERENT machine. One machine that is sick on two nights (the same Mac under
// the same CI load each evening) is not two pieces of evidence about the audio.
func TestRecordIntroTimeout_TheSecondStrikeMustComeFromADifferentWorker(t *testing.T) {
	s, f := newIntroStoreFixture(t)
	first := strike("hash-a", "run-1", 2)
	first.Worker = "m:a1a1a1a1"
	if out, err := s.RecordIntroTimeout(f.BookID, f.ID, first); err != nil || out.Strikes != 1 {
		t.Fatalf("strike 1: out=%+v err=%v", out, err)
	}
	if got := reread(t, s, f); got.IntroTimeoutWorker != first.Worker {
		t.Fatalf("worker not recorded with the strike: %q", got.IntroTimeoutWorker)
	}

	again := strike("hash-a", "run-2", 2)
	again.Worker = first.Worker
	out, err := s.RecordIntroTimeout(f.BookID, f.ID, again)
	if err != nil || !out.SameWorker || out.Strikes != 1 || out.Permanent {
		t.Fatalf("same worker, next night: out=%+v err=%v, want SameWorker and still one strike", out, err)
	}
	if got := reread(t, s, f); got.IntroTimeouts != 1 || got.TranscribeStatus != nil {
		t.Fatalf("a same-worker strike wrote: strikes=%d status=%v", got.IntroTimeouts, got.TranscribeStatus)
	}

	unnamed := strike("hash-a", "run-3", 2)
	unnamed.Worker = ""
	if out, err := s.RecordIntroTimeout(f.BookID, f.ID, unnamed); err != nil || !out.SameWorker {
		t.Fatalf("unnamed worker: out=%+v err=%v, want SameWorker (it cannot be shown to be different)", out, err)
	}

	other := strike("hash-a", "run-4", 2)
	other.Worker = "m:b2b2b2b2"
	if out, err := s.RecordIntroTimeout(f.BookID, f.ID, other); err != nil || out.Strikes != 2 || !out.Permanent {
		t.Fatalf("different worker: out=%+v err=%v, want the second strike and the permanent verdict", out, err)
	}
}

// A dispatcher condemnation names every machine it was stuck on; as a second
// strike it is evidence from more than the single machine of the first.
func TestRecordIntroTimeout_ACondemnationAfterOneWorkersStrikeCounts(t *testing.T) {
	s, f := newIntroStoreFixture(t)
	first := strike("hash-a", "run-1", 2)
	first.Worker = "m:a1a1a1a1"
	if _, err := s.RecordIntroTimeout(f.BookID, f.ID, first); err != nil {
		t.Fatal(err)
	}
	condemned := strike("hash-a", "run-2", 2)
	condemned.Worker = "m:a1a1a1a1 + m:b2b2b2b2"
	if out, err := s.RecordIntroTimeout(f.BookID, f.ID, condemned); err != nil || !out.Permanent {
		t.Fatalf("out=%+v err=%v", out, err)
	}
}

// R4 (review of #4182): two condemnations by the same pair of machines are two
// verdicts, each from two machines that were stuck on the file alone. Comparing
// the stored string counted the second as "the same worker" and the file never
// settled.
func TestRecordIntroTimeout_TheSameCondemnationPairCountsAgain(t *testing.T) {
	s, f := newIntroStoreFixture(t)
	for i, want := range []bool{false, true} {
		c := strike("hash-a", fmt.Sprintf("run-%d", i+1), 2)
		c.Worker = "m:a1a1a1a1 + m:b2b2b2b2"
		out, err := s.RecordIntroTimeout(f.BookID, f.ID, c)
		if err != nil || out.SameWorker || out.Permanent != want || out.Strikes != i+1 {
			t.Fatalf("condemnation %d: out=%+v err=%v", i+1, out, err)
		}
	}
}

// The row keeps every machine that struck it, so after A and B a strike from A
// again is not new evidence; a third machine is.
func TestRecordIntroTimeout_TheRowRemembersEveryMachineThatStruck(t *testing.T) {
	s, f := newIntroStoreFixture(t)
	for i, tc := range []struct {
		worker  string
		strikes int
		same    bool
	}{{"m:a1a1a1a1", 1, false}, {"m:b2b2b2b2", 2, false}, {"m:a1a1a1a1", 2, true}, {"m:c3c3c3c3", 3, false}} {
		st := strike("hash-a", fmt.Sprintf("run-%d", i+1), 3)
		st.Worker = tc.worker
		out, err := s.RecordIntroTimeout(f.BookID, f.ID, st)
		if err != nil || out.Strikes != tc.strikes || out.SameWorker != tc.same {
			t.Fatalf("strike %d by %s: out=%+v err=%v", i+1, tc.worker, out, err)
		}
	}
	if got := reread(t, s, f); got.IntroTimeoutWorker != "m:a1a1a1a1 + m:b2b2b2b2 + m:c3c3c3c3" || got.TranscribeStatus == nil {
		t.Fatalf("worker=%q status=%v", got.IntroTimeoutWorker, got.TranscribeStatus)
	}
}

// A first strike counts from any worker, named or not (an unnamed one came
// from an endpoint whose /health could not be read). Its machine is unknown,
// so the first known machine after it may be the same one: it is recorded, not
// counted, and a second known machine makes the file permanent.
func TestRecordIntroTimeout_AnUnnamedFirstStrikeNeedsTwoKnownMachinesAfterIt(t *testing.T) {
	s, f := newIntroStoreFixture(t)
	first := strike("hash-a", "run-1", 2)
	first.Worker = ""
	if out, err := s.RecordIntroTimeout(f.BookID, f.ID, first); err != nil || out.Strikes != 1 {
		t.Fatalf("strike 1: out=%+v err=%v", out, err)
	}
	second := strike("hash-a", "run-2", 2)
	second.Worker = "m:a1a1a1a1"
	if out, err := s.RecordIntroTimeout(f.BookID, f.ID, second); err != nil || out.Permanent || !out.SameWorker || !out.IdentityRecorded {
		t.Fatalf("first known machine: out=%+v err=%v, want it recorded and not counted", out, err)
	}
	if got := reread(t, s, f); got.IntroTimeouts != 1 || got.IntroTimeoutWorker != "m:a1a1a1a1" || got.IntroTimeoutRunID != "run-1" {
		t.Fatalf("recording the machine must touch only the worker field: strikes=%d worker=%q run=%q",
			got.IntroTimeouts, got.IntroTimeoutWorker, got.IntroTimeoutRunID)
	}
	third := strike("hash-a", "run-3", 2)
	third.Worker = "m:b2b2b2b2"
	if out, err := s.RecordIntroTimeout(f.BookID, f.ID, third); err != nil || !out.Permanent {
		t.Fatalf("a second known machine: out=%+v err=%v, want the permanent verdict", out, err)
	}
}

// S1 (review 3 of #4182): a worker that gains machine_id changes identity
// once. A strike from its old "u:" id, or a raw URL a pre-release build
// stored, followed by its new "m:" id 13 h later is ONE machine, not two.
func TestRecordIntroTimeout_AnUpgradedWorkersNewIdentityIsNotASecondMachine(t *testing.T) {
	for _, before := range []string{"u:0123456789ab", "http://192.0.2.10:19848"} {
		t.Run(before, func(t *testing.T) {
			s, f := newIntroStoreFixture(t)
			first := strike("hash-a", "run-1", 2)
			first.Worker = before
			if out, err := s.RecordIntroTimeout(f.BookID, f.ID, first); err != nil || out.Strikes != 1 {
				t.Fatalf("first: out=%+v err=%v", out, err)
			}
			second := strike("hash-a", "run-2", 2)
			second.At = first.At.Add(13 * time.Hour)
			second.MinGap = 12 * time.Hour
			second.Worker = "m:aaaaaaaaaaaaaaaa"
			out, err := s.RecordIntroTimeout(f.BookID, f.ID, second)
			if err != nil || out.Permanent || out.Strikes != 1 {
				t.Fatalf("out=%+v err=%v, want one strike and no verdict", out, err)
			}
			got := reread(t, s, f)
			if got.TranscribeStatus != nil || strings.Contains(got.IntroTimeoutWorker, "http") || !strings.Contains(got.IntroTimeoutWorker, "m:aaaaaaaaaaaaaaaa") {
				t.Fatalf("status=%v worker=%q, want no verdict, the new machine recorded and no address kept", got.TranscribeStatus, got.IntroTimeoutWorker)
			}
		})
	}
}

// N1 (review 4 of #4182): recording the first known machine restarts the
// MinGap window like a counted strike. The reviewer's sequence: an old worker's
// strike, the Mac recorded by the book op 13 h later, then llm1 through the
// file op an hour after that. Two machines failing within the hour is the
// "both sick at once" case MinGap damps, so llm1 is too soon, not a permanent
// verdict; it counts once the window has passed.
func TestRecordIntroTimeout_RecordingANewMachineStartsTheMinGap(t *testing.T) {
	s, f := newIntroStoreFixture(t)
	old := strike("hash-a", "run-book-1", 2)
	old.Worker, old.MinGap = "u:0123456789ab", 12*time.Hour
	if out, err := s.RecordIntroTimeout(f.BookID, f.ID, old); err != nil || out.Strikes != 1 {
		t.Fatalf("old worker: out=%+v err=%v", out, err)
	}
	mac := strike("hash-a", "run-book-2", 2)
	mac.Worker, mac.MinGap, mac.At = "m:aaaaaaaaaaaaaaaa", 12*time.Hour, old.At.Add(13*time.Hour)
	if out, err := s.RecordIntroTimeout(f.BookID, f.ID, mac); err != nil || !out.IdentityRecorded || out.Strikes != 1 {
		t.Fatalf("Mac: out=%+v err=%v, want its identity recorded and still one strike", out, err)
	}
	if got := reread(t, s, f); got.IntroTimeoutAt == nil || !got.IntroTimeoutAt.Equal(mac.At) || got.IntroTimeoutRunID != old.RunOpID {
		t.Fatalf("at=%v run=%q, want the recorded identity's time and the counted strike's run", got.IntroTimeoutAt, got.IntroTimeoutRunID)
	}
	llm1 := strike("hash-a", "run-file-2", 2)
	llm1.Worker, llm1.MinGap, llm1.At = "m:bbbbbbbbbbbbbbbb", 12*time.Hour, mac.At.Add(time.Hour)
	if out, err := s.RecordIntroTimeout(f.BookID, f.ID, llm1); err != nil || !out.TooSoon || out.Permanent || out.Strikes != 1 {
		t.Fatalf("llm1 an hour later: out=%+v err=%v, want TooSoon and no verdict", out, err)
	}
	if got := reread(t, s, f); got.TranscribeStatus != nil || got.IntroTimeouts != 1 {
		t.Fatalf("status=%v strikes=%d, want nothing written", got.TranscribeStatus, got.IntroTimeouts)
	}
	llm1.RunOpID, llm1.At = "run-file-3", mac.At.Add(13*time.Hour)
	if out, err := s.RecordIntroTimeout(f.BookID, f.ID, llm1); err != nil || out.Strikes != 2 || !out.Permanent {
		t.Fatalf("llm1 after the window: out=%+v err=%v, want the second strike and the verdict", out, err)
	}
}

// S1: old workers identify each port by a hash of its URL, so a condemnation
// by two ports of one Mac names two "u:" ids. That is one machine as far as
// anyone can tell; two such condemnations are not a permanent verdict.
func TestRecordIntroTimeout_APortPairCondemnationByOldWorkersIsNotTwoMachines(t *testing.T) {
	s, f := newIntroStoreFixture(t)
	for i, w := range []string{"u:000000019848 + u:000000019849", "u:000000019850 + u:000000019851"} {
		c := strike("hash-a", fmt.Sprintf("run-%d", i+1), 2)
		c.At = c.At.Add(time.Duration(i) * 13 * time.Hour)
		c.MinGap = 12 * time.Hour
		c.Worker = w
		out, err := s.RecordIntroTimeout(f.BookID, f.ID, c)
		if err != nil || out.Permanent || out.Strikes != 1 {
			t.Fatalf("condemnation %d: out=%+v err=%v, want one strike and no verdict", i+1, out, err)
		}
	}
	if got := reread(t, s, f); got.TranscribeStatus != nil {
		t.Fatalf("status = %v", *got.TranscribeStatus)
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
		st := strike("hash-a", "", 5)
		st.Worker = fmt.Sprintf("m:%08d", want)
		out, err := s.RecordIntroTimeout(f.BookID, f.ID, st)
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
