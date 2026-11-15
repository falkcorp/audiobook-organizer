// file: internal/plugins/maintenance/file_intro_yieldwait_test.go
// version: 1.0.0
// guid: 5d0a7c4e-8b1f-4e63-9a52-3f6c1d2b7e90
// last-edited: 2026-11-14

package maintenance

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/transcribe"
	"github.com/falkcorp/mediacore/tasks"
)

// Review N1: the waits of the native transcribe-file-intros run (the scan-busy
// pause before the whisper call and at the write gate, and the outage retry
// sleep) stop at the yield margin, so a native run never ends timed_out because
// of a wait. HEAD passes no giveUp and waits as it always did.

func TestFileIntroYieldWait_PauseGivesUpInsideTheMarginOnlyWhileBusy(t *testing.T) {
	orig := fileIntroPauseInterval
	fileIntroPauseInterval = time.Millisecond
	t.Cleanup(func() { fileIntroPauseInterval = orig })

	// A scan that never ends: without giveUp this wait would not return.
	q := &fakeQueue{busyN: 1 << 20, defID: "library.scan", status: "running"}
	w := newWorkerForPause(q)
	var asked atomic.Int32
	w.giveUp = func() bool { return asked.Add(1) > 3 }
	done := make(chan error, 1)
	go func() { done <- w.waitWhileBusy(context.Background()) }()
	select {
	case err := <-done:
		if !errors.Is(err, errFileIntroYieldNow) {
			t.Fatalf("err = %v, want errFileIntroYieldNow", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the pause did not give up inside the margin")
	}

	// Not busy: inside the margin or not, the batch is never dropped for it.
	free := newWorkerForPause(&fakeQueue{})
	free.giveUp = func() bool { return true }
	if err := free.waitWhileBusy(context.Background()); err != nil {
		t.Fatalf("a batch that finds no scan running must go on: %v", err)
	}
}

// fileIntroOutageErr is the recognized network failure of a dead pool.
func fileIntroOutageErr() error {
	return &transcribe.TransportError{Endpoints: []string{"http://192.0.2.10:8000"}, Err: errors.New("connection refused"), Recognized: true}
}

func TestFileIntroYieldWait_OutageRetrySleepGivesUpInsideTheMargin(t *testing.T) {
	origFn, origOutage, origPause := fileIntroWhisperFn, fileIntroOutageInterval, fileIntroPauseInterval
	t.Cleanup(func() {
		fileIntroWhisperFn, fileIntroOutageInterval, fileIntroPauseInterval = origFn, origOutage, origPause
	})
	fileIntroOutageInterval = time.Hour // only giveUp can end the sleep
	fileIntroPauseInterval = 2 * time.Millisecond

	for _, tc := range []struct {
		name   string
		giveUp func(start time.Time) bool
	}{
		{"inside the margin before the sleep", func(time.Time) bool { return true }},
		{"the margin arrives during the sleep", func(start time.Time) bool { return time.Since(start) > 30*time.Millisecond }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			fileIntroWhisperFn = func(context.Context, map[string]fileIntroJob, func(int, int)) (map[string]fileIntroWhisperResult, error) {
				calls.Add(1)
				return nil, fileIntroOutageErr()
			}
			w := newWorkerForPause(&fakeQueue{})
			start := time.Now()
			w.giveUp = func() bool { return tc.giveUp(start) }
			type ret struct {
				out map[string]fileIntroWhisperResult
				err error
			}
			done := make(chan ret, 1)
			go func() {
				out, err := w.callWhisper(context.Background(), map[string]fileIntroJob{"f1": {Path: "/lib/a.m4b"}})
				done <- ret{out, err}
			}()
			select {
			case r := <-done:
				if !errors.Is(r.err, errFileIntroYieldNow) {
					t.Fatalf("err = %v, want errFileIntroYieldNow (not the outage error, not a timeout)", r.err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the outage retry sleep did not give up inside the margin")
			}
			if n := calls.Load(); n != 1 {
				t.Fatalf("whisper was called %d times, want 1 (no retry once inside the margin)", n)
			}
		})
	}
}

// HEAD's worker has no giveUp: the same outage is retried to the limit.
func TestFileIntroYieldWait_WithoutGiveUpTheOutageIsRetriedToTheLimit(t *testing.T) {
	origFn, origOutage := fileIntroWhisperFn, fileIntroOutageInterval
	t.Cleanup(func() { fileIntroWhisperFn, fileIntroOutageInterval = origFn, origOutage })
	fileIntroOutageInterval = time.Millisecond
	var calls atomic.Int32
	fileIntroWhisperFn = func(context.Context, map[string]fileIntroJob, func(int, int)) (map[string]fileIntroWhisperResult, error) {
		calls.Add(1)
		return nil, fileIntroOutageErr()
	}
	_, err := newWorkerForPause(&fakeQueue{}).callWhisper(context.Background(), map[string]fileIntroJob{"f1": {Path: "/lib/a.m4b"}})
	if !errors.Is(err, errFileIntroOutage) || errors.Is(err, errFileIntroYieldNow) {
		t.Fatalf("err = %v, want errFileIntroOutage", err)
	}
	if n := calls.Load(); int(n) != fileIntroOutageMaxTries {
		t.Fatalf("calls = %d, want %d", n, fileIntroOutageMaxTries)
	}
}

// A batch whose wait gave up is skipped like one never started: not done, not
// failed, not counted as settled, and the loop reports a clean yield.
func TestFileIntroYieldWait_AGivenUpBatchIsSkippedNotFailedNorCounted(t *testing.T) {
	var afterCalls atomic.Int32
	l := yieldLoop(2, func(i int) error {
		if i == 1 {
			return errFileIntroYieldNow
		}
		return nil
	})
	l.afterBatch = func(int) { afterCalls.Add(1) }
	prefix, yielded, err := l.run(shortDeadline(t))
	if err != nil || !yielded {
		t.Fatalf("want a clean yield, got yielded=%v err=%v", yielded, err)
	}
	if prefix != 1 {
		t.Fatalf("prefix = %d, want 1 (the given-up batch is not in the prefix)", prefix)
	}
	if n := afterCalls.Load(); n != 1 {
		t.Fatalf("afterBatch ran %d times, want 1 (a given-up batch settled nothing)", n)
	}
}

// The scenario of the review: a batch parked at the write gate behind a library
// scan when the run enters its margin must yield with a cursor, not wait the
// attempt out.
func TestFileIntroYieldWait_ABatchParkedAtTheWriteGateYieldsWithACursor(t *testing.T) {
	fx := newIntroFx(t)
	q := &scanQueue{}
	fx.p = New(queueDeps{fakeDeps: fakeDeps{store: fx.s}, q: q})
	paths := fx.yieldFiles(fileIntroBatchGroups + 1) // batch 0: eight files, batch 1: the last one
	lastPath := paths[len(paths)-1]

	// Batch 1's whisper call returns only after batch 0 has written its rows,
	// and starts a scan on the way out: batch 1 reaches its write gate behind a
	// scan that never ends, while batch 0 is already done.
	inner := fileIntroWhisperFn
	fileIntroWhisperFn = func(ctx context.Context, jobs map[string]fileIntroJob, prog func(int, int)) (map[string]fileIntroWhisperResult, error) {
		out, err := inner(ctx, jobs, prog)
		for _, j := range jobs {
			if j.Path != lastPath {
				continue
			}
			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				cores, _ := fx.s.GetAllBookFilesCore()
				n := 0
				for i := range cores {
					if cores[i].HasIntroResult() {
						n++
					}
				}
				if n >= fileIntroBatchGroups {
					break
				}
				time.Sleep(2 * time.Millisecond)
			}
			q.setBusy(true)
		}
		return out, err
	}

	ck := &memCheckpoint{}
	type ret struct {
		err error
	}
	done := make(chan ret, 1)
	go func() {
		_, err := fx.yieldRun(shortDeadline(t), fx.yieldEnv(&fileIntroRep{id: "run-wait-1"}, ck, nil, fileIntroYieldMargin), true)
		done <- ret{err}
	}()
	select {
	case r := <-done:
		if !errors.Is(r.err, tasks.ErrContinueInPlace) {
			t.Fatalf("a batch parked behind a scan inside the margin must yield, got %v", r.err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the run waited behind the scan instead of yielding")
	}
	cur := ck.cursor(t)
	if cur.DoneBatches != 1 || cur.Last == nil {
		t.Fatalf("cursor: %+v, want one completed batch and its last group", cur)
	}
	var parked *database.BookFile
	for _, id := range mustAllBooks(t, fx) {
		for _, f := range mustFiles(t, fx.s, id) {
			if f.FilePath == lastPath {
				f := f
				parked = &f
			}
		}
	}
	if parked == nil || parked.HasIntroResult() {
		t.Fatalf("the parked batch's file must be left undone for the next attempt: %+v", parked)
	}
}

// Review N8: a continued attempt takes the run id from the cursor, not from the
// attempt's own op id, so the two may differ.
func TestFileIntroYield_NextAttemptKeepsTheCursorsRunIDWhateverItsOwnOpIDIs(t *testing.T) {
	fx := newIntroFx(t)
	fileIntroSlots = func() int { return 1 }
	fx.yieldFiles(2*fileIntroBatchGroups + 1)
	ck := &memCheckpoint{}
	if _, err := fx.yieldRun(shortDeadline(t), fx.yieldEnv(&fileIntroRep{id: "run-A"}, ck, nil, fileIntroYieldMargin), true); !errors.Is(err, tasks.ErrContinueInPlace) {
		t.Fatalf("attempt 1 should yield, got %v", err)
	}
	res, err := fx.yieldRun(context.Background(), fx.yieldEnv(&fileIntroRep{id: "run-B"}, ck, nil, fileIntroYieldMargin), true)
	if err != nil {
		t.Fatal(err)
	}
	if res.RunID != "run-A" || res.Stats == nil || res.Stats.RunOpID != "run-A" {
		t.Fatalf("the continued attempt must keep the run id of the cursor: %+v stats=%+v", res, res.Stats)
	}
}
