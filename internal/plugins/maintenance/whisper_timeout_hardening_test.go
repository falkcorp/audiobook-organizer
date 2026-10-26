// file: internal/plugins/maintenance/whisper_timeout_hardening_test.go
// version: 1.0.0
// guid: 3c9d7e41-5b28-4f06-a1d3-8e2f60b4c715
// last-edited: 2026-10-26

package maintenance

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/serverdecode"
	"github.com/falkcorp/audiobook-organizer/internal/transcribe"
)

// Tests for the review round on the whisper hang fix: which file a timeout is
// charged to, one strike per run, the worker-fault breaker, partial results
// that settle before a batch-level error, the write gate, and strike clearing.
// Fixtures are synthetic (Gotham comic titles, throwaway temp paths).

var silentAnswer = transcribe.BatchResult{}

// scriptedPath answers the path-batch seam one round per call with the given
// results (the last repeats), every job in the call getting that answer, and
// records each call's jobs.
func scriptedPath(rounds ...transcribe.BatchResult) (pathBatchFn, func() []map[string]transcribe.PathJob) {
	var mu sync.Mutex
	var seen []map[string]transcribe.PathJob
	fn := func(_ context.Context, jobs map[string]transcribe.PathJob, _ transcribe.BatchOptions) (map[string]transcribe.BatchResult, error) {
		mu.Lock()
		n := len(seen)
		seen = append(seen, jobs)
		mu.Unlock()
		res := rounds[min(n, len(rounds)-1)]
		out := make(map[string]transcribe.BatchResult, len(jobs))
		for id := range jobs {
			out[id] = res
		}
		return out, nil
	}
	return fn, func() []map[string]transcribe.PathJob {
		mu.Lock()
		defer mu.Unlock()
		return append([]map[string]transcribe.PathJob(nil), seen...)
	}
}

// strike records n strikes against a file's current bytes, each from its own run.
func (fx *introFx) strike(bookID, fileID, hash string, n int) {
	fx.t.Helper()
	for i := range n {
		if _, err := fx.s.RecordIntroTimeout(bookID, fileID, database.IntroTimeoutStrike{
			SourceHash: hash, RunOpID: fmt.Sprintf("earlier-run-%d", i), Detail: decoderTimeoutText,
			At: time.Now(), Limit: transcribeTimeoutStrikeLimit,
		}); err != nil {
			fx.t.Fatal(err)
		}
	}
}

// ---- B1: a timeout is charged to the file the request carried ---------------

func TestBookPage_ARetryTwoTimeoutIsChargedToTheSecondFile(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")
	fx := newIntroFx(t)
	b := fx.book("The Long Halloween")
	id1, _ := fx.file(b, "01.m4b", 1, "lh-1", false)
	id2, p2 := fx.file(b, "02.m4b", 2, "lh-2", false)

	// First pass and the 300 s retry hear nothing; the second file's clip then
	// kills the decoder.
	fn, calls := scriptedPath(silentAnswer, silentAnswer, transcribe.BatchResult{Error: decoderTimeoutText})
	swapPathSeams(t, true, "", fn)
	_, accum, _ := fx.bookPage(t, false, b)

	if got := calls(); len(got) != 3 || got[2][b].Path != p2 {
		t.Fatalf("retry 2 must carry the second file, calls=%v", got)
	}
	if r := fx.row(b, id1); r.IntroTimeouts != 0 || r.TranscribeStatus != nil {
		t.Fatalf("the FIRST file was charged for the second file's timeout: strikes=%d status=%v", r.IntroTimeouts, r.TranscribeStatus)
	}
	if r := fx.row(b, id2); r.IntroTimeouts != 1 || r.IntroTimeoutHash != "lh-2" {
		t.Fatalf("the second file must carry the strike: strikes=%d hash=%q", r.IntroTimeouts, r.IntroTimeoutHash)
	}
	if st := accum.snapshot(); st.Deferred != 1 || st.TranscribeTimeout != 0 {
		t.Fatalf("one strike defers the book: %+v", st)
	}

	// A second run reaches the limit ON THE SECOND FILE, and the book follows.
	fn, _ = scriptedPath(silentAnswer, silentAnswer, transcribe.BatchResult{Error: decoderTimeoutText})
	swapPathSeams(t, true, "", fn)
	fx.bookPage(t, false, b)
	if r := fx.row(b, id1); r.IntroTimeouts != 0 || r.TranscribeStatus != nil {
		t.Fatalf("file 1 touched on the second run: %+v", r)
	}
	if r := fx.row(b, id2); r.TranscribeStatus == nil || *r.TranscribeStatus != statusTranscribeTimeout {
		t.Fatalf("file 2 should now be permanent: %+v", r)
	}
	if bk := fx.bookRow(b); bk.TranscribeStatus == nil || *bk.TranscribeStatus != statusTranscribeTimeout {
		t.Fatalf("book status: %v", bk.TranscribeStatus)
	}
}

// A silent book whose retry lands on a file that already holds a result learns
// nothing from the timeout; it has been silent as far as the retries could
// listen, so it gets the sentinel instead of being re-sent by every run.
func TestBookPage_ASilentBookWhoseRetryHitsAFileWithAResultGetsTheSentinel(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")
	fx := newIntroFx(t)
	b := fx.book("Year One")
	fx.file(b, "01.m4b", 1, "y1-1", false)
	id2, _ := fx.file(b, "02.m4b", 2, "y1-2", false)
	held := goodIntro
	if _, err := fx.s.SetBookFileIntro(b, id2, database.BookFileIntroResult{
		SourceHash: "y1-2", RunOpID: "earlier", Status: statusOK, Transcript: &held, At: time.Now(), Done: true,
	}); err != nil {
		t.Fatal(err)
	}
	fn, _ := scriptedPath(silentAnswer, silentAnswer, transcribe.BatchResult{Error: decoderTimeoutText})
	swapPathSeams(t, true, "", fn)

	_, accum, _ := fx.bookPage(t, false, b)
	bk := fx.bookRow(b)
	if bk.IntroTranscription == nil || *bk.IntroTranscription != silenceSentinel {
		t.Fatalf("want the silence sentinel, got %v", bk.IntroTranscription)
	}
	if st := accum.snapshot(); st.Deferred != 0 || st.TranscribeTimeout != 0 {
		t.Fatalf("a settled book is not deferred: %+v", st)
	}
	if r := fx.row(b, id2); r.IntroTimeouts != 0 {
		t.Fatalf("no strike belongs on a file that holds a result: %+v", r)
	}
}

// A second file that is already a permanent timeout is not sent as retry 2.
func TestBookPage_RetryTwoSkipsASecondFileThatIsAlreadyPermanent(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")
	fx := newIntroFx(t)
	b := fx.book("Hush")
	fx.file(b, "01.m4b", 1, "hu-1", false)
	id2, _ := fx.file(b, "02.m4b", 2, "hu-2", false)
	fx.strike(b, id2, "hu-2", transcribeTimeoutStrikeLimit)
	fn, calls := scriptedPath(silentAnswer)
	swapPathSeams(t, true, "", fn)

	fx.bookPage(t, false, b)
	if n := len(calls()); n != 2 {
		t.Fatalf("a permanent second file must not be sent: %d whisper calls, want 2 (first pass, retry 1)", n)
	}
}

// ---- S1: one strike per file per run; the worker-fault breaker --------------

func TestBookPage_TheSameRunCountsOneStrikePerFile(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")
	fx := newIntroFx(t)
	b := fx.book("Knightfall")
	id, _ := fx.file(b, "01.m4b", 1, "kf-1", false)
	var calls atomic.Int32
	swapPathSeams(t, true, "", answerEvery(&calls, transcribe.BatchResult{Error: decoderTimeoutText}, nil))

	fx.bookPageAs(t, "run-resumed", false, nil, b)
	_, accum, logs := fx.bookPageAs(t, "run-resumed", false, nil, b)
	r := fx.row(b, id)
	if r.IntroTimeouts != 1 || r.TranscribeStatus != nil {
		t.Fatalf("two timeouts inside one run must be one strike: strikes=%d status=%v", r.IntroTimeouts, r.TranscribeStatus)
	}
	if st := accum.snapshot(); st.Deferred != 1 || st.TranscribeTimeout != 0 {
		t.Fatalf("stats: %+v", st)
	}
	if !strings.Contains(logs, "already counted") {
		t.Fatalf("the deferral should say why:\n%s", logs)
	}
}

func TestFileIntroRun_TheSameRunCountsOneStrikePerFile(t *testing.T) {
	fx := newIntroFx(t)
	b := fx.book("No Mans Land")
	id, _ := fx.timeoutFile(b, "01.m4b", 1, "nml-1")
	for range 2 {
		if _, err := fx.runAs(&fileIntroRep{id: "run-resumed"}, ""); err != nil {
			t.Fatal(err)
		}
	}
	if r := fx.row(b, id); r.IntroTimeouts != 1 || r.TranscribeStatus != nil {
		t.Fatalf("two timeouts inside one run must be one strike: strikes=%d status=%v", r.IntroTimeouts, r.TranscribeStatus)
	}
}

func TestTimeoutsAreWorkerFault(t *testing.T) {
	for _, c := range []struct {
		timeouts, clips int
		want            bool
	}{
		{0, 0, false}, {1, 1, false}, {1, 4, false},
		{2, 2, true}, {3, 3, true}, {3, 4, true},
		{2, 4, false}, // exactly half is still a set of verdicts
		{2, 5, false}, {4, 8, false}, {5, 8, true},
	} {
		if got := timeoutsAreWorkerFault(c.timeouts, c.clips); got != c.want {
			t.Errorf("timeoutsAreWorkerFault(%d, %d) = %v, want %v", c.timeouts, c.clips, got, c.want)
		}
	}
}

func TestFileIntroRun_ABatchOfMostlyTimeoutsIsAWorkerFaultNotThreeStrikes(t *testing.T) {
	fx := newIntroFx(t)
	b := fx.book("Batman Annual")
	var ids []string
	for i := range 3 {
		id, _ := fx.timeoutFile(b, fmt.Sprintf("%02d.m4b", i+1), i+1, fmt.Sprintf("ba-%d", i))
		ids = append(ids, id)
	}
	if _, err := fx.run(""); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if r := fx.row(b, id); r.IntroTimeouts != 0 || r.TranscribeStatus != nil {
			t.Fatalf("a worker-fault response must not count a strike: %+v", r)
		}
	}
	if st, _ := fx.s.GetFileIntroStats(); st.Deferred != 3 || st.TranscribeTimeout != 0 {
		t.Fatalf("stats: %+v", st)
	}
}

func TestFileIntroRun_AMinorityOfTimeoutsInABatchAreStillVerdicts(t *testing.T) {
	fx := newIntroFx(t)
	b := fx.book("Batman Annual")
	var bad []string
	for i := range 4 {
		name, hash := fmt.Sprintf("%02d.m4b", i+1), fmt.Sprintf("bm-%d", i)
		if i < 2 {
			id, _ := fx.timeoutFile(b, name, i+1, hash)
			bad = append(bad, id)
			continue
		}
		_, p := fx.file(b, name, i+1, hash, false)
		fx.whisp.texts[p] = goodIntro
	}
	if _, err := fx.run(""); err != nil {
		t.Fatal(err)
	}
	for _, id := range bad {
		if r := fx.row(b, id); r.IntroTimeouts != 1 {
			t.Fatalf("2 of 4 timing out is not a worker fault, each is a strike: %+v", r)
		}
	}
}

func TestBookPage_ARequestOfMostlyTimeoutsIsAWorkerFaultNotThreeStrikes(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")
	fx := newIntroFx(t)
	var books []string
	ids := map[string]string{}
	for i := range 3 {
		b := fx.book(fmt.Sprintf("Arkham Asylum %d", i+1))
		id, _ := fx.file(b, "01.m4b", 1, fmt.Sprintf("aa-%d", i), false)
		books = append(books, b)
		ids[b] = id
	}
	var calls atomic.Int32
	swapPathSeams(t, true, "", answerEvery(&calls, transcribe.BatchResult{Error: decoderTimeoutText}, nil))

	_, accum, logs := fx.bookPage(t, false, books...)
	for b, id := range ids {
		if r := fx.row(b, id); r.IntroTimeouts != 0 || r.TranscribeStatus != nil {
			t.Fatalf("a worker-fault response must not count a strike: %+v", r)
		}
	}
	if st := accum.snapshot(); st.Deferred != 3 || st.TranscribeTimeout != 0 {
		t.Fatalf("stats: %+v", st)
	}
	if !strings.Contains(logs, "worker fault") {
		t.Fatalf("the deferral should say why:\n%s", logs)
	}
}

func TestBookPage_AMinorityOfTimeoutsInARequestAreStillVerdicts(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")
	fx := newIntroFx(t)
	var books []string
	ids := map[string]string{}
	for i := range 4 {
		b := fx.book(fmt.Sprintf("Arkham Asylum %d", i+1))
		id, _ := fx.file(b, "01.m4b", 1, fmt.Sprintf("aa-%d", i), false)
		books = append(books, b)
		ids[b] = id
	}
	swapPathSeams(t, true, "", func(_ context.Context, jobs map[string]transcribe.PathJob, _ transcribe.BatchOptions) (map[string]transcribe.BatchResult, error) {
		out := map[string]transcribe.BatchResult{}
		for id := range jobs {
			if id == books[0] || id == books[1] {
				out[id] = transcribe.BatchResult{Error: decoderTimeoutText}
			} else {
				out[id] = transcribe.BatchResult{Text: goodIntro}
			}
		}
		return out, nil
	})
	fx.bookPage(t, false, books...)
	for _, b := range books[:2] {
		if r := fx.row(b, ids[b]); r.IntroTimeouts != 1 {
			t.Fatalf("2 of 4 timing out is not a worker fault, each is a strike: %+v", r)
		}
	}
}

// ---- S6: results that settled before a batch-level error are applied --------

func TestFileIntroRun_PartialResultsAreAppliedWhenTheBatchEndsStuck(t *testing.T) {
	fx := newIntroFx(t)
	b := fx.book("The Long Halloween")
	idA, _ := fx.file(b, "01.m4b", 1, "lh-a", false)
	idB, _ := fx.file(b, "02.m4b", 2, "lh-b", false)
	idC, _ := fx.file(b, "03.m4b", 3, "lh-c", false)
	fileIntroWhisperFn = func(_ context.Context, jobs map[string]fileIntroJob, _ func(int, int)) (map[string]fileIntroWhisperResult, error) {
		return map[string]fileIntroWhisperResult{idA: {Text: goodIntro}}, stuckTransportErr()
	}

	if _, err := fx.run(""); err != nil {
		t.Fatalf("one stuck batch must not fail the run: %v", err)
	}
	if r := fx.row(b, idA); r.IntroTranscription == nil || *r.IntroTranscription != goodIntro {
		t.Fatalf("the clip that settled before the stall was thrown away: %+v", r)
	}
	for _, id := range []string{idB, idC} {
		if r := fx.row(b, id); r.IntroTranscription != nil || r.TranscribeStatus != nil {
			t.Fatalf("an unanswered file must stay undone: %+v", r)
		}
	}
	if st, _ := fx.s.GetFileIntroStats(); st.Deferred != 2 || st.WhisperError != 0 || st.OK != 1 {
		t.Fatalf("stats: %+v", st)
	}
}

func TestFileIntroRun_PartialResultsAreAppliedOnAWholeBatchFailure(t *testing.T) {
	fx := newIntroFx(t)
	b := fx.book("Year One")
	idA, _ := fx.file(b, "01.m4b", 1, "y1-a", false)
	idB, _ := fx.file(b, "02.m4b", 2, "y1-b", false)
	fileIntroWhisperFn = func(_ context.Context, _ map[string]fileIntroJob, _ func(int, int)) (map[string]fileIntroWhisperResult, error) {
		return map[string]fileIntroWhisperResult{idA: {Text: goodIntro}}, errors.New("pool exploded")
	}
	if _, err := fx.run(""); err != nil {
		t.Fatal(err)
	}
	if r := fx.row(b, idA); r.IntroTranscription == nil {
		t.Fatalf("the answered file must keep its transcript: %+v", r)
	}
	if r := fx.row(b, idB); r.TranscribeStatus == nil || *r.TranscribeStatus != statusWhisperError {
		t.Fatalf("the unanswered file takes the batch failure: %+v", r)
	}
}

func TestFileIntroRun_AnOutageRetryResendsOnlyTheUnansweredFiles(t *testing.T) {
	fx := newIntroFx(t)
	b := fx.book("Hush")
	idA, _ := fx.file(b, "01.m4b", 1, "hu-a", false)
	idB, _ := fx.file(b, "02.m4b", 2, "hu-b", false)
	var mu sync.Mutex
	var sent [][]string
	var calls int
	fileIntroWhisperFn = func(_ context.Context, jobs map[string]fileIntroJob, _ func(int, int)) (map[string]fileIntroWhisperResult, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		keys := make([]string, 0, len(jobs))
		for k := range jobs {
			keys = append(keys, k)
		}
		sent = append(sent, keys)
		if calls == 1 {
			return map[string]fileIntroWhisperResult{idA: {Text: goodIntro}}, &transcribe.TransportError{
				Endpoints: []string{"http://192.0.2.10:8000"}, Err: errors.New("connection reset"), Recognized: true,
			}
		}
		out := map[string]fileIntroWhisperResult{}
		for k := range jobs {
			out[k] = fileIntroWhisperResult{Text: goodIntro}
		}
		return out, nil
	}
	if _, err := fx.run(""); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 2 || len(sent[1]) != 1 || sent[1][0] != idB {
		t.Fatalf("the retry must carry only the unanswered file, sent=%v", sent)
	}
	for _, id := range []string{idA, idB} {
		if r := fx.row(b, id); r.IntroTranscription == nil {
			t.Fatalf("file %s has no transcript: %+v", id, r)
		}
	}
}

func TestFileIntroResultsFromBatch(t *testing.T) {
	if got := fileIntroResultsFromBatch(nil); got != nil {
		t.Fatalf("no results must stay nil (an error that answered nothing), got %v", got)
	}
	got := fileIntroResultsFromBatch(map[string]transcribe.BatchResult{
		"a": {Text: goodIntro}, "b": {Error: decoderTimeoutText},
	})
	if len(got) != 2 || got["a"].Text != goodIntro || got["b"].Error != decoderTimeoutText {
		t.Fatalf("converted: %+v", got)
	}
}

func TestBookPage_PartialResultsAreAppliedWhenThePoolEndsStuck(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")
	fx := newIntroFx(t)
	b1 := fx.book("Knightfall")
	b2 := fx.book("Knightquest")
	fx.file(b1, "01.m4b", 1, "kf-1", false)
	id2, _ := fx.file(b2, "01.m4b", 1, "kq-1", false)
	swapPathSeams(t, true, "", func(_ context.Context, _ map[string]transcribe.PathJob, _ transcribe.BatchOptions) (map[string]transcribe.BatchResult, error) {
		return map[string]transcribe.BatchResult{b1: {Text: goodIntro}}, stuckTransportErr()
	})

	n, accum, _ := fx.bookPage(t, false, b1, b2)
	if n != 1 {
		t.Fatalf("processed %d, want the one answered book", n)
	}
	if bk := fx.bookRow(b1); bk.IntroTranscription == nil || *bk.IntroTranscription != goodIntro {
		t.Fatalf("the answered book lost its transcript: %+v", bk.IntroTranscription)
	}
	if bk := fx.bookRow(b2); bk.TranscribeStatus != nil || bk.IntroTranscription != nil {
		t.Fatalf("the unanswered book must stay undone, not whisper_error: status=%v", bk.TranscribeStatus)
	}
	if r := fx.row(b2, id2); r.TranscribeStatus != nil {
		t.Fatalf("file row written for an unanswered book: %+v", r)
	}
	st := accum.snapshot()
	if st.Deferred != 1 || st.WhisperError != 0 {
		t.Fatalf("stats: %+v", st)
	}
	if accum.stuck.n.Load() != 0 {
		t.Fatalf("a page that answered something must not extend the stuck streak (n=%d)", accum.stuck.n.Load())
	}
}

// A book the transport dropped (no result, no error) is deferred, never a
// whisper_error "no whisper result".
func TestBookPage_ABookMissingFromAnErrorFreeResponseIsDeferred(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")
	fx := newIntroFx(t)
	b1 := fx.book("No Mans Land")
	b2 := fx.book("Contagion")
	fx.file(b1, "01.m4b", 1, "nml-1", false)
	fx.file(b2, "01.m4b", 1, "ct-1", false)
	swapPathSeams(t, true, "", func(_ context.Context, _ map[string]transcribe.PathJob, _ transcribe.BatchOptions) (map[string]transcribe.BatchResult, error) {
		return map[string]transcribe.BatchResult{b1: {Text: goodIntro}}, nil
	})
	_, accum, _ := fx.bookPage(t, false, b1, b2)
	if bk := fx.bookRow(b2); bk.TranscribeStatus != nil {
		t.Fatalf("a dropped job was written as %q", *bk.TranscribeStatus)
	}
	if st := accum.snapshot(); st.Deferred != 1 || st.WhisperError != 0 {
		t.Fatalf("stats: %+v", st)
	}
}

// ---- S7: the strike is a book_file write, so it waits for a library scan ----

func TestBookPage_TheStrikeWaitsForTheWriteGate(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")
	fx := newIntroFx(t)
	b := fx.book("Hush")
	id, _ := fx.file(b, "01.m4b", 1, "hu-1", false)
	var calls atomic.Int32
	swapPathSeams(t, true, "", answerEvery(&calls, transcribe.BatchResult{Error: decoderTimeoutText}, nil))

	var gateCalls, strikesAtGate atomic.Int32
	fx.bookPageAs(t, "run-gated", false, func(a *transcribeStatsAccum) {
		a.gate = func(context.Context) error {
			gateCalls.Add(1)
			strikesAtGate.Store(int32(fx.row(b, id).IntroTimeouts))
			return nil
		}
	}, b)
	if gateCalls.Load() != 1 || strikesAtGate.Load() != 0 {
		t.Fatalf("the gate must be passed once, BEFORE the strike is written: calls=%d strikes-at-gate=%d", gateCalls.Load(), strikesAtGate.Load())
	}
	if r := fx.row(b, id); r.IntroTimeouts != 1 {
		t.Fatalf("strike not recorded after the gate opened: %+v", r)
	}
}

func TestBookPage_AFailedWriteGateDefersTheBookWithoutAStrike(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")
	fx := newIntroFx(t)
	b := fx.book("Hush")
	id, _ := fx.file(b, "01.m4b", 1, "hu-1", false)
	var calls atomic.Int32
	swapPathSeams(t, true, "", answerEvery(&calls, transcribe.BatchResult{Error: decoderTimeoutText}, nil))

	_, accum, logs := fx.bookPageAs(t, "run-gated", false, func(a *transcribeStatsAccum) {
		a.gate = func(context.Context) error { return errors.New("run cancelled") }
	}, b)
	if r := fx.row(b, id); r.IntroTimeouts != 0 {
		t.Fatalf("a strike was written through a failed gate: %+v", r)
	}
	if st := accum.snapshot(); st.Deferred != 1 {
		t.Fatalf("stats: %+v", st)
	}
	if !strings.Contains(logs, "write gate failed") {
		t.Fatalf("log:\n%s", logs)
	}
}

// ---- S8: a transcript clears the file's strikes -----------------------------

func TestBookPage_ATranscriptClearsTheFilesStrikes(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")
	fx := newIntroFx(t)
	b := fx.book("Year One")
	id, _ := fx.file(b, "01.m4b", 1, "y1-1", false)
	fx.strike(b, id, "y1-1", 1)
	var calls atomic.Int32
	swapPathSeams(t, true, "", answerEvery(&calls, transcribe.BatchResult{Text: goodIntro}, nil))

	fx.bookPage(t, false, b)
	if r := fx.row(b, id); r.IntroTimeouts != 0 || r.IntroTimeoutHash != "" {
		t.Fatalf("a successful transcript must clear the file's strikes: %+v", r)
	}
}

func TestBookPage_ARetryFailedTranscriptClearsAPermanentVerdict(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")
	fx := newIntroFx(t)
	b := fx.book("Year One")
	id, _ := fx.file(b, "01.m4b", 1, "y1-1", false)
	fx.strike(b, id, "y1-1", transcribeTimeoutStrikeLimit)
	var calls atomic.Int32
	swapPathSeams(t, true, "", answerEvery(&calls, transcribe.BatchResult{Text: goodIntro}, nil))

	fx.bookPage(t, true, b)
	r := fx.row(b, id)
	if r.IntroTimeouts != 0 || (r.TranscribeStatus != nil && *r.TranscribeStatus == statusTranscribeTimeout) {
		t.Fatalf("the verdict must not outlive a transcript of the same bytes: %+v", r)
	}
}

func TestBookPage_AFailedWriteGateLeavesTheStrikesInPlace(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")
	fx := newIntroFx(t)
	b := fx.book("Year One")
	id, _ := fx.file(b, "01.m4b", 1, "y1-1", false)
	fx.strike(b, id, "y1-1", 1)
	var calls atomic.Int32
	swapPathSeams(t, true, "", answerEvery(&calls, transcribe.BatchResult{Text: goodIntro}, nil))

	fx.bookPageAs(t, "run-gated", false, func(a *transcribeStatsAccum) {
		a.gate = func(context.Context) error { return errors.New("run cancelled") }
	}, b)
	if r := fx.row(b, id); r.IntroTimeouts != 1 {
		t.Fatalf("clearing is a book_file write and must go through the gate: %+v", r)
	}
}

// A book that already has a transcript is not relabelled as a timeout just
// because its first file holds the verdict, and is not counted as an attempt.
func TestBookPage_AFirstFileVerdictDoesNotRelabelABookThatHasATranscript(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")
	fx := newIntroFx(t)
	b := fx.book("Hush")
	id, _ := fx.file(b, "01.m4b", 1, "hu-1", false)
	fx.strike(b, id, "hu-1", transcribeTimeoutStrikeLimit)
	text, status := goodIntro, statusOK
	if _, err := fx.s.ModifyBook(b, func(cur *database.Book) error {
		cur.IntroTranscription, cur.TranscribeStatus = &text, &status
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	swapPathSeams(t, true, "", answerEvery(&calls, transcribe.BatchResult{Text: goodIntro}, nil))

	_, accum, _ := fx.bookPage(t, false, b)
	bk := fx.bookRow(b)
	if bk.TranscribeStatus == nil || *bk.TranscribeStatus != statusOK || bk.IntroTranscription == nil || *bk.IntroTranscription != goodIntro {
		t.Fatalf("a book with a transcript was relabelled: status=%v transcript=%v", bk.TranscribeStatus, bk.IntroTranscription)
	}
	if st := accum.snapshot(); st.Attempted != 0 || st.SkippedExisting != 1 {
		t.Fatalf("stats: %+v", st)
	}
}

// A batch that answered even one clip did get an answer from the pool: it
// never counts toward the three-in-a-row stop.
func TestFileIntroRun_StuckBatchesThatAnswerSomethingNeverStopTheRun(t *testing.T) {
	fx := newIntroFx(t)
	fileIntroSlots = func() int { return 1 }
	rows := fx.manyFiles("Contagion", fileIntroBatchGroups*5)
	var calls atomic.Int64
	fileIntroWhisperFn = func(_ context.Context, jobs map[string]fileIntroJob, _ func(int, int)) (map[string]fileIntroWhisperResult, error) {
		calls.Add(1)
		for k := range jobs {
			return map[string]fileIntroWhisperResult{k: {Text: goodIntro}}, stuckTransportErr()
		}
		return nil, stuckTransportErr()
	}
	if _, err := fx.run(""); err != nil {
		t.Fatalf("batches that each answered a clip must not stop the run: %v", err)
	}
	if n := calls.Load(); n != 5 {
		t.Fatalf("expected all 5 batches to be attempted, got %d", n)
	}
	done := 0
	for id, b := range rows {
		if fx.row(b, id).IntroTranscription != nil {
			done++
		}
	}
	if done != 5 {
		t.Fatalf("the one answered clip per batch must be applied: %d of 5", done)
	}
}

// The breaker reads each response on its own: a retry round in which most
// clips timed out is as much a worker fault as the first pass.
func TestBookPage_ARetryRoundOfMostlyTimeoutsIsAWorkerFaultToo(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")
	fx := newIntroFx(t)
	var books []string
	ids := map[string]string{}
	for i := range 3 {
		b := fx.book(fmt.Sprintf("Contagion %d", i+1))
		id, _ := fx.file(b, "01.m4b", 1, fmt.Sprintf("ct-%d", i), false)
		books = append(books, b)
		ids[b] = id
	}
	fn, calls := scriptedPath(silentAnswer, transcribe.BatchResult{Error: decoderTimeoutText})
	swapPathSeams(t, true, "", fn)

	_, accum, _ := fx.bookPage(t, false, books...)
	if n := len(calls()); n != 2 {
		t.Fatalf("whisper calls = %d, want first pass and retry 1", n)
	}
	for b, id := range ids {
		if r := fx.row(b, id); r.IntroTimeouts != 0 || r.TranscribeStatus != nil {
			t.Fatalf("a worker-fault retry round must not count a strike: %+v", r)
		}
	}
	if st := accum.snapshot(); st.Deferred != 3 || st.TranscribeTimeout != 0 {
		t.Fatalf("stats: %+v", st)
	}
}

// ---- N3: the liveness ticker ends even when the whisper call panics ---------

func TestBookPage_TheTickerStopsWhenTheWhisperCallPanics(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")
	fx := newIntroFx(t)
	orig := whisperWaitTickInterval
	whisperWaitTickInterval = 5 * time.Millisecond
	t.Cleanup(func() { whisperWaitTickInterval = orig })
	b := fx.book("Hush")
	fx.file(b, "01.m4b", 1, "hu-1", false)
	swapPathSeams(t, true, "", func(context.Context, map[string]transcribe.PathJob, transcribe.BatchOptions) (map[string]transcribe.BatchResult, error) {
		time.Sleep(40 * time.Millisecond)
		panic("transport blew up")
	})
	var beats atomic.Int64
	func() {
		defer func() { _ = recover() }()
		fx.bookPageAs(t, "run-panic", false, func(a *transcribeStatsAccum) {
			a.heartbeat = func(string) { beats.Add(1) }
		}, b)
	}()
	atPanic := beats.Load()
	if atPanic == 0 {
		t.Fatal("setup: no heartbeat before the panic")
	}
	time.Sleep(60 * time.Millisecond)
	if after := beats.Load(); after != atPanic {
		t.Fatalf("the ticker outlived the panicking call: %d beats at the panic, %d after", atPanic, after)
	}
}

func TestFileIntroWorker_TheTickerStopsWhenTheWhisperCallPanics(t *testing.T) {
	fx := newIntroFx(t)
	orig := whisperWaitTickInterval
	whisperWaitTickInterval = 5 * time.Millisecond
	t.Cleanup(func() { whisperWaitTickInterval = orig })
	b := fx.book("Hush")
	fx.file(b, "01.m4b", 1, "hu-1", false)
	fileIntroWhisperFn = func(context.Context, map[string]fileIntroJob, func(int, int)) (map[string]fileIntroWhisperResult, error) {
		time.Sleep(40 * time.Millisecond)
		panic("transport blew up")
	}
	rep := &recordingRep{fileIntroRep: fileIntroRep{id: "run-panic"}}
	w := &fileIntroWorker{reporter: rep}
	func() {
		defer func() { _ = recover() }()
		_, _ = w.callWhisper(context.Background(), map[string]fileIntroJob{"x": {Path: "/lib/Gotham/x.m4b", Seconds: 90}})
	}()
	atPanic := rep.count("waiting on whisper")
	if atPanic == 0 {
		t.Fatal("setup: no heartbeat before the panic")
	}
	time.Sleep(60 * time.Millisecond)
	if after := rep.count("waiting on whisper"); after != atPanic {
		t.Fatalf("the ticker outlived the panicking call: %d beats at the panic, %d after", atPanic, after)
	}
}

// ---- N4: the book op's tick stamps liveness and never steps progress --------

type liveRep struct {
	fakeReporter
	touches  atomic.Int64
	progress atomic.Int64
	itemMu   sync.Mutex
	item     string
}

func (r *liveRep) TouchLiveness() { r.touches.Add(1) }
func (r *liveRep) UpdateProgress(_, _ int, _ string) error {
	r.progress.Add(1)
	return nil
}
func (r *liveRep) SetCurrentItem(s string) { r.itemMu.Lock(); r.item = s; r.itemMu.Unlock() }

func TestBookIntroHeartbeat_StampsLivenessAndNeverWritesProgress(t *testing.T) {
	rep := &liveRep{}
	bookIntroHeartbeat(rep)("waiting on whisper: 3 books in flight for 10s")
	if rep.touches.Load() != 1 {
		t.Fatalf("liveness touches = %d, want 1", rep.touches.Load())
	}
	if rep.progress.Load() != 0 {
		t.Fatal("the tick wrote a progress number; with several pages in flight that steps the bar backwards")
	}
	rep.itemMu.Lock()
	defer rep.itemMu.Unlock()
	if !strings.Contains(rep.item, "waiting on whisper") {
		t.Fatalf("current item = %q", rep.item)
	}
}
