// file: internal/plugins/maintenance/whisper_strike_policy_test.go
// version: 1.0.0
// guid: 7e1b94d2-3a65-4c80-9f17-d2a8b6c05e34
// last-edited: 2026-10-26

package maintenance

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/serverdecode"
	"github.com/falkcorp/audiobook-organizer/internal/transcribe"
)

// Tests for the strike policy after the delta re-review of the whisper timeout
// work: what a failed dispatch's timeouts mean, decoder crashes as strikes, the
// 12 h gap between strikes, one run-id scheme in both ops, and the book op's
// retry-round and cancellation edges. Fixtures are synthetic (Gotham comic
// titles, throwaway temp paths).

// ---- a failed dispatch's timeouts are not verdicts ---------------------------

func TestDropPoolStrikes_KeepsEverythingButUnCondemnedStrikes(t *testing.T) {
	in := map[string]transcribe.BatchResult{
		"text":      {Text: goodIntro},
		"timeout":   {Error: decoderTimeoutText},
		"crash":     {Error: decoderCrashText},
		"condemned": {Error: decoderTimeoutText, Condemned: true},
		"ffmpeg":    {Error: "ffmpeg_error: no audio stream"},
	}
	got := dropPoolStrikes(in, batchStrike)
	for _, id := range []string{"timeout", "crash"} {
		if _, ok := got[id]; ok {
			t.Errorf("%s survived: a failed dispatch's timeout or crash is not a verdict", id)
		}
	}
	for _, id := range []string{"text", "condemned", "ffmpeg"} {
		if _, ok := got[id]; !ok {
			t.Errorf("%s was dropped, it is a real answer", id)
		}
	}
	if len(in) != 5 {
		t.Errorf("the input map was modified: %d entries", len(in))
	}
	clean := map[string]transcribe.BatchResult{"a": {Text: "x"}}
	if out := dropPoolStrikes(clean, batchStrike); len(out) != 1 {
		t.Errorf("nothing to drop must return the input: %v", out)
	}
}

func TestBookPage_AnErroredDispatchDefersItsTimeoutsAndCrashesWithoutAStrike(t *testing.T) {
	for _, text := range []string{decoderTimeoutText, decoderCrashText} {
		t.Run(text[:strings.Index(text, ":")], func(t *testing.T) {
			t.Setenv(serverdecode.EnvVar, "")
			fx := newIntroFx(t)
			var books []string
			ids := map[string]string{}
			for i := range 2 {
				b := fx.book(fmt.Sprintf("Arkham Knight %d", i+1))
				id, _ := fx.file(b, "01.m4b", 1, fmt.Sprintf("ak-%d", i), false)
				books = append(books, b)
				ids[b] = id
			}
			swapPathSeams(t, true, "", erroredPath(transcribe.BatchResult{Error: text}, stuckTransportErr()))

			_, accum, logs := fx.bookPage(t, false, books...)
			for b, id := range ids {
				if r := fx.row(b, id); r.IntroTimeouts != 0 || r.TranscribeStatus != nil {
					t.Fatalf("a failed dispatch's answer was struck: %+v", r)
				}
			}
			if st := accum.snapshot(); st.Deferred != 2 || st.TranscribeTimeout != 0 {
				t.Fatalf("stats: %+v", st)
			}
			if !strings.Contains(logs, "not read as verdicts") {
				t.Fatalf("the deferral should say why:\n%s", logs)
			}
		})
	}
}

func TestBookPage_ACondemnedFileIsStruckEvenWhenTheDispatchErrored(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")
	fx := newIntroFx(t)
	b := fx.book("Arkham City")
	id, _ := fx.file(b, "01.m4b", 1, "ac-1", false)
	swapPathSeams(t, true, "", erroredPath(transcribe.BatchResult{Error: decoderTimeoutText, Condemned: true}, stuckTransportErr()))

	fx.bookPage(t, false, b)
	if r := fx.row(b, id); r.IntroTimeouts != 1 {
		t.Fatalf("the dispatcher's own verdict (sent alone to several workers) must be struck: %+v", r)
	}
}

func TestBookPage_ARetryRoundThatErroredDefersItsTimeoutsWithoutAStrike(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")
	fx := newIntroFx(t)
	b := fx.book("Contagion")
	id, _ := fx.file(b, "01.m4b", 1, "ct-1", false)
	var calls atomic.Int32
	swapPathSeams(t, true, "", func(_ context.Context, jobs map[string]transcribe.PathJob, _ transcribe.BatchOptions) (map[string]transcribe.BatchResult, error) {
		res, err := transcribe.BatchResult{}, error(nil)
		if calls.Add(1) > 1 {
			res, err = transcribe.BatchResult{Error: decoderTimeoutText}, stuckTransportErr()
		}
		out := map[string]transcribe.BatchResult{}
		for k := range jobs {
			out[k] = res
		}
		return out, err
	})

	_, accum, _ := fx.bookPage(t, false, b)
	if r := fx.row(b, id); r.IntroTimeouts != 0 || r.TranscribeStatus != nil {
		t.Fatalf("a failed retry round's timeout was struck: %+v", r)
	}
	if bk := fx.bookRow(b); bk.IntroTranscription != nil {
		t.Fatalf("the book must stay undecided, it was given %q", *bk.IntroTranscription)
	}
	if st := accum.snapshot(); st.Deferred != 1 {
		t.Fatalf("stats: %+v", st)
	}
}

func TestFileIntroRun_AnErroredDispatchDefersItsTimeoutsAndCrashesWithoutAStrike(t *testing.T) {
	for _, text := range []string{decoderTimeoutText, decoderCrashText} {
		t.Run(text[:strings.Index(text, ":")], func(t *testing.T) {
			fx := newIntroFx(t)
			b := fx.book("Batman Annual")
			var bad []string
			for i := range 2 {
				id, p := fx.file(b, fmt.Sprintf("%02d.m4b", i+1), i+1, fmt.Sprintf("bz-%d", i), false)
				fx.whisp.errs[p] = text
				bad = append(bad, id)
			}
			goodID, gp := fx.file(b, "03.m4b", 3, "bz-2", false)
			fx.whisp.texts[gp] = goodIntro
			fx.whisp.alsoErr = stuckTransportErr()

			// The run ends stuck (three stuck batches in a row), and that is
			// the only error it may end with.
			_, err := fx.run("")
			if err != nil && !errors.Is(err, errWhisperStuck) {
				t.Fatalf("unexpected run error: %v", err)
			}
			for _, id := range bad {
				if r := fx.row(b, id); r.IntroTimeouts != 0 || r.TranscribeStatus != nil {
					t.Fatalf("a failed dispatch's answer was struck: %+v", r)
				}
			}
			if r := fx.row(b, goodID); r.IntroTranscription == nil {
				t.Fatalf("the clip that DID settle must be applied: %+v", r)
			}
		})
	}
}

func TestFileIntroRun_ACondemnedAnswerIsStruckEvenWhenTheDispatchErrored(t *testing.T) {
	fx := newIntroFx(t)
	b := fx.book("Arkham City")
	id, p := fx.file(b, "01.m4b", 1, "ac-1", false)
	fx.whisp.errs[p] = decoderTimeoutText
	saved := fileIntroWhisperFn
	fileIntroWhisperFn = func(ctx context.Context, jobs map[string]fileIntroJob, f func(int, int)) (map[string]fileIntroWhisperResult, error) {
		out, _ := saved(ctx, jobs, f)
		for k, r := range out {
			r.Condemned = true
			out[k] = r
		}
		return out, stuckTransportErr()
	}
	t.Cleanup(func() { fileIntroWhisperFn = saved })

	_, _ = fx.run("")
	if r := fx.row(b, id); r.IntroTimeouts != 1 {
		t.Fatalf("the dispatcher's own verdict must be struck: %+v", r)
	}
}

// ---- decoder_crashed is a strike like transcribe_timeout --------------------

func TestBookPage_ADecoderCrashIsAStrikeAndThenPermanent(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")
	fx := newIntroFx(t)
	b := fx.book("Zero Year")
	id, _ := fx.file(b, "01.m4b", 1, "zy-1", false)
	var calls atomic.Int32
	swapPathSeams(t, true, "", answerEvery(&calls, transcribe.BatchResult{Error: decoderCrashText}, nil))

	fx.bookPageAs(t, "run-1", false, nil, b)
	if r := fx.row(b, id); r.IntroTimeouts != 1 || r.TranscribeStatus != nil {
		t.Fatalf("a crash that held the clip is a strike: %+v", r)
	}
	fx.bookPageAs(t, "run-2", false, nil, b)
	if bk := fx.bookRow(b); bk.TranscribeStatus == nil || *bk.TranscribeStatus != statusTranscribeTimeout {
		t.Fatalf("a clip that keeps killing the decoder must end as a verdict, not be re-sent forever: %v", bk.TranscribeStatus)
	}
}

func TestFileIntroRun_ADecoderCrashIsAStrikeAndThenPermanent(t *testing.T) {
	fx := newIntroFx(t)
	b := fx.book("Zero Year")
	id, p := fx.file(b, "01.m4b", 1, "zy-1", false)
	fx.whisp.errs[p] = decoderCrashText

	if _, err := fx.runAs(&fileIntroRep{id: "run-1"}, ""); err != nil {
		t.Fatal(err)
	}
	if r := fx.row(b, id); r.IntroTimeouts != 1 || r.TranscribeStatus != nil {
		t.Fatalf("a crash that held the clip is a strike: %+v", r)
	}
	if _, err := fx.runAs(&fileIntroRep{id: "run-2"}, ""); err != nil {
		t.Fatal(err)
	}
	if r := fx.row(b, id); r.TranscribeStatus == nil || *r.TranscribeStatus != statusTranscribeTimeout {
		t.Fatalf("a clip that keeps killing the decoder must end as a verdict: %+v", r)
	}
}

// ---- 12 h between strikes: one stall seen by both ops is one strike ---------

func TestBookPage_AStrikeFromAnotherRunInsideTheGapIsDeferredNotCounted(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")
	fx := newIntroFx(t)
	withStrikeGap(t) // after newIntroFx, which switches the gap off
	b := fx.book("Hush")
	id, _ := fx.file(b, "01.m4b", 1, "hu-1", false)
	fx.strikeLast(b, id, "hu-1", 1, time.Hour) // the per-file op struck it an hour ago
	var calls atomic.Int32
	swapPathSeams(t, true, "", answerEvery(&calls, transcribe.BatchResult{Error: decoderTimeoutText}, nil))

	_, accum, logs := fx.bookPageAs(t, "book-op-run", false, nil, b)
	if r := fx.row(b, id); r.IntroTimeouts != 1 || r.TranscribeStatus != nil {
		t.Fatalf("a second op's strike inside the gap must not count: %+v", r)
	}
	if st := accum.snapshot(); st.Deferred != 1 || st.TranscribeTimeout != 0 {
		t.Fatalf("stats: %+v", st)
	}
	if !strings.Contains(logs, "too soon") {
		t.Fatalf("the deferral should say why:\n%s", logs)
	}
}

func TestBookPage_AStrikeAfterTheGapCountsAndEndsAsAVerdict(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")
	fx := newIntroFx(t)
	withStrikeGap(t) // after newIntroFx, which switches the gap off
	b := fx.book("Hush")
	id, _ := fx.file(b, "01.m4b", 1, "hu-1", false)
	fx.strikeLast(b, id, "hu-1", 1, 13*time.Hour)
	var calls atomic.Int32
	swapPathSeams(t, true, "", answerEvery(&calls, transcribe.BatchResult{Error: decoderTimeoutText}, nil))

	fx.bookPageAs(t, "book-op-run", false, nil, b)
	if bk := fx.bookRow(b); bk.TranscribeStatus == nil || *bk.TranscribeStatus != statusTranscribeTimeout {
		t.Fatalf("a strike a day later is real evidence: %v", bk.TranscribeStatus)
	}
}

func TestFileIntroRun_AStrikeFromAnotherRunInsideTheGapIsDeferredNotCounted(t *testing.T) {
	fx := newIntroFx(t)
	withStrikeGap(t) // after newIntroFx, which switches the gap off
	b := fx.book("Hush")
	id, _ := fx.timeoutFile(b, "01.m4b", 1, "hu-1")
	fx.strikeLast(b, id, "hu-1", 1, time.Hour)

	if _, err := fx.runAs(&fileIntroRep{id: "file-op-run"}, ""); err != nil {
		t.Fatal(err)
	}
	if r := fx.row(b, id); r.IntroTimeouts != 1 || r.TranscribeStatus != nil {
		t.Fatalf("a second op's strike inside the gap must not count: %+v", r)
	}
	if st, _ := fx.s.GetFileIntroStats(); st.Deferred != 1 || st.TranscribeTimeout != 0 {
		t.Fatalf("stats: %+v", st)
	}
}

func TestFileIntroRun_AStrikeAfterTheGapCountsAndEndsAsAVerdict(t *testing.T) {
	fx := newIntroFx(t)
	withStrikeGap(t) // after newIntroFx, which switches the gap off
	b := fx.book("Hush")
	id, _ := fx.timeoutFile(b, "01.m4b", 1, "hu-1")
	fx.strikeLast(b, id, "hu-1", 1, 13*time.Hour)

	if _, err := fx.runAs(&fileIntroRep{id: "file-op-run"}, ""); err != nil {
		t.Fatal(err)
	}
	if r := fx.row(b, id); r.TranscribeStatus == nil || *r.TranscribeStatus != statusTranscribeTimeout {
		t.Fatalf("a strike a day later is real evidence: %+v", r)
	}
}

// ---- one run-id scheme: the book op stamps the registry op id ---------------

type bookOpIDReporter struct {
	denomReporter
	id string
}

func (r *bookOpIDReporter) OpID() string { return r.id }

func TestIntroTranscribe_TheRunIDIsTheRegistryOpID(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")
	p, s, ids := newRemoteRun(t, 1)
	b, _ := s.GetBookByID(ids[0])
	if err := s.CreateBookFile(&database.BookFile{BookID: b.ID, FilePath: b.FilePath, Format: "m4b", Duration: 600, TrackNumber: 1, FileHash: "ns2-hash"}); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	swapPathSeams(t, true, "", answerEvery(&calls, transcribe.BatchResult{Error: decoderTimeoutText}, nil))

	rep := &bookOpIDReporter{id: "op-book-intros-7"}
	if err := p.runIntroTranscribe(context.Background(), liveTranscribeParams(`{}`), rep); err != nil {
		t.Fatalf("run failed: %v", err)
	}
	rows, err := s.GetBookFiles(b.ID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
	if rows[0].IntroTimeoutRunID != "op-book-intros-7" {
		t.Fatalf("the strike carries run id %q, want the registry op id (a start-time string can never match clear_run_id or the other op's SameRun)", rows[0].IntroTimeoutRunID)
	}
}

// ---- NS7: a retry-1 timeout belongs to the clip, not the file that follows ---

func TestBookPage_ARetryOneTimeoutIsNeitherCopiedNorStruck(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")
	fx := newIntroFx(t)
	b := fx.book("Dark Victory")
	id1, _ := fx.file(b, "01.m4b", 1, "dv-1", false)
	id2, p2 := fx.file(b, "02.m4b", 2, "dv-2", false)
	// 90 s: silent. 300 s: the decoder stalls on the clip. Retry 2 then goes to
	// the second file, which is silent too.
	fn, calls := scriptedPath(silentAnswer, transcribe.BatchResult{Error: decoderTimeoutText}, silentAnswer)
	swapPathSeams(t, true, "", fn)

	_, accum, logs := fx.bookPage(t, false, b)
	got := calls()
	if len(got) != 3 || got[2][b].Path != p2 {
		t.Fatalf("a stalled retry 1 must still go on to retry 2 on the second file: calls=%v", got)
	}
	for _, id := range []string{id1, id2} {
		if r := fx.row(b, id); r.IntroTimeouts != 0 || r.TranscribeStatus != nil {
			t.Fatalf("the retry-1 stall must not be struck against any file: %+v", r)
		}
	}
	if st := accum.snapshot(); st.TranscribeTimeout != 0 {
		t.Fatalf("stats: %+v", st)
	}
	if !strings.Contains(logs, "retry1") && !strings.Contains(logs, "retry 1") {
		t.Fatalf("the log should say retry 1 stalled:\n%s", logs)
	}
}

// ---- NS5: a misconfigured worker is never a verdict on the upload path ------

func TestProcessTranscribePage_UploadModeWorkerMisconfiguredIsDeferredNotWhisperError(t *testing.T) {
	store, written, books, rootDir := transportTestFixture(t, "b-misconf", "hash-misconf")
	swapTranscribeBatchFn(t, func(_ context.Context, jobs map[string]string, _ transcribe.BatchOptions) (map[string]transcribe.BatchResult, error) {
		out := make(map[string]transcribe.BatchResult, len(jobs))
		for id := range jobs {
			out[id] = transcribe.BatchResult{Error: "worker_misconfigured: the decode process cannot start on this host"}
		}
		return out, nil
	})

	p := New(fakeDeps{store: store})
	accum := newTranscribeStatsAccum(nil, "op", 0, time.Now())
	processed := p.processTranscribePage(context.Background(), store, slog.Default(),
		books, rootDir, nil, accum, false, false, pageJournal{}, extractLocal)

	if processed != 0 {
		t.Errorf("processed = %d, want 0", processed)
	}
	if len(*written) != 0 {
		t.Fatalf("a worker that cannot decode says nothing about the book: want no write, got %d (first: %+v)", len(*written), (*written)[0])
	}
	if st := accum.snapshot(); st.Deferred != 1 {
		t.Fatalf("stats: %+v", st)
	}
}

// ---- NN2 / NN9: cancellation and the non-blocking gate ----------------------

func TestBookPage_ACancelledRunDiscardsThePageAndWritesNothing(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")
	fx := newIntroFx(t)
	b := fx.book("Court of Owls")
	id, _ := fx.file(b, "01.m4b", 1, "co-1", false)
	ctx, cancel := context.WithCancel(context.Background())
	swapPathSeams(t, true, "", func(_ context.Context, jobs map[string]transcribe.PathJob, _ transcribe.BatchOptions) (map[string]transcribe.BatchResult, error) {
		cancel() // the operator stops the run while the worker is answering
		out := map[string]transcribe.BatchResult{}
		for k := range jobs {
			out[k] = transcribe.BatchResult{Text: goodIntro}
		}
		return out, nil
	})
	var gates atomic.Int32

	books := []database.Book{*fx.bookRow(b)}
	rootDir, _ := noClipWrites(t)
	accum := newTranscribeStatsAccum(nil, "run-cancel", 1, time.Now())
	accum.gate = func(context.Context) error { gates.Add(1); return nil }
	n := fx.p.processTranscribePage(ctx, fx.s, slog.Default(), books, rootDir, nil, accum, false, false, pageJournal{}, extractRemote)

	if n != 0 {
		t.Fatalf("a cancelled page applies nothing, processed=%d", n)
	}
	if bk := fx.bookRow(b); bk.IntroTranscription != nil || bk.TranscribeStatus != nil {
		t.Fatalf("a cancelled run wrote to the book: %+v", bk)
	}
	if r := fx.row(b, id); r.IntroTimeouts != 0 {
		t.Fatalf("a cancelled run wrote to the file: %+v", r)
	}
	if st := accum.snapshot(); st.Deferred != 1 {
		t.Fatalf("the page must be counted deferred: %+v", st)
	}
}

func TestWaitWhileScanBusy_ACancelledContextReturnsBeforeReadingTheQueue(t *testing.T) {
	q := &fakeQueue{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitWhileScanBusy(ctx, "test-op", q, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if q.calls != 0 {
		t.Fatalf("the queue was read %d times after the cancel", q.calls)
	}
}

func TestScanBusyNow_NeverWaits(t *testing.T) {
	busy := &scanQueue{busy: true}
	start := time.Now()
	err := scanBusyNow("test-op", busy)
	if err == nil || !strings.Contains(err.Error(), "running") {
		t.Fatalf("a running scan must be reported: %v", err)
	}
	if busy.busyPolls != 1 {
		t.Fatalf("one poll, no waiting: %d polls", busy.busyPolls)
	}
	if time.Since(start) > fileIntroPauseInterval*20 {
		t.Fatalf("scanBusyNow waited %s", time.Since(start))
	}
	if err := scanBusyNow("test-op", &scanQueue{}); err != nil {
		t.Fatalf("an idle queue is not busy: %v", err)
	}
	if err := scanBusyNow("test-op", &fakeQueue{failN: 1}); err == nil {
		t.Fatal("an unreadable queue must refuse the write")
	}
	if err := scanBusyNow("test-op", nil); err == nil {
		t.Fatal("no queue means the write cannot be shown safe")
	}
}
