// file: internal/plugins/maintenance/whisper_no_answer_test.go
// version: 1.0.0
// guid: a97db0f2-8212-49bc-b646-6b4c39e34a80
// last-edited: 2026-10-26

package maintenance

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/serverdecode"
	"github.com/falkcorp/audiobook-organizer/internal/transcribe"
)

// Tests for answers that say nothing about the book: a 300 s clip that got no
// verdict, the worker's no-answer codes, a pool that stays unavailable, a
// second strike from the same worker, and clearing a book-op run's verdicts.
// Fixtures are synthetic: Gotham comic titles, throwaway paths under
// t.TempDir(), workers on 192.0.2.x.

// noBookOutcome fails the test when the book carries any transcribe outcome.
func (fx *introFx) noBookOutcome(t *testing.T, bookID, why string) {
	t.Helper()
	bk := fx.bookRow(bookID)
	if bk.IntroTranscription != nil || bk.TranscribeStatus != nil {
		t.Fatalf("%s: book has text=%v status=%v", why, derefOr(bk.IntroTranscription), derefOr(bk.TranscribeStatus))
	}
}

// noStrike fails the test when the file carries a strike or a status.
func (fx *introFx) noStrike(t *testing.T, bookID, fileID, why string) {
	t.Helper()
	if r := fx.row(bookID, fileID); r.IntroTimeouts != 0 || r.TranscribeStatus != nil {
		t.Fatalf("%s: file has strikes=%d status=%v", why, r.IntroTimeouts, derefOr(r.TranscribeStatus))
	}
}

func derefOr(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

// holdSilence records the per-file op's silence result on a file's current bytes.
func (fx *introFx) holdSilence(bookID, fileID, hash string) {
	fx.t.Helper()
	s := silenceSentinel
	if _, err := fx.s.SetBookFileIntro(bookID, fileID, database.BookFileIntroResult{
		SourceHash: hash, RunOpID: "earlier", Status: statusEmpty, Transcript: &s, At: time.Now(), Done: true, Silence: true,
	}); err != nil {
		fx.t.Fatal(err)
	}
}

// ---- B2: nothing listened past 90 s, so the book is never sealed silent ------

// A one-file book: the 90 s clip is empty and the 300 s clip runs out its
// deadline. Nothing heard past 90 s, so the book is deferred, not [SILENCE].
func TestBookPage_AOneFileBookWhoseLongClipTimesOutIsDeferredNotSilent(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")
	fx := newIntroFx(t)
	b := fx.book("Knightfall")
	id1, _ := fx.file(b, "01.m4b", 1, "kf-1", false)
	fn, calls := scriptedPath(silentAnswer, transcribe.BatchResult{Error: decoderTimeoutText})
	swapPathSeams(t, true, "", fn)

	_, accum, _ := fx.bookPage(t, false, b)
	if got := calls(); len(got) != 2 || got[1][b].Seconds != remoteRetryLongSeconds {
		t.Fatalf("want the 90 s pass then the 300 s retry, calls=%v", got)
	}
	fx.noBookOutcome(t, b, "a book whose 300 s clip got no answer")
	fx.noStrike(t, b, id1, "a 300 s clip the per-file op never sends")
	if st := accum.snapshot(); st.Deferred != 1 || st.TranscribeTimeout != 0 || st.Empty != 0 {
		t.Fatalf("the book must be deferred: %+v", st)
	}
}

// The mixed-version window: a worker older than the per-clip deadline hangs on
// the 300 s clip and the dispatcher condemns it. Still no answer past 90 s.
func TestBookPage_AOneFileBookWhoseLongClipIsCondemnedIsDeferredNotSilent(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")
	fx := newIntroFx(t)
	b := fx.book("Knightquest")
	id1, _ := fx.file(b, "01.m4b", 1, "kq-1", false)
	condemned := transcribe.BatchResult{Condemned: true, Endpoint: "http://192.0.2.10:8000 + http://192.0.2.11:8000",
		Error: transcribe.PathErrTranscribeTimeout + ": no result: this file alone left 2 worker(s) stuck past their deadline"}
	fn, _ := scriptedPath(silentAnswer, condemned)
	swapPathSeams(t, true, "", fn)

	_, accum, _ := fx.bookPage(t, false, b)
	fx.noBookOutcome(t, b, "a condemned 300 s clip")
	fx.noStrike(t, b, id1, "a condemned 300 s clip")
	if st := accum.snapshot(); st.Deferred != 1 {
		t.Fatalf("the book must be deferred: %+v", st)
	}
}

// The control: a book silent through every clip that could listen still gets
// the sentinel, so the deferrals above are about the missing answer only.
func TestBookPage_AOneFileBookSilentThroughTheLongClipGetsTheSentinel(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")
	fx := newIntroFx(t)
	b := fx.book("KnightsEnd")
	fx.file(b, "01.m4b", 1, "ke-1", false)
	fn, _ := scriptedPath(silentAnswer, silentAnswer)
	swapPathSeams(t, true, "", fn)

	fx.bookPage(t, false, b)
	if bk := fx.bookRow(b); bk.IntroTranscription == nil || *bk.IntroTranscription != silenceSentinel {
		t.Fatalf("want the silence sentinel, got %v", derefOr(bk.IntroTranscription))
	}
}

// Retry 1 times out and retry 2 (file 2's first 90 s) is empty too: deferred.
func TestBookPage_ALongClipTimeoutThenASilentSecondFileIsDeferredNotSilent(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")
	fx := newIntroFx(t)
	b := fx.book("Hush")
	id1, _ := fx.file(b, "01.m4b", 1, "hu-1", false)
	id2, p2 := fx.file(b, "02.m4b", 2, "hu-2", false)
	fn, calls := scriptedPath(silentAnswer, transcribe.BatchResult{Error: decoderTimeoutText}, silentAnswer)
	swapPathSeams(t, true, "", fn)

	_, accum, _ := fx.bookPage(t, false, b)
	if got := calls(); len(got) != 3 || got[2][b].Path != p2 {
		t.Fatalf("retry 2 must still carry the second file, calls=%v", got)
	}
	fx.noBookOutcome(t, b, "an unanswered 300 s clip and an empty second file")
	fx.noStrike(t, b, id1, "file 1")
	fx.noStrike(t, b, id2, "file 2")
	if st := accum.snapshot(); st.Deferred != 1 {
		t.Fatalf("the book must be deferred: %+v", st)
	}
}

// Retry 1 times out and retry 2 hears words: those words are the book's answer.
func TestBookPage_ALongClipTimeoutThenASecondFileTranscriptIsTheAnswer(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")
	fx := newIntroFx(t)
	b := fx.book("Dark Victory")
	fx.file(b, "01.m4b", 1, "dv-1", false)
	fx.file(b, "02.m4b", 2, "dv-2", false)
	fn, _ := scriptedPath(silentAnswer, transcribe.BatchResult{Error: decoderTimeoutText}, transcribe.BatchResult{Text: goodIntro})
	swapPathSeams(t, true, "", fn)

	fx.bookPage(t, false, b)
	if bk := fx.bookRow(b); bk.IntroTranscription == nil || *bk.IntroTranscription != goodIntro {
		t.Fatalf("file 2's transcript must be the book's answer, got %v", derefOr(bk.IntroTranscription))
	}
}

// Retry 1 times out and retry 2 times out too: file 2 takes the strike (its 90 s
// clip is one the per-file op also sends), file 1 none, and the book is deferred.
func TestBookPage_ALongClipTimeoutThenASecondFileTimeoutStrikesOnlyFileTwo(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")
	fx := newIntroFx(t)
	b := fx.book("The Man Who Laughs")
	id1, _ := fx.file(b, "01.m4b", 1, "ml-1", false)
	id2, _ := fx.file(b, "02.m4b", 2, "ml-2", false)
	fn, _ := scriptedPath(silentAnswer, transcribe.BatchResult{Error: decoderTimeoutText}, transcribe.BatchResult{Error: decoderTimeoutText})
	swapPathSeams(t, true, "", fn)

	_, accum, _ := fx.bookPage(t, false, b)
	fx.noStrike(t, b, id1, "file 1's 300 s clip")
	if r := fx.row(b, id2); r.IntroTimeouts != 1 {
		t.Fatalf("file 2 must carry one strike, got %d", r.IntroTimeouts)
	}
	fx.noBookOutcome(t, b, "two clips with no answer")
	if st := accum.snapshot(); st.Deferred != 1 {
		t.Fatalf("the book must be deferred: %+v", st)
	}
}

// Retry 1 times out and retry 2 times out on a second file that already holds
// silence. Without the 300 s verdict that is not "silent as far as every clip
// could listen" (compare TestBookPage_ARetryTwoTimeoutOnAFileHoldingSilenceGetsTheSentinel).
func TestBookPage_ALongClipTimeoutAndASecondFileHoldingSilenceIsDeferred(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")
	fx := newIntroFx(t)
	b := fx.book("Haunted Knight")
	fx.file(b, "01.m4b", 1, "hk-1", false)
	id2, _ := fx.file(b, "02.m4b", 2, "hk-2", false)
	fx.holdSilence(b, id2, "hk-2")
	fn, _ := scriptedPath(silentAnswer, transcribe.BatchResult{Error: decoderTimeoutText}, transcribe.BatchResult{Error: decoderTimeoutText})
	swapPathSeams(t, true, "", fn)

	_, accum, _ := fx.bookPage(t, false, b)
	fx.noBookOutcome(t, b, "a held silent second file after an unanswered 300 s clip")
	if st := accum.snapshot(); st.Deferred != 1 {
		t.Fatalf("the book must be deferred: %+v", st)
	}
}

// ---- N3: a first file the per-file op already heard as silence ---------------

// The first pass times out on a first file whose 90 s clip the per-file op
// already heard as silence. That is the same clip; the book goes on to the
// 300 s retry instead of being deferred on every run with nothing trying it.
func TestBookPage_AFirstPassTimeoutOnAFileHoldingSilenceGoesOnToTheLongClip(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")
	fx := newIntroFx(t)
	b := fx.book("Mad Love")
	id1, _ := fx.file(b, "01.m4b", 1, "mlv-1", false)
	fx.holdSilence(b, id1, "mlv-1")
	fn, calls := scriptedPath(transcribe.BatchResult{Error: decoderTimeoutText}, transcribe.BatchResult{Text: goodIntro})
	swapPathSeams(t, true, "", fn)

	_, accum, _ := fx.bookPage(t, false, b)
	got := calls()
	if len(got) < 2 || got[1][b].Seconds != remoteRetryLongSeconds {
		t.Fatalf("the 300 s retry must run, calls=%v", got)
	}
	if bk := fx.bookRow(b); bk.IntroTranscription == nil || *bk.IntroTranscription != goodIntro {
		t.Fatalf("the 300 s clip's words must be the book's answer, got %v (stats %+v)", derefOr(bk.IntroTranscription), accum.snapshot())
	}
	if r := fx.row(b, id1); r.IntroTimeouts != 0 {
		t.Fatalf("a held file's first-pass timeout must not strike: %d", r.IntroTimeouts)
	}
}

// ---- no-answer codes: never a verdict, never a strike, never silence ---------

var noAnswerCodes = []string{
	transcribe.PathErrWorkerMisconfigured,
	transcribe.PathErrDecoderKilled,
	transcribe.PathErrRequestAbandoned,
	transcribe.PathErrWorkerFault,
}

func TestBookPage_ANoAnswerCodeDefersWithoutAStrikeOrASentinel(t *testing.T) {
	for _, code := range noAnswerCodes {
		for _, stage := range []string{"first pass", "retry 1", "retry 2"} {
			t.Run(code+"/"+stage, func(t *testing.T) {
				t.Setenv(serverdecode.EnvVar, "")
				fx := newIntroFx(t)
				b := fx.book("Batman Year One " + code + " " + stage)
				id1, _ := fx.file(b, "01.m4b", 1, "y1-1", false)
				id2, _ := fx.file(b, "02.m4b", 2, "y1-2", false)
				na := transcribe.BatchResult{Error: code + ": no answer about this file"}
				var fn pathBatchFn
				switch stage {
				case "first pass":
					fn, _ = scriptedPath(na)
				case "retry 1":
					fn, _ = scriptedPath(silentAnswer, na)
				default:
					fn, _ = scriptedPath(silentAnswer, silentAnswer, na)
				}
				swapPathSeams(t, true, "", fn)

				_, accum, _ := fx.bookPage(t, false, b)
				fx.noBookOutcome(t, b, code+" at "+stage)
				fx.noStrike(t, b, id1, code+" at "+stage)
				fx.noStrike(t, b, id2, code+" at "+stage)
				if st := accum.snapshot(); st.Deferred != 1 || st.WhisperError != 0 {
					t.Fatalf("want the book deferred and no whisper_error: %+v", st)
				}
			})
		}
	}
}

func TestFileIntroRun_ANoAnswerCodeDefersWithoutAStrike(t *testing.T) {
	for _, code := range noAnswerCodes {
		t.Run(code, func(t *testing.T) {
			fx := newIntroFx(t)
			b := fx.book("Batman Year Two " + code)
			id1, p1 := fx.file(b, "01.m4b", 1, "y2-1", false)
			fx.whisp.errs[p1] = code + ": no answer about this file"

			if _, err := fx.run(""); err != nil {
				t.Fatalf("a no-answer code must not fail the run: %v", err)
			}
			if r := fx.row(b, id1); r.IntroTimeouts != 0 || r.TranscribeStatus != nil || r.IntroSourceHash != "" || r.IntroTranscription != nil {
				t.Fatalf("a no-answer code wrote to the file: %+v", r)
			}
			if st, _ := fx.s.GetFileIntroStats(); st.Deferred != 1 || st.WhisperError != 0 {
				t.Fatalf("stats: %+v", st)
			}
		})
	}
}

// ---- a worker fault on a one-worker pool does not stop the run ---------------

// One endpoint whose decoder cannot start: the dispatcher returns the fault as
// an unrecognized transport error. Every batch is deferred once (no outage
// retries), and the run reaches every batch instead of stopping at the first.
func TestFileIntroRun_AWorkerFaultOnAOneWorkerPoolDefersEachBatchAndTheRunGoesOn(t *testing.T) {
	fx := newIntroFx(t)
	fileIntroSlots = func() int { return 1 }
	const batches = 5
	rows := fx.manyFiles("No Mans Land Fault", fileIntroBatchGroups*batches)
	fx.whisp.transErr = &transcribe.TransportError{
		Endpoints: []string{"http://192.0.2.10:8000"},
		Err: &transcribe.WorkerFaultError{URL: "http://192.0.2.10:8000", Reason: "worker_misconfigured on 8 of 8 clips",
			Faulted: map[string]transcribe.BatchResult{"x": {Error: transcribe.PathErrWorkerMisconfigured + ": ffmpeg cannot start"}}},
	}

	if _, err := fx.run(""); err != nil {
		t.Fatalf("a worker fault must not stop the run: %v", err)
	}
	if n := len(fx.whisp.calls); n != batches {
		t.Fatalf("want one call per batch (%d), got %d", batches, n)
	}
	for id, b := range rows {
		if r := fx.row(b, id); r.TranscribeStatus != nil || r.IntroTimeouts != 0 || r.IntroTranscription != nil {
			t.Fatalf("a worker fault wrote something for %s: %+v", id, r)
		}
	}
	if st, _ := fx.s.GetFileIntroStats(); st.Deferred != len(rows) {
		t.Fatalf("every file must be deferred: %+v", st)
	}
}

// ---- the book op counts only a pool that is unavailable toward stopping -------

func TestBookPage_OnlyAnUnavailablePoolCountsTowardStoppingTheRun(t *testing.T) {
	cases := []struct {
		name  string
		err   error
		count bool
	}{
		{"recognized outage", &transcribe.TransportError{Endpoints: []string{"http://192.0.2.10:8000"}, Err: errors.New("connection refused"), Recognized: true}, true},
		{"pool unavailable after pacing", &transcribe.TransportError{Endpoints: []string{"http://192.0.2.10:8000"}, Err: fmt.Errorf("every endpoint benched: %w", transcribe.ErrPoolUnavailable)}, true},
		{"worker fault", &transcribe.TransportError{Endpoints: []string{"http://192.0.2.10:8000"}, Err: &transcribe.WorkerFaultError{URL: "http://192.0.2.10:8000", Reason: "decoder_killed"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(serverdecode.EnvVar, "")
			fx := newIntroFx(t)
			b := fx.book("Cataclysm " + tc.name)
			id1, _ := fx.file(b, "01.m4b", 1, "cy-1", false)
			var calls atomic.Int32
			swapPathSeams(t, true, "", answerEvery(&calls, transcribe.BatchResult{}, tc.err))

			_, accum, _ := fx.bookPage(t, false, b)
			want := int64(0)
			if tc.count {
				want = 1
			}
			if got := accum.unavailable.n.Load(); got != want {
				t.Fatalf("unavailable streak = %d, want %d", got, want)
			}
			fx.noBookOutcome(t, b, tc.name)
			fx.noStrike(t, b, id1, tc.name)
			if st := accum.snapshot(); st.Deferred != 1 {
				t.Fatalf("the book must be deferred: %+v", st)
			}
		})
	}
}

// Five pages in a row with no worker available stop the whole run.
func TestIntroTranscribeRun_StopsAfterFiveUnavailablePagesInARow(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")
	p, s, ids := newRemoteRun(t, introTranscribePageSize*whisperUnavailableMaxBatches+1)
	var calls atomic.Int32
	swapPathSeams(t, true, "", answerEvery(&calls, transcribe.BatchResult{},
		&transcribe.TransportError{Endpoints: []string{"http://192.0.2.10:8000"}, Err: fmt.Errorf("benched: %w", transcribe.ErrPoolUnavailable)}))
	err := p.runIntroTranscribe(context.Background(), liveTranscribeParams(`{}`), &denomReporter{})
	if !errors.Is(err, errWhisperUnavailable) {
		t.Fatalf("want errWhisperUnavailable, got %v", err)
	}
	// Pages run introTranscribePageConc at a time, so the pages already in
	// flight when the streak is reached finish; the error is what proves the
	// run stopped on the streak rather than running out of pages.
	if got := calls.Load(); got < int32(whisperUnavailableMaxBatches) {
		t.Fatalf("only %d pages ran", got)
	}
	for _, id := range ids {
		if b, _ := s.GetBookByID(id); b == nil || b.TranscribeStatus != nil || b.IntroTranscription != nil {
			t.Fatalf("an unavailable page wrote to %s", id)
		}
	}
}

// ---- S2: the second strike must come from a different worker -----------------

func TestBookPage_ASecondStrikeFromTheSameWorkerIsNotCounted(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")
	fx := newIntroFx(t)
	b := fx.book("The Killing Joke")
	id1, _ := fx.file(b, "01.m4b", 1, "kj-1", false)
	const sick = "http://192.0.2.50:8000"
	fn, _ := scriptedPath(transcribe.BatchResult{Error: decoderTimeoutText, Endpoint: sick})
	swapPathSeams(t, true, "", fn)
	fx.bookPage(t, false, b) // strike 1, from the sick worker

	_, accum, logs := fx.bookPage(t, false, b) // the same worker again
	if r := fx.row(b, id1); r.IntroTimeouts != 1 || r.TranscribeStatus != nil {
		t.Fatalf("a second strike from the same worker counted: strikes=%d status=%v", r.IntroTimeouts, derefOr(r.TranscribeStatus))
	}
	fx.noBookOutcome(t, b, "the same worker twice")
	if st := accum.snapshot(); st.Deferred != 1 || st.TranscribeTimeout != 0 {
		t.Fatalf("the book must be deferred: %+v", st)
	}
	if !strings.Contains(logs, "same worker") {
		t.Fatalf("the log must say why the strike was not counted:\n%s", logs)
	}

	fn, _ = scriptedPath(transcribe.BatchResult{Error: decoderTimeoutText, Endpoint: "http://192.0.2.51:8000"})
	swapPathSeams(t, true, "", fn)
	fx.bookPage(t, false, b) // a second worker agrees
	if r := fx.row(b, id1); r.TranscribeStatus == nil || *r.TranscribeStatus != statusTranscribeTimeout {
		t.Fatalf("a second worker's strike must make it permanent: %+v", r)
	}
	if bk := fx.bookRow(b); bk.TranscribeStatus == nil || *bk.TranscribeStatus != statusTranscribeTimeout {
		t.Fatalf("book status: %v", derefOr(bk.TranscribeStatus))
	}
}

func TestFileIntroRun_ASecondStrikeFromTheSameWorkerIsNotCounted(t *testing.T) {
	fx := newIntroFx(t)
	b := fx.book("A Death in the Family")
	id, _ := fx.timeoutFile(b, "01.m4b", 1, "df-1")
	fx.whisp.worker = "http://192.0.2.60:8000"
	for _, run := range []string{"run-1", "run-2"} {
		if _, err := fx.runAs(&fileIntroRep{id: run}, ""); err != nil {
			t.Fatal(err)
		}
	}
	if r := fx.row(b, id); r.IntroTimeouts != 1 || r.TranscribeStatus != nil {
		t.Fatalf("a second strike from the same worker counted: strikes=%d status=%v", r.IntroTimeouts, derefOr(r.TranscribeStatus))
	}
	if st, _ := fx.s.GetFileIntroStats(); st.Deferred != 1 || st.TranscribeTimeout != 0 {
		t.Fatalf("stats: %+v", st)
	}

	fx.whisp.worker = "http://192.0.2.61:8000"
	if _, err := fx.runAs(&fileIntroRep{id: "run-3"}, ""); err != nil {
		t.Fatal(err)
	}
	if r := fx.row(b, id); r.TranscribeStatus == nil || *r.TranscribeStatus != statusTranscribeTimeout || r.IntroTimeouts != 2 {
		t.Fatalf("a second worker's strike must make it permanent: %+v", r)
	}
}

// ---- S3: clearing a book-op run rolls back the book verdict it stamped --------

func TestFileIntroClear_ABookOpRunIdAlsoRollsBackTheBookVerdictItStamped(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")
	fx := newIntroFx(t)
	a := fx.book("Under the Hood") // made permanent by the run, untouched since -> reverted
	a1, _ := fx.file(a, "01.m4b", 1, "uh-1", false)
	fx.strike(a, a1, "uh-1", 1)
	c := fx.book("Hush Returns") // made permanent by the run, then relabelled -> kept
	c1, _ := fx.file(c, "01.m4b", 1, "hr-1", false)
	fx.strike(c, c1, "hr-1", 1)

	fn, _ := scriptedPath(transcribe.BatchResult{Error: decoderTimeoutText})
	swapPathSeams(t, true, "", fn)
	const run = "op-book-s3"
	fx.bookPageAs(t, run, false, nil, a, c)
	for _, id := range []string{a, c} {
		if bk := fx.bookRow(id); bk.TranscribeStatus == nil || *bk.TranscribeStatus != statusTranscribeTimeout {
			t.Fatalf("setup: book %s not stamped: %v", id, derefOr(bk.TranscribeStatus))
		}
	}
	if r := fx.row(a, a1); r.IntroRunOpID != run {
		t.Fatalf("setup: the permanent verdict must name the run, got %q", r.IntroRunOpID)
	}
	if _, err := fx.s.ModifyBook(c, func(cur *database.Book) error {
		cur.TranscribeError = sp("relabelled by a later run")
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	rep := &fileIntroRep{id: "clear-s3-dry"}
	if err := fx.p.runFileIntroTranscribe(context.Background(), []byte(`{"clear_run_id":"`+run+`"}`), rep); err != nil {
		t.Fatal(err)
	}
	if m := rep.lastMessage(); !strings.Contains(m, "would clear per-file results on 2 files and 1 book") {
		t.Fatalf("clear preview: %q", m)
	}
	if bk := fx.bookRow(a); bk.TranscribeStatus == nil {
		t.Fatal("the dry run wrote")
	}

	rep = &fileIntroRep{id: "clear-s3"}
	if err := fx.p.runFileIntroTranscribe(context.Background(), liveTranscribeParams(`{"clear_run_id":"`+run+`"}`), rep); err != nil {
		t.Fatal(err)
	}
	if m := rep.lastMessage(); !strings.Contains(m, "cleared per-file results on 2 files and 1 book") {
		t.Fatalf("clear message: %q", m)
	}
	fx.noStrike(t, a, a1, "a cleared file")
	if bk := fx.bookRow(a); bk.TranscribeStatus != nil || bk.TranscribeError != nil || bk.TranscribeAttemptedAt != nil {
		t.Fatalf("the run's book verdict was not rolled back: status=%v err=%v", derefOr(bk.TranscribeStatus), derefOr(bk.TranscribeError))
	}
	if bk := fx.bookRow(c); bk.TranscribeStatus == nil || derefOr(bk.TranscribeError) != "relabelled by a later run" {
		t.Fatalf("a verdict rewritten after the run was rolled back: %v", derefOr(bk.TranscribeError))
	}
}

// A book stamped with its first file's permanent verdict (never sent) is due
// again once that verdict is cleared: the stamp's cause is gone.
func TestTranscribeInputChanged_AFirstFileStampIsDueOnceTheFilesVerdictIsCleared(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")
	fx := newIntroFx(t)
	b := fx.book("War Games")
	id1, _ := fx.file(b, "01.m4b", 1, "wg-1", false)
	fx.strike(b, id1, "wg-1", transcribeTimeoutStrikeLimit)
	var calls atomic.Int32
	swapPathSeams(t, true, "", answerEvery(&calls, transcribe.BatchResult{Text: goodIntro}, nil))
	fx.bookPage(t, false, b)
	bk := fx.bookRow(b)
	if derefOr(bk.TranscribeError) != firstFilePermanentTimeoutDetail {
		t.Fatalf("setup: want the first-file stamp, got %v", derefOr(bk.TranscribeError))
	}
	if transcribeInputChanged(fx.s, bk) {
		t.Fatal("a book whose first file is still permanent must stay skipped")
	}
	// The run that made the file permanent is cleared.
	last := fmt.Sprintf("earlier-run-%d", transcribeTimeoutStrikeLimit-1)
	if err := fx.p.runFileIntroTranscribe(context.Background(), liveTranscribeParams(`{"clear_run_id":"`+last+`"}`), &fileIntroRep{id: "clear-wg"}); err != nil {
		t.Fatal(err)
	}
	fx.noStrike(t, b, id1, "the cleared first file")
	if !transcribeInputChanged(fx.s, fx.bookRow(b)) {
		t.Fatal("the stamp's cause is gone, so the book must be due again")
	}
}
