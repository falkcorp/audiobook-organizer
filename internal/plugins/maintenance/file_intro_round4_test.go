// file: internal/plugins/maintenance/file_intro_round4_test.go
// version: 1.0.0
// guid: 3b7e1c92-5d48-4a06-8f1d-c9a2e6b04d37
// last-edited: 2026-11-15

package maintenance

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/transcribe"
	"github.com/falkcorp/mediacore/tasks"
)

// Round 4 of the F5-09 review (N16, N17, N19, N22) for the native
// transcribe-file-intros run: the whisper call is cut before the attempt's
// deadline, a resumed attempt waits for a scan instead of refusing, a wait that
// gives up leaves the files undone, and the cursor is saved after every batch.

// ---- N16: the whisper call has its own, earlier, deadline -------------------

// The call margin is derived from the pool's own pacing allowance, so a later
// change to transcribe's poolPaceMax moves it (the function reads it), and it is
// strictly larger than that allowance plus what the yield path needs afterwards.
// The yield margin sits beyond it.
func TestFileIntroMargins_TheCallMarginCoversThePoolPaceAndTheCheckpoint(t *testing.T) {
	if got, floor := fileIntroCallMargin(), transcribe.PoolPaceMax()+fileIntroCheckpointBudget; got <= floor {
		t.Fatalf("call margin %s must be strictly larger than the pool's pace allowance %s plus the checkpoint budget %s (= %s)",
			got, transcribe.PoolPaceMax(), fileIntroCheckpointBudget, floor)
	}
	if fileIntroYieldMargin <= fileIntroCallMargin() {
		t.Fatalf("yield margin %s must be beyond the call margin %s, or a batch could start where its call is cut at once",
			fileIntroYieldMargin, fileIntroCallMargin())
	}
}

// callBoundRun runs the native core with a call margin: the run's deadline is
// `within` from now, batches stop starting yieldMargin before it, and a whisper
// call is cut callMargin before it.
func (fx *introFx) callBoundRun(within, yieldMargin, callMargin time.Duration, ck *memCheckpoint) (fileIntroResult, error) {
	fx.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), within)
	fx.t.Cleanup(cancel)
	env := fx.yieldEnv(&fileIntroRep{id: "run-call-bound"}, ck, nil, yieldMargin)
	env.CallMargin = callMargin
	return fx.yieldRun(ctx, env, true)
}

// A whisper call that never answers (a pool pacing for minutes, a transport
// queued behind the book op) must not carry the run to its timeout, which is
// terminal. Once batch 0 is done the call is cut at the call margin and the run
// yields with the cursor at the interrupted batch: its files get no result and
// no strike, and the next attempt re-sends them.
func TestFileIntroCallBound_ABlockedWhisperCallYieldsWithTheCursorAndNoStrike(t *testing.T) {
	fx := newIntroFx(t)
	fileIntroSlots = func() int { return 1 }
	fx.yieldFiles(2*fileIntroBatchGroups + 1)

	var calls atomic.Int32
	inner := fileIntroWhisperFn
	fileIntroWhisperFn = func(ctx context.Context, jobs map[string]fileIntroJob, prog func(int, int)) (map[string]fileIntroWhisperResult, error) {
		if calls.Add(1) == 1 {
			return inner(ctx, jobs, prog) // batch 0 answers
		}
		<-ctx.Done() // batch 1 never does: only its context can end it
		return nil, ctx.Err()
	}

	ck := &memCheckpoint{}
	done := make(chan error, 1)
	go func() {
		// The deadline is 3 s away and calls are cut 2 s before it, so the cut
		// lands about one second in; batches would still start until 100 ms
		// before the deadline.
		_, err := fx.callBoundRun(3*time.Second, 100*time.Millisecond, 2*time.Second, ck)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, tasks.ErrContinueInPlace) {
			t.Fatalf("a call cut at its margin must yield, got %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the run stayed in the blocked whisper call")
	}

	cur := ck.cursor(t)
	if cur.DoneBatches != 1 || cur.Last == nil {
		t.Fatalf("cursor %+v: want batch 0 complete and the cursor at the interrupted batch", cur)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("whisper was called %d times, want 2 (batch 0, then the cut batch 1; batch 2 is skipped once yielding)", n)
	}
	withResult := 0
	for _, id := range mustAllBooks(t, fx) {
		for _, f := range mustFiles(t, fx.s, id) {
			if f.HasIntroResult() {
				withResult++
			}
			if f.IntroTimeouts != 0 {
				t.Fatalf("a cut call struck %s (%d strikes): the interrupted item must be retried, not counted", f.ID, f.IntroTimeouts)
			}
		}
	}
	if withResult != fileIntroBatchGroups {
		t.Fatalf("%d files have a result, want only batch 0's %d", withResult, fileIntroBatchGroups)
	}
}

// With less budget left than the call margin, no request is started at all: the
// pool could pace for longer than is left, so the batch yields before the call.
func TestFileIntroCallBound_LessBudgetThanTheMarginYieldsBeforeTheCallStarts(t *testing.T) {
	fx := newIntroFx(t)
	fileIntroSlots = func() int { return 1 }
	fx.yieldFiles(2*fileIntroBatchGroups + 1)

	ck := &memCheckpoint{}
	// 3 s of budget against a 10 s call margin: inside it from the first call,
	// but batch 0 is exempt (a yield needs one completed batch), and batch 1 is
	// not.
	_, err := fx.callBoundRun(3*time.Second, 100*time.Millisecond, 10*time.Second, ck)
	if !errors.Is(err, tasks.ErrContinueInPlace) {
		t.Fatalf("want a yield, got %v", err)
	}
	if n := len(fx.whisp.sentPaths()); n != fileIntroBatchGroups {
		t.Fatalf("whisper was sent %d files, want only batch 0's %d (batch 1 must not start a call it cannot finish)", n, fileIntroBatchGroups)
	}
	if cur := ck.cursor(t); cur.DoneBatches != 1 {
		t.Fatalf("cursor %+v, want 1 batch done", cur)
	}
}

// Batch 0 is the documented exception: with no completed batch there is nothing
// to yield to, so its call is not cut even inside the margin.
func TestFileIntroCallBound_BatchZeroIsNeverCut(t *testing.T) {
	fx := newIntroFx(t)
	fileIntroSlots = func() int { return 1 }
	fx.yieldFiles(fileIntroBatchGroups)
	fx.whisp.delay = 50 * time.Millisecond // outlasts a margin that is already past

	ck := &memCheckpoint{}
	res, err := fx.callBoundRun(30*time.Second, time.Millisecond, time.Hour, ck)
	if err != nil {
		t.Fatalf("batch 0 must complete: %v", err)
	}
	if res.Stats == nil || res.Stats.Attempted != fileIntroBatchGroups {
		t.Fatalf("stats %+v, want batch 0 attempted", res.Stats)
	}
}

// ---- N19: an outage sleep that gives up leaves the files undone -------------

// The loop-level tests stub runBatch; this one runs the real batch. The pool is
// down for batch 1 and the retry sleep gives up inside the margin: runBatch must
// return the yield with nothing written. Without its errFileIntroYieldNow branch
// the error would fall through to the whisper_error case and write a durable
// failure on all eight groups.
func TestFileIntroYieldWait_AnOutageSleepThatGivesUpWritesNothingForTheBatch(t *testing.T) {
	fx := newIntroFx(t)
	fileIntroSlots = func() int { return 1 }
	fileIntroOutageInterval = time.Hour // only giveUp can end the sleep
	fx.yieldFiles(2*fileIntroBatchGroups + 1)

	var calls atomic.Int32
	inner := fileIntroWhisperFn
	fileIntroWhisperFn = func(ctx context.Context, jobs map[string]fileIntroJob, prog func(int, int)) (map[string]fileIntroWhisperResult, error) {
		if calls.Add(1) == 1 {
			return inner(ctx, jobs, prog)
		}
		return nil, fileIntroOutageErr()
	}

	ck := &memCheckpoint{}
	_, err := fx.yieldRun(shortDeadline(t), fx.yieldEnv(&fileIntroRep{id: "run-outage-yield"}, ck, nil, fileIntroYieldMargin), true)
	if !errors.Is(err, tasks.ErrContinueInPlace) {
		t.Fatalf("an outage inside the margin must yield, got %v", err)
	}
	withResult := 0
	for _, id := range mustAllBooks(t, fx) {
		for _, f := range mustFiles(t, fx.s, id) {
			if f.HasIntroResult() {
				withResult++
			}
		}
	}
	if withResult != fileIntroBatchGroups {
		t.Fatalf("%d files have a result, want only batch 0's %d: the yielded batch must be left undone, not written as whisper_error", withResult, fileIntroBatchGroups)
	}
	if cur := ck.cursor(t); cur.DoneBatches != 1 {
		t.Fatalf("cursor %+v, want 1 batch done", cur)
	}
}

// ---- N17: a resumed attempt waits for a scan ---------------------------------

func TestFileIntroResume_AResumedAttemptWaitsForAScanWhereAFreshStartRefuses(t *testing.T) {
	t.Run("fresh start keeps HEAD's refusal", func(t *testing.T) {
		fx := newIntroFx(t)
		q := &scanQueue{busy: true}
		fx.p = New(queueDeps{fakeDeps: fakeDeps{store: fx.s}, q: q})
		fx.yieldFiles(fileIntroBatchGroups)
		_, err := fx.yieldRun(context.Background(), fx.yieldEnv(&fileIntroRep{id: "run-fresh"}, &memCheckpoint{}, nil, fileIntroYieldMargin), true)
		if err == nil || !strings.Contains(err.Error(), "refusing to start") {
			t.Fatalf("a fresh start behind a running scan must refuse, got %v", err)
		}
	})

	t.Run("resumed attempt waits and continues", func(t *testing.T) {
		fx := newIntroFx(t)
		fileIntroSlots = func() int { return 1 }
		q := &scanQueue{}
		fx.p = New(queueDeps{fakeDeps: fakeDeps{store: fx.s}, q: q})
		paths := fx.yieldFiles(2*fileIntroBatchGroups + 1)
		ck := &memCheckpoint{}

		// Attempt 1 yields at the margin with the queue idle.
		if _, err := fx.yieldRun(shortDeadline(t), fx.yieldEnv(&fileIntroRep{id: "run-resume-scan"}, ck, nil, fileIntroYieldMargin), true); !errors.Is(err, tasks.ErrContinueInPlace) {
			t.Fatalf("attempt 1 should yield, got %v", err)
		}

		// Attempt 2 starts while a scan is running, which clears after a few
		// polls. A refusal would end the run failed here.
		q.mu.Lock()
		q.busy, q.busyPolls, q.clearAfter = true, 0, 4
		q.mu.Unlock()
		prog := &yieldProgress{}
		res, err := fx.yieldRun(context.Background(), fx.yieldEnv(&fileIntroRep{id: "run-resume-scan-2"}, ck, prog, fileIntroYieldMargin), true)
		if err != nil {
			t.Fatalf("a resumed attempt must wait for the scan and continue, got %v", err)
		}
		if n := q.polls(); n < 4 {
			t.Fatalf("the scan was polled busy %d times, want at least 4: the attempt did not wait for it", n)
		}
		if res.RunID != "run-resume-scan" || res.Stats == nil || !res.Stats.Done || res.Stats.Attempted != len(paths) {
			t.Fatalf("the continued run did not finish: %+v stats=%+v", res, res.Stats)
		}
		// The wait did not move the bar backwards.
		prog.mu.Lock()
		defer prog.mu.Unlock()
		for _, d := range prog.done {
			if d < 1 {
				t.Fatalf("progress fell to %d during the wait, below the 1 batch already done", d)
			}
		}
	})

	t.Run("a cancelled run leaves a waiting resumed attempt", func(t *testing.T) {
		fx := newIntroFx(t)
		fileIntroSlots = func() int { return 1 }
		q := &scanQueue{}
		fx.p = New(queueDeps{fakeDeps: fakeDeps{store: fx.s}, q: q})
		fx.yieldFiles(2*fileIntroBatchGroups + 1)
		ck := &memCheckpoint{}
		if _, err := fx.yieldRun(shortDeadline(t), fx.yieldEnv(&fileIntroRep{id: "run-resume-cancel"}, ck, nil, fileIntroYieldMargin), true); !errors.Is(err, tasks.ErrContinueInPlace) {
			t.Fatalf("attempt 1 should yield, got %v", err)
		}
		q.setBusy(true) // never clears
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		_, err := fx.yieldRun(ctx, fx.yieldEnv(&fileIntroRep{id: "run-resume-cancel"}, ck, nil, fileIntroYieldMargin), true)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("the wait is bounded by the run's context only, got %v", err)
		}
	})
}

// ---- N22: the cursor follows the completed prefix ----------------------------

// A crash (here the run's context is cancelled mid-run) after batch 1 leaves a
// cursor at batch 1, not nothing: the next attempt continues from there, sends
// only what is left, and the counters keep counting from the cursor's.
func TestFileIntroCheckpoint_ACrashAfterBatchKLeavesACursorAtK(t *testing.T) {
	fx := newIntroFx(t)
	fileIntroSlots = func() int { return 1 }
	paths := fx.yieldFiles(2*fileIntroBatchGroups + 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	inner := fileIntroWhisperFn
	fileIntroWhisperFn = func(c context.Context, jobs map[string]fileIntroJob, prog func(int, int)) (map[string]fileIntroWhisperResult, error) {
		if calls.Add(1) == 3 { // batch 2: the process dies
			cancel()
			return nil, context.Canceled
		}
		return inner(c, jobs, prog)
	}
	ck := &memCheckpoint{}
	// A margin with no deadline never yields; the cursor is still kept.
	if _, err := fx.yieldRun(ctx, fx.yieldEnv(&fileIntroRep{id: "run-crash"}, ck, nil, fileIntroYieldMargin), true); err == nil {
		t.Fatal("the crashed attempt must fail")
	}
	if ck.saves < 2 {
		t.Fatalf("the cursor was saved %d times, want once per completed batch (2)", ck.saves)
	}
	cur := ck.cursor(t)
	if cur.RunID != "run-crash" || cur.DoneBatches != 2 || cur.Last == nil || cur.Stats.Attempted != 2*fileIntroBatchGroups {
		t.Fatalf("cursor after the crash: %+v, want 2 batches of %d files and the key of the last group", cur, fileIntroBatchGroups)
	}

	fileIntroWhisperFn = inner
	fx.whisp.mu.Lock()
	fx.whisp.calls = nil
	fx.whisp.mu.Unlock()
	res, err := fx.yieldRun(context.Background(), fx.yieldEnv(&fileIntroRep{id: "run-crash-2"}, ck, nil, fileIntroYieldMargin), true)
	if err != nil {
		t.Fatalf("the continued attempt: %v", err)
	}
	if got := len(fx.whisp.sentPaths()); got != len(paths)-2*fileIntroBatchGroups {
		t.Fatalf("the continued attempt sent %d files, want the %d the crash left", got, len(paths)-2*fileIntroBatchGroups)
	}
	if res.RunID != "run-crash" || res.Stats == nil || res.Stats.Attempted != len(paths) {
		t.Fatalf("the counters must carry on from the cursor, not restart: %+v stats=%+v", res, res.Stats)
	}
	var st *database.FileIntroStats
	if st, err = fx.s.GetFileIntroStats(); err != nil || st == nil || st.Attempted != len(paths) {
		t.Fatalf("persisted stats %+v err=%v: want the whole run's %d", st, err, len(paths))
	}
}
