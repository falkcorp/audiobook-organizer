// file: internal/plugins/acoustid/kinds_env_test.go
// version: 1.0.0
// guid: 30b2d5b5-74f3-4f62-9d7f-9a4f741e03c1
// last-edited: 2026-11-19

package acoustid

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// envRC is a native run on a kindHost with the given mode.
func envRC(t *testing.T, mode tasks.Mode) (*kindHost, *tasks.RC) {
	t.Helper()
	h := &kindHost{FakeRun: taskstest.NewFakeRun(context.Background(), scanKind), mode: mode}
	return h, tasks.NewRC(h)
}

func TestAcoustidEnv_RegistryItemsOptionsMapping(t *testing.T) {
	pure := func(i, total int) string { return fmt.Sprintf("pure %d/%d", i, total) }
	counter := func(i, total int) string { return fmt.Sprintf("counter %d/%d", i, total) }
	ckpt := func(context.Context, int) error { return nil }

	o := registryItemsOptions(tasks.Fixed(3, "test pool"), kindItemsOptions{
		ProgressBase: 5, ProgressTotal: 40, PureLabel: pure, CheckpointEvery: 7, CheckpointState: ckpt,
	})
	require.Equal(t, 3, o.Concurrency)
	require.Equal(t, 5, o.ProgressOffset)
	require.Equal(t, 40, o.ProgressTotal)
	require.Equal(t, 7, o.CheckpointEvery)
	require.NotNil(t, o.CheckpointStateFn)
	require.Equal(t, "pure 1/2", o.Label(1, 2), "PureLabel is the label when Label is unset")

	both := registryItemsOptions(tasks.Sequential("test loop"), kindItemsOptions{PureLabel: pure, Label: counter})
	require.Equal(t, 1, both.Concurrency, "Sequential is one worker")
	require.Equal(t, "counter 1/2", both.Label(1, 2), "a counter Label wins over PureLabel in HEAD's pool")

	none := registryItemsOptions(tasks.CPU(), kindItemsOptions{})
	require.Nil(t, none.Label)
	require.Nil(t, none.CheckpointStateFn)
	require.GreaterOrEqual(t, none.Concurrency, 1, "CPU() is NumCPU workers")
}

func TestAcoustidEnv_HeadPoolBoundsWorkersAndRunsEveryItem(t *testing.T) {
	var running, peak, ran atomic.Int64
	var seenMu sync.Mutex
	seen := map[int]int{}
	err := headPool()(context.Background(), tasks.Fixed(2, "test pool"), 12, kindItemsOptions{}, func(_ context.Context, i int) error {
		seenMu.Lock()
		seen[i]++
		seenMu.Unlock()
		cur := running.Add(1)
		for {
			p := peak.Load()
			if cur <= p || peak.CompareAndSwap(p, cur) {
				break
			}
		}
		// Hold until a second item is in flight (or give up after a moment), so a
		// pool that runs fewer workers than declared shows a lower peak.
		deadline := time.Now().Add(2 * time.Second)
		for running.Load() < 2 && time.Now().Before(deadline) && ran.Load() < 10 {
			time.Sleep(time.Millisecond)
		}
		ran.Add(1)
		running.Add(-1)
		return errors.New("an item error HEAD's pool had nowhere to return")
	})
	require.NoError(t, err, "an item's error is dropped, as HEAD's hand-rolled pool dropped it")
	require.EqualValues(t, 12, ran.Load())
	require.EqualValues(t, 2, peak.Load(), "exactly the declared workers in flight, no more and no fewer")
	require.Len(t, seen, 12)
	for i := range 12 {
		require.Equal(t, 1, seen[i], "item %d runs exactly once", i)
	}
}

func TestAcoustidEnv_HeadPoolCancelWaitsForInFlightAndReturnsCtxErr(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var started, finished atomic.Int64
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- headPool()(ctx, tasks.Fixed(2, "test pool"), 50, kindItemsOptions{}, func(context.Context, int) error {
			started.Add(1)
			<-release
			finished.Add(1)
			return nil
		})
	}()
	for started.Load() < 2 {
		time.Sleep(time.Millisecond) // both workers are in flight; the next dispatch blocks on the semaphore
	}
	cancel()
	select {
	case err := <-done:
		t.Fatalf("the pool returned (%v) while items were still in flight", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	err := <-done
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, started.Load(), finished.Load(), "the pool waited for every item it started")
	require.Less(t, started.Load(), int64(50), "no item is dispatched after the cancel")
}

func TestAcoustidEnv_HeadPoolEmptyAndPreCanceled(t *testing.T) {
	require.NoError(t, headPool()(context.Background(), tasks.Sequential("test loop"), 0, kindItemsOptions{}, func(context.Context, int) error {
		t.Fatal("no items")
		return nil
	}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var ran atomic.Int64
	err := headPool()(ctx, tasks.Fixed(2, "test pool"), 5, kindItemsOptions{}, func(context.Context, int) error { ran.Add(1); return nil })
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, ran.Load(), "ctx is checked before every dispatch, the first too")
}

func TestAcoustidEnv_KindWatermarkIsTheContiguousPrefix(t *testing.T) {
	w := newKindWatermark(5)
	require.Equal(t, 0, w.complete(2), "an out-of-order completion leaves the prefix at 0")
	require.Equal(t, 0, w.complete(1))
	require.Equal(t, 3, w.complete(0), "0 closes the gap through 2")
	require.Equal(t, 3, w.complete(4))
	require.Equal(t, 5, w.complete(3))
	require.Equal(t, 5, w.complete(-1), "an out-of-range index changes nothing")
	require.Equal(t, 5, w.complete(99))
}

func TestAcoustidEnv_NativeItemsRunsEveryItemAndCheckpointsWatermark(t *testing.T) {
	_, rc := envRC(t, tasks.ModeLive)
	items := nativeItems(rc)

	var marks []int
	var ran atomic.Int64
	err := items(context.Background(), tasks.Sequential("test loop"), 5, kindItemsOptions{
		CheckpointEvery: 2,
		CheckpointState: func(_ context.Context, mark int) error { marks = append(marks, mark); return nil },
	}, func(context.Context, int) error { ran.Add(1); return nil })
	require.NoError(t, err)
	require.EqualValues(t, 5, ran.Load())
	require.Equal(t, []int{2, 4, 5}, marks, "one checkpoint per 2 completions, plus the final watermark")
}

func TestAcoustidEnv_NativeItemsCheckpointEveryUnsetMeansEachItem(t *testing.T) {
	_, rc := envRC(t, tasks.ModeLive)
	var marks []int
	require.NoError(t, nativeItems(rc)(context.Background(), tasks.Sequential("test loop"), 3, kindItemsOptions{
		CheckpointState: func(_ context.Context, mark int) error { marks = append(marks, mark); return nil },
	}, func(context.Context, int) error { return nil }))
	require.Equal(t, []int{1, 2, 3}, marks)
}

func TestAcoustidEnv_NativeItemsReturnsFirstItemErrorAndSkipsItsCheckpoint(t *testing.T) {
	_, rc := envRC(t, tasks.ModeLive)
	boom := errors.New("the Joker got in")
	var marks []int
	err := nativeItems(rc)(context.Background(), tasks.Sequential("test loop"), 4, kindItemsOptions{
		CheckpointEvery: 1,
		CheckpointState: func(_ context.Context, mark int) error { marks = append(marks, mark); return nil },
	}, func(_ context.Context, i int) error {
		if i == 2 {
			return boom
		}
		return nil
	})
	require.ErrorIs(t, err, boom, "the item's own error comes back, not the ItemsError around it")
	var ie *tasks.ItemsError
	require.False(t, errors.As(err, &ie))
	require.Equal(t, []int{1, 2}, marks, "a failed item is not checkpointed, and FailFast stops the rest")
}

func TestAcoustidEnv_NativeItemsObservesTheCallersCtx(t *testing.T) {
	_, rc := envRC(t, tasks.ModeLive)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var ran atomic.Int64
	err := nativeItems(rc)(ctx, tasks.Sequential("test loop"), 3, kindItemsOptions{}, func(context.Context, int) error { ran.Add(1); return nil })
	require.ErrorIs(t, err, context.Canceled, "a canceled caller ctx is returned, as registry.RunItems does")
	require.Zero(t, ran.Load())

	// An item's ctx is canceled with the caller's.
	ctx2, cancel2 := context.WithCancel(context.Background())
	err = nativeItems(rc)(ctx2, tasks.Sequential("test loop"), 1, kindItemsOptions{}, func(ic context.Context, _ int) error {
		cancel2()
		<-ic.Done()
		return ic.Err()
	})
	require.ErrorIs(t, err, context.Canceled)
}

func TestAcoustidEnv_NativeItemsParallelPoolReturnsCtxErrWhenCtxEndedDuringRun(t *testing.T) {
	_, rc := envRC(t, tasks.ModeLive)
	ctx, cancel := context.WithCancel(context.Background())
	err := nativeItems(rc)(ctx, tasks.Fixed(2, "test pool"), 4, kindItemsOptions{}, func(context.Context, int) error {
		cancel()
		return nil
	})
	require.ErrorIs(t, err, context.Canceled, "registry.RunItems' parallel path returns ctx.Err() even when every item ran")

	// The same with no item error at all to wrap: one item, two workers, the
	// item cancels and succeeds. Only the pool's own check can report the cancel.
	ctx1, cancel1 := context.WithCancel(context.Background())
	err = nativeItems(rc)(ctx1, tasks.Fixed(2, "test pool"), 1, kindItemsOptions{}, func(context.Context, int) error {
		cancel1()
		return nil
	})
	require.ErrorIs(t, err, context.Canceled, "a parallel pool reports a cancel that landed during a clean run")

	// A one-worker pool that ran every item returns nil, as HEAD's sequential path does.
	ctx2, cancel2 := context.WithCancel(context.Background())
	err = nativeItems(rc)(ctx2, tasks.Sequential("test loop"), 1, kindItemsOptions{}, func(context.Context, int) error {
		cancel2()
		return nil
	})
	require.NoError(t, err)
}

func TestAcoustidEnv_NativeItemsLabelTotalIsAbsoluteWhenSet(t *testing.T) {
	// The label the kernel reports for an item reads the total HEAD's label saw:
	// the absolute ProgressTotal when set, else the item count.
	var mu sync.Mutex
	var got []string
	capture := func(i, total int) string {
		s := fmt.Sprintf("%d/%d", i, total)
		mu.Lock()
		got = append(got, s)
		mu.Unlock()
		return s
	}
	for _, tc := range []struct {
		name  string
		total int
		want  string
	}{
		{"unset uses n", 0, "0/3"},
		{"absolute total", 90, "0/90"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, rc := envRC(t, tasks.ModeLive)
			mu.Lock()
			got = nil
			mu.Unlock()
			require.NoError(t, nativeItems(rc)(context.Background(), tasks.Sequential("test loop"), 3, kindItemsOptions{
				ProgressTotal: tc.total, PureLabel: capture,
			}, func(context.Context, int) error { return nil }))
			mu.Lock()
			defer mu.Unlock()
			require.Contains(t, got, tc.want)
			_ = h
		})
	}
}

func TestAcoustidEnv_HeadEnvNilReporterIsHookless(t *testing.T) {
	e := headEnv(nil, true)
	require.True(t, e.Live)
	require.Nil(t, e.Progress)
	require.Nil(t, e.Items)
	require.Nil(t, e.Reporter)
	require.False(t, headEnv(nil, false).Live)
}

func TestAcoustidEnv_NativeEnvLiveFollowsRunMode(t *testing.T) {
	_, live := envRC(t, tasks.ModeLive)
	_, prev := envRC(t, tasks.ModePreview)
	require.True(t, nativeEnv(live).Live)
	require.False(t, nativeEnv(prev).Live)
}

func TestAcoustidEnv_NativeProgressTotalUnknownAtZeroAndNoteOnlyWithMessage(t *testing.T) {
	h, rc := envRC(t, tasks.ModeLive)
	e := nativeEnv(rc)

	e.Progress(0, 0, "starting")
	e.Progress(3, 10, "")
	e.Progress(4, -1, "unknown total")

	frames := h.bars()
	require.Len(t, frames, 3)
	require.Equal(t, barFrame{Done: 0, Known: false}, frames[0], "total 0 is an unknown total")
	require.Equal(t, barFrame{Done: 3, Total: 10, Known: true}, frames[1])
	require.False(t, frames[2].Known, "a negative total is unknown too")
	require.Equal(t, []string{"starting", "unknown total"}, h.allNotes(), "an empty message adds no note")
}

func TestAcoustidEnv_NativeItemProgressIsANoteOnly(t *testing.T) {
	// The executor's own pool owns the completion count; a second writer of it
	// would race, so a hand-rolled pool's item report is only the status text.
	h, rc := envRC(t, tasks.ModeLive)
	e := nativeEnv(rc)
	e.ItemProgress(7, 9, "fixed=7 failed=0")
	e.ItemProgress(8, 9, "")
	require.Empty(t, h.bars(), "no bar frame")
	require.Equal(t, []string{"fixed=7 failed=0"}, h.allNotes())
}

func TestAcoustidEnv_ReporterSinkOptionalHooks(t *testing.T) {
	var s reporterSink

	require.NoError(t, s.UpdateProgress(1, 2, "no hook set is not an error"))
	require.NotNil(t, s.Logger(), "a sink with no logger discards")
	s.Logger().Info("goes nowhere")
	require.NoError(t, s.Log(slog.LevelInfo, "also nowhere", slog.String("k", "v")))
	require.False(t, s.IsCanceled())
	require.ErrorContains(t, s.Checkpoint(struct{}{}), "checkpoint is not available")
	require.ErrorContains(t, s.Trigger(context.Background(), "event", nil), "not available in this run")
	s.SetCurrentItem("ignored")
	s.TouchLiveness()

	var ranPhase bool
	require.NoError(t, s.RunPhase(context.Background(), "phase", func(_ context.Context, _ sdk.Reporter) error {
		ranPhase = true
		return nil
	}))
	require.True(t, ranPhase)
}

func TestAcoustidEnv_ReporterSinkDelegatesToItsHooks(t *testing.T) {
	var got []string
	var touched, saved atomic.Int64
	var canceled atomic.Bool
	logger := slog.New(slog.DiscardHandler)
	s := reporterSink{
		progress:   func(c, tot int, msg string) { got = append(got, fmt.Sprintf("%d/%d %s", c, tot, msg)) },
		touch:      func() { touched.Add(1) },
		logger:     logger,
		canceled:   canceled.Load,
		checkpoint: func(any) error { saved.Add(1); return errors.New("saved") },
	}
	require.NoError(t, s.UpdateProgress(2, 4, "half"))
	require.Equal(t, []string{"2/4 half"}, got)
	require.Same(t, logger, s.Logger())
	s.TouchLiveness()
	require.EqualValues(t, 1, touched.Load())
	require.False(t, s.IsCanceled())
	canceled.Store(true)
	require.True(t, s.IsCanceled())
	require.ErrorContains(t, s.Checkpoint(1), "saved")
	require.EqualValues(t, 1, saved.Load())
}

func TestAcoustidEnv_ErrNeedMessages(t *testing.T) {
	require.NoError(t, errNeed(scanKind, "NewEngine", true))
	require.EqualError(t, errNeed(scanKind, "NewEngine", false), string(scanKind)+": KindDeps.NewEngine is not set")
	require.EqualError(t, errNeedWriter(backfillKind), string(backfillKind)+": the writer factory returned no writer")
}

// TestAcoustidEnv_HeadPoolNeverReturnsWithAnItemInFlight: the dispatcher's own
// ctx check (the loop top, not the semaphore wait) must also drain the pool
// before it returns, or a caller would write to a store the run has closed. The
// item cancels the ctx and then holds; whether the loop sees the cancel before or
// after its next dispatch is a scheduler race, so the scenario repeats.
func TestAcoustidEnv_HeadPoolNeverReturnsWithAnItemInFlight(t *testing.T) {
	for trial := range 60 {
		ctx, cancel := context.WithCancel(context.Background())
		release := make(chan struct{})
		done := make(chan error, 1)
		go func() {
			done <- headPool()(ctx, tasks.Fixed(8, "test pool"), 20, kindItemsOptions{}, func(context.Context, int) error {
				cancel()
				<-release
				return nil
			})
		}()
		select {
		case err := <-done:
			cancel()
			close(release)
			t.Fatalf("trial %d: the pool returned (%v) while items were still held", trial, err)
		case <-time.After(15 * time.Millisecond):
		}
		close(release)
		require.ErrorIs(t, <-done, context.Canceled)
		cancel()
	}
}
