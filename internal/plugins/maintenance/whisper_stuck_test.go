// file: internal/plugins/maintenance/whisper_stuck_test.go
// version: 1.1.0
// guid: 7b0e3d52-41c8-4f6a-9d2b-e85a1c4f7390
// last-edited: 2026-10-26

package maintenance

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/serverdecode"
	"github.com/falkcorp/audiobook-organizer/internal/transcribe"
)

// Tests for the transcribe_timeout / worker-stuck handling shared by the
// per-file and per-book transcription ops. Fixtures are synthetic: Gotham
// comic titles, throwaway paths under t.TempDir().

const decoderTimeoutText = "transcribe_timeout: decoder exceeded 60s on this clip and was killed"

// stuckTransportErr is what the dispatcher returns when it could not settle a
// batch: a RECOGNIZED transport error wrapping a WorkerStuckError. Recognized
// is the dangerous case, because the outage loop retries exactly those.
func stuckTransportErr() error {
	return &transcribe.TransportError{
		Endpoints:  []string{"http://192.0.2.10:8000"},
		Err:        &transcribe.WorkerStuckError{URL: "http://192.0.2.10:8000", After: 10 * time.Minute, Jobs: []string{"x"}},
		Recognized: true,
	}
}

// ---- constants and classification pins --------------------------------------

// The status string is spelled in three packages (the worker's error code, the
// store's status and the ops' status). A drift between them would silently turn
// a permanent verdict into a retryable whisper_error or vice versa.
func TestTranscribeTimeoutStatusSpellingsAgree(t *testing.T) {
	if statusTranscribeTimeout != transcribe.PathErrTranscribeTimeout {
		t.Fatalf("op status %q != worker error code %q", statusTranscribeTimeout, transcribe.PathErrTranscribeTimeout)
	}
	if statusTranscribeTimeout != database.IntroStatusTranscribeTimeout {
		t.Fatalf("op status %q != store status %q", statusTranscribeTimeout, database.IntroStatusTranscribeTimeout)
	}
	if got := transcribe.PathErrorCode(decoderTimeoutText); got != transcribe.PathErrTranscribeTimeout {
		t.Fatalf("PathErrorCode(%q) = %q", decoderTimeoutText, got)
	}
}

func TestTranscribeTimeoutIsADurableFailure(t *testing.T) {
	if !isDurableTranscribeFailure(statusTranscribeTimeout) {
		t.Fatal("transcribe_timeout must be durable: selection has to skip it")
	}
	if isDurableTranscribeFailure(statusWhisperError) {
		t.Fatal("whisper_error must stay retryable")
	}
}

func TestTranscribeInputChanged_TimeoutNeedsNewBytes(t *testing.T) {
	// Same rule as ffmpeg_error: the file was present when it failed, so only a
	// replace (mtime after the failed attempt) makes the book eligible again.
	for _, status := range []string{statusFFmpegError, statusTranscribeTimeout} {
		t.Run(status, func(t *testing.T) {
			src := writeSource(t, "hush.m4b")
			store, _, _ := remoteBookStore(t, map[string][]string{"b1": {src}})
			st := status
			attempted := time.Now().Add(time.Hour) // the file is OLDER than the attempt
			b := &database.Book{ID: "b1", TranscribeStatus: &st, TranscribeAttemptedAt: &attempted}
			if transcribeInputChanged(store, b) {
				t.Fatal("an unchanged file must keep being skipped")
			}
			attempted = time.Now().Add(-time.Hour) // the file is NEWER than the attempt
			if !transcribeInputChanged(store, b) {
				t.Fatal("a replaced file must be retried")
			}
		})
	}
}

func TestTranscribeStatsAccum_CountsTimeouts(t *testing.T) {
	a := newTranscribeStatsAccum(nil, "op", 3, time.Now())
	a.recordOutcome(statusTranscribeTimeout, time.Now())
	a.recordOutcome(statusTranscribeTimeout, time.Now())
	a.recordOutcome(statusWhisperError, time.Now())
	st := a.snapshot()
	if st.TranscribeTimeout != 2 || st.WhisperError != 1 || st.Attempted != 3 {
		t.Fatalf("stats: %+v", st)
	}
}

// ---- tickWhileWaiting -------------------------------------------------------

func TestTickWhileWaiting_TicksThenStopsForGood(t *testing.T) {
	var n atomic.Int64
	stop := tickWhileWaiting(2*time.Millisecond, func(time.Duration) { n.Add(1) })
	deadline := time.Now().Add(5 * time.Second)
	for n.Load() < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("only %d ticks", n.Load())
		}
		time.Sleep(time.Millisecond)
	}
	stop()
	stop() // idempotent
	after := n.Load()
	time.Sleep(25 * time.Millisecond)
	if n.Load() != after {
		t.Fatalf("ticked after stop returned: %d -> %d", after, n.Load())
	}
}

// ---- per-file op ------------------------------------------------------------

func (fx *introFx) timeoutFile(b, name string, track int, hash string) (id, path string) {
	id, path = fx.file(b, name, track, hash, false)
	fx.whisp.errs[path] = decoderTimeoutText
	return id, path
}

func TestFileIntroRun_TimeoutStrikesThenPermanent(t *testing.T) {
	fx := newIntroFx(t)
	b := fx.book("The Long Halloween")
	id, p := fx.timeoutFile(b, "01.m4b", 1, "lh-1")

	// Strike 1: counted, retried next run, nothing else written.
	if _, err := fx.runAs(&fileIntroRep{id: "run-1"}, ""); err != nil {
		t.Fatal(err)
	}
	r := fx.row(b, id)
	if r.TranscribeStatus != nil || r.IntroTimeouts != 1 || r.IntroTranscription != nil {
		t.Fatalf("after strike 1 want only a strike, got status=%v strikes=%d", r.TranscribeStatus, r.IntroTimeouts)
	}
	if st, _ := fx.s.GetFileIntroStats(); st.Deferred != 1 || st.TranscribeTimeout != 0 {
		t.Fatalf("stats after strike 1: %+v", st)
	}

	// Strike 2: permanent.
	if _, err := fx.runAs(&fileIntroRep{id: "run-2"}, ""); err != nil {
		t.Fatal(err)
	}
	r = fx.row(b, id)
	if r.TranscribeStatus == nil || *r.TranscribeStatus != statusTranscribeTimeout || r.IntroTimeouts != 2 {
		t.Fatalf("after strike 2 want permanent, got status=%v strikes=%d", r.TranscribeStatus, r.IntroTimeouts)
	}
	if st, _ := fx.s.GetFileIntroStats(); st.TranscribeTimeout != 1 || st.Attempted != 1 {
		t.Fatalf("stats after strike 2: %+v", st)
	}

	// Run 3: skipped like any durable verdict, never sent.
	fx.whisp.calls = nil
	if _, err := fx.runAs(&fileIntroRep{id: "run-3"}, ""); err != nil {
		t.Fatal(err)
	}
	if got := fx.whisp.sentPaths(); got[p] != 0 {
		t.Fatalf("a permanent transcribe_timeout file was re-sent: %v", got)
	}
	if st, _ := fx.s.GetFileIntroStats(); st.SkippedFailed != 1 {
		t.Fatalf("stats after run 3: %+v", st)
	}

	// retry_failed sends it again, and a good answer clears the verdict and the
	// strikes.
	fx.whisp.calls = nil
	delete(fx.whisp.errs, p)
	fx.whisp.texts[p] = goodIntro
	if _, err := fx.runAs(&fileIntroRep{id: "run-4"}, `{"retry_failed":true}`); err != nil {
		t.Fatal(err)
	}
	if got := fx.whisp.sentPaths(); got[p] != 1 {
		t.Fatalf("retry_failed should re-send the file: %v", got)
	}
	r = fx.row(b, id)
	if r.IntroTranscription == nil || *r.IntroTranscription != goodIntro || r.IntroTimeouts != 0 {
		t.Fatalf("a transcript must replace the verdict and reset strikes: %+v", r)
	}
}

// The strike count belongs to one FileHash: a replaced file starts again.
func TestFileIntroRun_TimeoutStrikesRestartWhenTheFileChanges(t *testing.T) {
	fx := newIntroFx(t)
	b := fx.book("Year One")
	id, _ := fx.timeoutFile(b, "01.m4b", 1, "y1-old")
	if _, err := fx.runAs(&fileIntroRep{id: "run-1"}, ""); err != nil {
		t.Fatal(err)
	}
	if r := fx.row(b, id); r.IntroTimeouts != 1 {
		t.Fatalf("want 1 strike, got %d", r.IntroTimeouts)
	}
	// The file is replaced: same row, new bytes.
	replaced := *fx.row(b, id)
	replaced.FileHash = "y1-new"
	if err := fx.s.UpdateBookFile(id, &replaced); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.runAs(&fileIntroRep{id: "run-2"}, ""); err != nil {
		t.Fatal(err)
	}
	r := fx.row(b, id)
	if r.TranscribeStatus != nil || r.IntroTimeouts != 1 {
		t.Fatalf("strikes against the old bytes must not count against the new file: status=%v strikes=%d", r.TranscribeStatus, r.IntroTimeouts)
	}
}

// A transcript that lands while the worker is still timing out on the same
// bytes (another op, a manual apply) is never overwritten by the verdict.
func TestFileIntroRun_TimeoutNeverOverwritesATranscriptThatLandedMeanwhile(t *testing.T) {
	fx := newIntroFx(t)
	b := fx.book("Hush")
	id, _ := fx.timeoutFile(b, "01.m4b", 1, "hush-1")
	// Strike 1 so the next timeout would be the permanent one.
	if _, err := fx.runAs(&fileIntroRep{id: "run-1"}, ""); err != nil {
		t.Fatal(err)
	}
	inner := fileIntroWhisperFn
	fileIntroWhisperFn = func(ctx context.Context, jobs map[string]fileIntroJob, f func(int, int)) (map[string]fileIntroWhisperResult, error) {
		text := goodIntro
		if _, err := fx.s.SetBookFileIntro(b, id, database.BookFileIntroResult{
			SourceHash: "hush-1", RunOpID: "elsewhere", Status: statusOK, Transcript: &text, At: time.Now(), Done: true,
		}); err != nil {
			t.Error(err)
		}
		return inner(ctx, jobs, f)
	}
	t.Cleanup(func() { fileIntroWhisperFn = inner })
	if _, err := fx.runAs(&fileIntroRep{id: "run-2"}, ""); err != nil {
		t.Fatal(err)
	}
	r := fx.row(b, id)
	if r.IntroTranscription == nil || *r.IntroTranscription != goodIntro {
		t.Fatalf("the transcript was lost: %+v", r)
	}
	if r.TranscribeStatus != nil && *r.TranscribeStatus == statusTranscribeTimeout {
		t.Fatalf("a permanent verdict was written over a transcript")
	}
}

// A worker that is reachable but stuck is not an outage: the batch is NOT
// re-sent by the outage loop (that is how one clip parked every worker).
func TestFileIntroRun_StuckWorkerIsNotAnOutage(t *testing.T) {
	fx := newIntroFx(t)
	b := fx.book("Knightfall")
	id, p := fx.file(b, "01.m4b", 1, "kf-1", false)
	fx.whisp.texts[p] = goodIntro
	fx.whisp.transErr = stuckTransportErr()
	fx.whisp.transErrFor = 1

	if _, err := fx.run(""); err != nil {
		t.Fatalf("one stuck batch must not fail the run: %v", err)
	}
	if n := len(fx.whisp.calls); n != 1 {
		t.Fatalf("the stuck batch was re-sent %d time(s); a stuck worker must not enter the outage loop", n-1)
	}
	r := fx.row(b, id)
	if r.TranscribeStatus != nil || r.IntroTimeouts != 0 || r.IntroTranscription != nil {
		t.Fatalf("a stuck batch is no verdict on the file and must write nothing: %+v", r)
	}
	if st, _ := fx.s.GetFileIntroStats(); st.Deferred != 1 || st.WhisperError != 0 {
		t.Fatalf("stats: %+v", st)
	}
	// The next run finds the worker healthy again and finishes the file.
	if _, err := fx.runAs(&fileIntroRep{id: "run-2"}, ""); err != nil {
		t.Fatal(err)
	}
	if r := fx.row(b, id); r.IntroTranscription == nil {
		t.Fatalf("file not transcribed once the worker recovered: %+v", r)
	}
}

// Files for n full batches, each with its own hash so every one is its own group.
func (fx *introFx) manyFiles(title string, n int) map[string]string {
	b := fx.book(title)
	ids := map[string]string{}
	for i := range n {
		id, p := fx.file(b, fmt.Sprintf("%03d.m4b", i+1), i+1, fmt.Sprintf("%s-%03d", title, i), false)
		fx.whisp.texts[p] = goodIntro
		ids[id] = b
	}
	return ids
}

func TestFileIntroRun_StopsAfterThreeStuckBatchesInARow(t *testing.T) {
	fx := newIntroFx(t)
	fileIntroSlots = func() int { return 1 } // deterministic order
	rows := fx.manyFiles("No Mans Land", fileIntroBatchGroups*5)
	fx.whisp.transErr = stuckTransportErr()

	_, err := fx.run("")
	if !errors.Is(err, errWhisperStuck) {
		t.Fatalf("want errWhisperStuck, got %v", err)
	}
	if n := len(fx.whisp.calls); n != whisperStuckMaxBatches {
		t.Fatalf("run made %d whisper calls, want exactly %d then stop", n, whisperStuckMaxBatches)
	}
	for id, b := range rows {
		if r := fx.row(b, id); r.TranscribeStatus != nil || r.IntroTimeouts != 0 || r.IntroTranscription != nil {
			t.Fatalf("a stuck batch wrote something for %s: %+v", id, r)
		}
	}
}

func TestFileIntroRun_AnAnsweredBatchResetsTheStuckStreak(t *testing.T) {
	fx := newIntroFx(t)
	fileIntroSlots = func() int { return 1 }
	fx.manyFiles("Batman Annual", fileIntroBatchGroups*6)
	inner := fileIntroWhisperFn
	var calls atomic.Int64
	fileIntroWhisperFn = func(ctx context.Context, jobs map[string]fileIntroJob, f func(int, int)) (map[string]fileIntroWhisperResult, error) {
		if calls.Add(1)%2 == 1 { // stuck, ok, stuck, ok, ...
			return nil, stuckTransportErr()
		}
		return inner(ctx, jobs, f)
	}
	t.Cleanup(func() { fileIntroWhisperFn = inner })

	if _, err := fx.run(""); err != nil {
		t.Fatalf("alternating stuck/answered batches must not stop the run (the streak resets on an answer): %v", err)
	}
	if n := calls.Load(); n != 6 {
		t.Fatalf("expected 6 batches, got %d calls", n)
	}
}

// ---- progress heartbeat (F) -------------------------------------------------

type recordingRep struct {
	fileIntroRep
	mu   sync.Mutex
	msgs []string
}

func (r *recordingRep) UpdateProgress(c, tot int, msg string) error {
	r.mu.Lock()
	r.msgs = append(r.msgs, msg)
	r.mu.Unlock()
	return r.fileIntroRep.UpdateProgress(c, tot, msg)
}

func (r *recordingRep) count(sub string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, m := range r.msgs {
		if strings.Contains(m, sub) {
			n++
		}
	}
	return n
}

func TestFileIntroRun_ReportsProgressWhileAWhisperRequestIsOutstanding(t *testing.T) {
	fx := newIntroFx(t)
	orig := whisperWaitTickInterval
	whisperWaitTickInterval = 5 * time.Millisecond
	t.Cleanup(func() { whisperWaitTickInterval = orig })
	b := fx.book("The Cult")
	_, p := fx.file(b, "01.m4b", 1, "cult-1", false)
	fx.whisp.texts[p] = goodIntro
	fx.whisp.delay = 120 * time.Millisecond

	rep := &recordingRep{fileIntroRep: fileIntroRep{id: "run-hb"}}
	if err := fx.p.runFileIntroTranscribe(context.Background(), liveTranscribeParams(""), rep); err != nil {
		t.Fatal(err)
	}
	if n := rep.count("waiting on whisper"); n < 3 {
		t.Fatalf("only %d heartbeat(s) during a 120ms request with a 5ms interval", n)
	}
}

// ---- per-book op ------------------------------------------------------------

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuf) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// bookPageRuns numbers the runs bookPage simulates.
var bookPageRuns atomic.Int64

// bookPage runs one remote-mode page for the given books on the fx store, as a
// run of its own.
func (fx *introFx) bookPage(t *testing.T, retryDurable bool, ids ...string) (int, *transcribeStatsAccum, string) {
	t.Helper()
	// Each page is its own run: a strike counts once per (file, run), so two
	// pages sharing a run id would be one run seeing the file time out twice.
	return fx.bookPageAs(t, fmt.Sprintf("op-book-%d", bookPageRuns.Add(1)), retryDurable, nil, ids...)
}

// bookPageAs is bookPage under an explicit run id, with setup applied to the
// accumulator before the page runs (to install a write gate, for one).
func (fx *introFx) bookPageAs(t *testing.T, runID string, retryDurable bool, setup func(*transcribeStatsAccum), ids ...string) (int, *transcribeStatsAccum, string) {
	t.Helper()
	books := make([]database.Book, 0, len(ids))
	for _, id := range ids {
		books = append(books, *fx.bookRow(id))
	}
	rootDir, _ := noClipWrites(t)
	logs := &syncBuf{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	accum := newTranscribeStatsAccum(nil, runID, len(ids), time.Now())
	if setup != nil {
		setup(accum)
	}
	n := fx.p.processTranscribePage(context.Background(), fx.s, log, books, rootDir, nil, accum, false, retryDurable, pageJournal{}, extractRemote)
	return n, accum, logs.String()
}

func answerEvery(calls *atomic.Int32, res transcribe.BatchResult, err error) pathBatchFn {
	return func(_ context.Context, jobs map[string]transcribe.PathJob, _ transcribe.BatchOptions) (map[string]transcribe.BatchResult, error) {
		calls.Add(1)
		if err != nil {
			return nil, err
		}
		out := make(map[string]transcribe.BatchResult, len(jobs))
		for id := range jobs {
			out[id] = res
		}
		return out, nil
	}
}

func TestBookPage_TimeoutCountsStrikesOnTheFileThenIsPermanent(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")
	fx := newIntroFx(t)
	b := fx.book("The Long Halloween Absolute")
	id, _ := fx.file(b, "01.m4b", 1, "bk-1", false)
	var calls atomic.Int32
	swapPathSeams(t, true, "", answerEvery(&calls, transcribe.BatchResult{Error: decoderTimeoutText}, nil))

	// Strike 1: the book is deferred and nothing about it is written.
	n, accum, _ := fx.bookPage(t, false, b)
	if n != 0 {
		t.Fatalf("processed %d", n)
	}
	if st := accum.snapshot(); st.Deferred != 1 || st.TranscribeTimeout != 0 || st.Attempted != 0 {
		t.Fatalf("stats after strike 1: %+v", st)
	}
	if bk := fx.bookRow(b); bk.TranscribeStatus != nil {
		t.Fatalf("book got a status after one strike: %v", *bk.TranscribeStatus)
	}
	if r := fx.row(b, id); r.IntroTimeouts != 1 || r.TranscribeStatus != nil {
		t.Fatalf("file row after strike 1: strikes=%d status=%v", r.IntroTimeouts, r.TranscribeStatus)
	}

	// Strike 2: permanent on the book AND the file.
	_, accum, _ = fx.bookPage(t, false, b)
	if st := accum.snapshot(); st.TranscribeTimeout != 1 || st.Attempted != 1 || st.Deferred != 0 {
		t.Fatalf("stats after strike 2: %+v", st)
	}
	bk := fx.bookRow(b)
	if bk.TranscribeStatus == nil || *bk.TranscribeStatus != statusTranscribeTimeout {
		t.Fatalf("book status after strike 2: %v", bk.TranscribeStatus)
	}
	if r := fx.row(b, id); r.TranscribeStatus == nil || *r.TranscribeStatus != statusTranscribeTimeout || r.IntroTimeouts != 2 {
		t.Fatalf("file row after strike 2: strikes=%d status=%v", r.IntroTimeouts, r.TranscribeStatus)
	}
	if calls.Load() != 2 {
		t.Fatalf("worker calls = %d, want 2", calls.Load())
	}
}

// The per-file op's permanent verdict is honoured by the per-book op: the book
// is not sent, and retry_failed overrides that.
func TestBookPage_SkipsAFirstFileThatIsAlreadyPermanent(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")
	fx := newIntroFx(t)
	b := fx.book("Year One Deluxe")
	id, _ := fx.file(b, "01.m4b", 1, "perm-1", false)
	fx.strike(b, id, "perm-1", transcribeTimeoutStrikeLimit) // earlier runs of the per-file op
	if r := fx.row(b, id); r.TranscribeStatus == nil || *r.TranscribeStatus != statusTranscribeTimeout {
		t.Fatalf("setup: file not permanent: %+v", r)
	}
	var calls atomic.Int32
	swapPathSeams(t, true, "", answerEvery(&calls, transcribe.BatchResult{Text: goodIntro}, nil))

	_, accum, _ := fx.bookPage(t, false, b)
	if calls.Load() != 0 {
		t.Fatalf("a permanent transcribe_timeout file was sent by the book op")
	}
	if bk := fx.bookRow(b); bk.TranscribeStatus == nil || *bk.TranscribeStatus != statusTranscribeTimeout {
		t.Fatalf("the verdict must be stamped on the book so selection skips it: %v", bk.TranscribeStatus)
	}
	// Not sent, so not an attempt: the stamp must not inflate Attempted or the
	// per-status counter (it did, once per run, and again on every forced run).
	if st := accum.snapshot(); st.TranscribeTimeout != 0 || st.Attempted != 0 || st.SkippedExisting != 1 {
		t.Fatalf("a book that was never sent was counted as an attempt: %+v", st)
	}

	fx.bookPage(t, true, b)
	if calls.Load() != 1 {
		t.Fatalf("retry_failed must send the book again, calls=%d", calls.Load())
	}
}

// A book whose only source is Book.FilePath has no row to hold a strike, so it
// cannot become permanent: it is deferred every time (and said so in the log).
func TestBookPage_TimeoutWithNoFileRowIsDeferredNeverPermanent(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")
	fx := newIntroFx(t)
	bk, err := fx.s.CreateBook(&database.Book{Title: "Batman Black and White", FilePath: writeSource(t, "bw.m4b")})
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	swapPathSeams(t, true, "", answerEvery(&calls, transcribe.BatchResult{Error: decoderTimeoutText}, nil))
	var lastLog string
	for range transcribeTimeoutStrikeLimit + 1 {
		_, accum, lg := fx.bookPage(t, false, bk.ID)
		lastLog = lg
		if st := accum.snapshot(); st.Deferred != 1 || st.TranscribeTimeout != 0 {
			t.Fatalf("stats: %+v", st)
		}
	}
	if got := fx.bookRow(bk.ID); got.TranscribeStatus != nil {
		t.Fatalf("a book with no file row became permanent: %v", *got.TranscribeStatus)
	}
	if !strings.Contains(lastLog, "no book_file row/hash") {
		t.Fatalf("the limitation must be visible in the log: %s", lastLog)
	}
}

func TestBookPage_StuckWorkerDefersAndSaysStuckNotUnreachable(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")
	fx := newIntroFx(t)
	b := fx.book("Gotham Central")
	fx.file(b, "01.m4b", 1, "gc-1", false)
	var calls atomic.Int32
	swapPathSeams(t, true, "", answerEvery(&calls, transcribe.BatchResult{}, stuckTransportErr()))

	_, accum, logs := fx.bookPage(t, false, b)
	if st := accum.snapshot(); st.Deferred != 1 || st.WhisperError != 0 || st.Attempted != 0 {
		t.Fatalf("stats: %+v", st)
	}
	if accum.stuck.n.Load() != 1 {
		t.Fatalf("stuck streak = %d, want 1", accum.stuck.n.Load())
	}
	if !strings.Contains(logs, "whisper worker stuck") || strings.Contains(logs, "endpoints unreachable") {
		t.Fatalf("a stuck worker must be logged as stuck, not unreachable:\n%s", logs)
	}
	if bk := fx.bookRow(b); bk.TranscribeStatus != nil {
		t.Fatalf("a stuck worker is no verdict on the book: %v", *bk.TranscribeStatus)
	}
}

func TestBookPage_AnAnsweredPageResetsTheStuckStreak(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")
	fx := newIntroFx(t)
	b := fx.book("Detective Comics")
	fx.file(b, "01.m4b", 1, "dc-1", false)
	rootDir, _ := noClipWrites(t)
	accum := newTranscribeStatsAccum(nil, "op", 1, time.Now())
	run := func() {
		books := []database.Book{*fx.bookRow(b)}
		fx.p.processTranscribePage(context.Background(), fx.s, slog.New(slog.NewTextHandler(&syncBuf{}, nil)), books, rootDir, nil, accum, false, false, pageJournal{}, extractRemote)
	}
	var calls atomic.Int32
	swapPathSeams(t, true, "", answerEvery(&calls, transcribe.BatchResult{}, stuckTransportErr()))
	run()
	run()
	if accum.stuck.n.Load() != 2 {
		t.Fatalf("streak = %d, want 2", accum.stuck.n.Load())
	}
	swapPathSeams(t, true, "", answerEvery(&calls, transcribe.BatchResult{Text: goodIntro}, nil))
	run()
	if accum.stuck.n.Load() != 0 {
		t.Fatalf("streak = %d after an answered page, want 0", accum.stuck.n.Load())
	}
}

// Three pages in a row that the pool answers "stuck" stop the whole run.
func TestIntroTranscribeRun_StopsAfterThreeStuckPagesInARow(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")
	p, s, ids := newRemoteRun(t, introTranscribePageSize*whisperStuckMaxBatches+1)
	var calls atomic.Int32
	swapPathSeams(t, true, "", answerEvery(&calls, transcribe.BatchResult{}, stuckTransportErr()))
	err := p.runIntroTranscribe(context.Background(), liveTranscribeParams(`{}`), &denomReporter{})
	if !errors.Is(err, errWhisperStuck) {
		t.Fatalf("want errWhisperStuck, got %v", err)
	}
	if got := calls.Load(); got < int32(whisperStuckMaxBatches) {
		t.Fatalf("only %d pages ran", got)
	}
	// Nothing was written for any book.
	for _, id := range ids {
		if b, _ := s.GetBookByID(id); b == nil || b.TranscribeStatus != nil || b.IntroTranscription != nil {
			t.Fatalf("a stuck page wrote to %s", id)
		}
	}
}

func TestBookPage_ReportsProgressWhileAWhisperRequestIsOutstanding(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")
	fx := newIntroFx(t)
	orig := whisperWaitTickInterval
	whisperWaitTickInterval = 5 * time.Millisecond
	t.Cleanup(func() { whisperWaitTickInterval = orig })
	b := fx.book("Arkham Asylum")
	fx.file(b, "01.m4b", 1, "aa-1", false)
	rootDir, _ := noClipWrites(t)
	var beats atomic.Int64
	accum := newTranscribeStatsAccum(nil, "op", 1, time.Now())
	accum.heartbeat = func(msg string) {
		if strings.Contains(msg, "waiting on whisper") {
			beats.Add(1)
		}
	}
	swapPathSeams(t, true, "", func(ctx context.Context, jobs map[string]transcribe.PathJob, _ transcribe.BatchOptions) (map[string]transcribe.BatchResult, error) {
		select {
		case <-time.After(120 * time.Millisecond):
		case <-ctx.Done():
		}
		out := map[string]transcribe.BatchResult{}
		for id := range jobs {
			out[id] = transcribe.BatchResult{Text: goodIntro}
		}
		return out, nil
	})
	books := []database.Book{*fx.bookRow(b)}
	fx.p.processTranscribePage(context.Background(), fx.s, slog.New(slog.NewTextHandler(&syncBuf{}, nil)), books, rootDir, nil, accum, false, false, pageJournal{}, extractRemote)
	if beats.Load() < 3 {
		t.Fatalf("only %d heartbeat(s) during a 120ms request with a 5ms interval", beats.Load())
	}
}
