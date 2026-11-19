// file: internal/plugins/metafetch/kinds_env_test.go
// version: 1.0.0
// guid: 0f6c2b84-9a37-4d51-b8e0-47d1a3c95e62
// last-edited: 2026-11-19

package metafetch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// probeKind names the throwaway kinds the pool tests run under (not a real kind).
var probeKind = tasks.Kind("books." + "metafetch.env-probe")

// envRecorder is a reporter that keeps every progress message.
type envRecorder struct {
	stubReporter
	mu     sync.Mutex
	msgs   []string
	frames [][2]int
}

func (r *envRecorder) UpdateProgress(current, total int, message string) error {
	r.mu.Lock()
	r.msgs = append(r.msgs, message)
	r.frames = append(r.frames, [2]int{current, total})
	r.mu.Unlock()
	return nil
}

func (r *envRecorder) progressFrames() [][2]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][2]int(nil), r.frames...)
}

func (r *envRecorder) Logger() *slog.Logger { return slog.New(slog.DiscardHandler) }

func (r *envRecorder) messages() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.msgs...)
}

var _ sdk.Reporter = (*envRecorder)(nil)

func anyMessageContains(msgs []string, sub string) bool {
	for _, m := range msgs {
		if strings.Contains(m, sub) {
			return true
		}
	}
	return false
}

// HEAD's pool labels items with Label when a core sets one and falls back to the
// pure label otherwise.
func TestMetafetchEnv_HeadItemsLabelFallsBackToThePureLabel(t *testing.T) {
	run := func(o kindItemsOptions) []string {
		rep := &envRecorder{}
		err := headItems(rep)(context.Background(), tasks.Sequential("test"), 2, o, func(context.Context, int) error { return nil })
		require.NoError(t, err)
		return rep.messages()
	}
	require.True(t, anyMessageContains(run(kindItemsOptions{PureLabel: func(i, total int) string { return fmt.Sprintf("pure-%d-of-%d", i, total) }}), "pure-1-of-2"))
	both := run(kindItemsOptions{
		Label:     func(i, _ int) string { return fmt.Sprintf("counter-%d", i) },
		PureLabel: func(i, _ int) string { return fmt.Sprintf("pure-%d", i) },
	})
	require.True(t, anyMessageContains(both, "counter-1"))
	require.False(t, anyMessageContains(both, "pure-"), "Label wins over PureLabel")
}

// HEAD's pool passes the parallelism's worker count and the progress placement
// through to registry.RunItems, and a first error stops the run.
func TestMetafetchEnv_HeadItemsConcurrencyAndError(t *testing.T) {
	var cur, peak atomic.Int64
	fn := func(context.Context, int) error {
		n := cur.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		defer cur.Add(-1)
		return nil
	}
	require.NoError(t, headItems(&envRecorder{})(context.Background(), tasks.Sequential("test"), 20, kindItemsOptions{}, fn))
	require.EqualValues(t, 1, peak.Load(), "Sequential is one worker")

	boom := errors.New("boom")
	err := headItems(&envRecorder{})(context.Background(), tasks.Sequential("test"), 5, kindItemsOptions{}, func(_ context.Context, i int) error {
		if i == 2 {
			return boom
		}
		return nil
	})
	require.ErrorIs(t, err, boom)
}

// The silent errgroup pool of pcache-sweep's pointer pages is bounded by the
// parallelism, runs every index once, reports no progress and returns the first
// error.
func TestMetafetchEnv_HeadErrgroupItems(t *testing.T) {
	var cur, peak, ran atomic.Int64
	err := headErrgroupItems()(context.Background(), tasks.Fixed(3, "test"), 30, kindItemsOptions{}, func(context.Context, int) error {
		n := cur.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		ran.Add(1)
		time.Sleep(2 * time.Millisecond) // let the other workers pile up if the limit allowed it
		cur.Add(-1)
		return nil
	})
	require.NoError(t, err)
	require.EqualValues(t, 30, ran.Load())
	require.LessOrEqual(t, peak.Load(), int64(3))

	boom := errors.New("boom")
	err = headErrgroupItems()(context.Background(), tasks.Fixed(2, "test"), 4, kindItemsOptions{}, func(_ context.Context, i int) error {
		if i == 1 {
			return boom
		}
		return nil
	})
	require.ErrorIs(t, err, boom)
}

// The native pool's label closes over the total HEAD's label saw: n, or the
// absolute ProgressTotal when a core places its slice of a larger bar.
func TestMetafetchEnv_NativeItemsLabelTotal(t *testing.T) {
	for _, tc := range []struct {
		name  string
		total int
		want  string
	}{{"no absolute total", 0, "of-3"}, {"absolute total", 10, "of-10"}} {
		t.Run(tc.name, func(t *testing.T) {
			def := tasks.Task(probeKind, tasks.TaskSpec[struct{}, struct{}]{
				Run: func(rc *tasks.RC, _ struct{}) (struct{}, error) {
					return struct{}{}, nativeItems(rc)(rc.Context(), tasks.Sequential("test"), 3,
						kindItemsOptions{ProgressTotal: tc.total, PureLabel: func(_, total int) string { return fmt.Sprintf("of-%d", total) }},
						func(context.Context, int) error { return nil })
				},
			})
			h, _, err := runKind(t, def, tasks.ModeLive, &sentinelWriter{}, "")
			require.NoError(t, err)
			var seen []string
			for _, ev := range h.Progress() {
				seen = append(seen, ev.Current)
			}
			require.True(t, anyMessageContains(seen, tc.want), "progress items %v lack %q", seen, tc.want)
		})
	}
}

// A failed item's own error comes back from the native pool, not the ItemsError.
func TestMetafetchEnv_NativeItemsReturnsTheItemsOwnError(t *testing.T) {
	boom := errors.New("boom")
	def := tasks.Task(probeKind, tasks.TaskSpec[struct{}, struct{}]{
		Run: func(rc *tasks.RC, _ struct{}) (struct{}, error) {
			return struct{}{}, nativeItems(rc)(rc.Context(), tasks.Sequential("test"), 3, kindItemsOptions{},
				func(_ context.Context, i int) error {
					if i == 1 {
						return boom
					}
					return nil
				})
		},
	})
	_, _, err := runKind(t, def, tasks.ModeLive, &sentinelWriter{}, "")
	require.Equal(t, boom, err, "the item's own error, unwrapped")
}

// A progress frame with no known total is reported as unknown, not as zero, and
// a note is only added when there is one.
func TestMetafetchEnv_NativeProgressTotalIsUnknownAtZero(t *testing.T) {
	def := tasks.Task(probeKind, tasks.TaskSpec[struct{}, struct{}]{
		Run: func(rc *tasks.RC, _ struct{}) (struct{}, error) {
			e := nativeEnv(rc)
			e.Progress(2, 0, "unknown total")
			e.Progress(1, 4, "known total")
			e.Progress(3, 4, "")
			return struct{}{}, nil
		},
	})
	h, _, err := runKind(t, def, tasks.ModeLive, &sentinelWriter{}, "")
	require.NoError(t, err)
	require.Equal(t, []barFrame{{Done: 2}, {Done: 1, Total: 4, Known: true}, {Done: 3, Total: 4, Known: true}}, h.bars())
	require.Equal(t, []string{"unknown total", "known total"}, h.noted(), "an empty message adds no note")
}

// Live follows the run's mode in each shell.
func TestMetafetchEnv_ModeAndHooks(t *testing.T) {
	probe := func(mode tasks.Mode) kindEnv {
		var env kindEnv
		def := tasks.Task(probeKind, tasks.TaskSpec[struct{}, struct{}]{
			Run: func(rc *tasks.RC, _ struct{}) (struct{}, error) { env = nativeEnv(rc); return struct{}{}, nil },
		})
		_, _, err := runKind(t, def, mode, &sentinelWriter{}, "")
		require.NoError(t, err)
		return env
	}
	require.True(t, probe(tasks.ModeLive).Live)
	require.False(t, probe(tasks.ModePreview).Live)
	e := probe(tasks.ModeLive)
	require.NotNil(t, e.Logger)
	require.NotNil(t, e.Items)
	require.NotNil(t, e.Checkpoint)
	require.NotNil(t, e.Restore)
	require.NotNil(t, e.Reporter)
	require.False(t, e.Canceled())

	rep := &envRecorder{}
	he := headEnv(rep, true)
	require.True(t, he.Live)
	require.False(t, headEnv(rep, false).Live)
	require.Nil(t, he.Restore, "HEAD's registry merges the checkpoint into the params instead")
	require.NotNil(t, he.Checkpoint)
	he.Progress(1, 2, "hello")
	require.Equal(t, []string{"hello"}, rep.messages())
}

// A sink with no logger discards; one with a logger uses it. A sink with no
// touch hook ignores a liveness touch; one with a hook calls it.
func TestMetafetchEnv_ReporterSinkOptionalHooks(t *testing.T) {
	require.NotNil(t, reporterSink{}.Logger(), "an unset logger is a discard logger, never nil")
	var buf strings.Builder
	l := slog.New(slog.NewTextHandler(&buf, nil))
	require.Same(t, l, reporterSink{logger: l}.Logger())
	require.NotPanics(t, func() { reporterSink{}.TouchLiveness() })
	touched := 0
	reporterSink{touch: func() { touched++ }}.TouchLiveness()
	require.Equal(t, 1, touched)
	require.Error(t, reporterSink{}.Checkpoint(1))
	require.NoError(t, reporterSink{checkpoint: func(any) error { return nil }}.Checkpoint(1))
	require.False(t, reporterSink{}.IsCanceled())
	require.True(t, reporterSink{canceled: func() bool { return true }}.IsCanceled())
	require.Error(t, reporterSink{}.Trigger(context.Background(), "x", nil))
	require.NoError(t, reporterSink{}.Log(slog.LevelInfo, "msg"))
	require.NoError(t, reporterSink{}.UpdateProgress(1, 2, "m"))
	var got string
	require.NoError(t, reporterSink{progress: func(_, _ int, m string) { got = m }}.UpdateProgress(1, 2, "m"))
	require.Equal(t, "m", got)
	require.NoError(t, reporterSink{}.RunPhase(context.Background(), "p", func(context.Context, sdk.Reporter) error { return nil }))
	require.NotPanics(t, func() { reporterSink{}.SetCurrentItem("x") })
}

// decodeKindParams tolerates what HEAD's json.Unmarshal did and refuses a mode.
func TestMetafetchEnv_DecodeKindParams(t *testing.T) {
	var v struct {
		N int `json:"n"`
	}
	require.NoError(t, decodeKindParams("books.x", []byte(`{"n":3,"unknown":1}`), &v))
	require.Equal(t, 3, v.N)
	require.ErrorContains(t, decodeKindParams("books.x", []byte(`{"Apply":1}`), &v), "Apply")
	require.ErrorContains(t, decodeKindParams("books.x", []byte(`{"n":"x"}`), &v), "decode params")
}

// The native pool places its slice on the bar (ProgressBase of ProgressTotal),
// as HEAD's RunItemsOptions.ProgressOffset/ProgressTotal did.
func TestMetafetchEnv_NativeItemsPlacesTheSliceOnTheBar(t *testing.T) {
	def := tasks.Task(probeKind, tasks.TaskSpec[struct{}, struct{}]{
		Run: func(rc *tasks.RC, _ struct{}) (struct{}, error) {
			return struct{}{}, nativeItems(rc)(rc.Context(), tasks.Sequential("test"), 3,
				kindItemsOptions{ProgressBase: 5, ProgressTotal: 10}, func(context.Context, int) error { return nil })
		},
	})
	h, _, err := runKind(t, def, tasks.ModeLive, &sentinelWriter{}, "")
	require.NoError(t, err)
	evs := h.Progress()
	require.NotEmpty(t, evs)
	var maxDone int64
	for _, ev := range evs {
		maxDone = max(maxDone, ev.Done)
		require.EqualValues(t, 10, ev.Total, "the absolute total")
		require.Greater(t, ev.Done, int64(5), "the slice starts after its base")
	}
	require.EqualValues(t, 8, maxDone, "base 5 plus 3 items")
}

// HEAD's pool runs the parallelism's worker count wide: with Fixed(4) and four
// items, two are in flight at once (a pool forced to one worker never gets there
// and its first item times out waiting).
func TestMetafetchEnv_HeadItemsRunsFixedWorkersWide(t *testing.T) {
	var inflight atomic.Int64
	var timedOut atomic.Bool
	gate := make(chan struct{})
	var once sync.Once
	err := headItems(&envRecorder{})(context.Background(), tasks.Fixed(4, "test"), 4, kindItemsOptions{}, func(context.Context, int) error {
		if inflight.Add(1) >= 2 {
			once.Do(func() { close(gate) })
		}
		select {
		case <-gate:
		case <-time.After(300 * time.Millisecond):
			timedOut.Store(true)
		}
		inflight.Add(-1)
		return nil
	})
	require.NoError(t, err)
	require.False(t, timedOut.Load(), "two workers were never in flight together")
}

// HEAD's pool places a slice of a larger set on the bar: the frames carry the
// absolute total and run from the base.
func TestMetafetchEnv_HeadItemsPlacesTheSliceOnTheBar(t *testing.T) {
	rep := &envRecorder{}
	require.NoError(t, headItems(rep)(context.Background(), tasks.Sequential("test"), 3,
		kindItemsOptions{ProgressBase: 5, ProgressTotal: 10}, func(context.Context, int) error { return nil }))
	frames := rep.progressFrames()
	require.NotEmpty(t, frames)
	var maxCur int
	for _, f := range frames {
		require.Equal(t, 10, f[1], "the absolute total: %v", frames)
		maxCur = max(maxCur, f[0])
	}
	require.Equal(t, 8, maxCur, "base 5 plus 3 items: %v", frames)
}

// The native pool stops dispatching at the first failed item, as HEAD's did: the
// items after it never run.
func TestMetafetchEnv_NativeItemsStopsDispatchAtTheFirstError(t *testing.T) {
	boom := errors.New("boom")
	var ran atomic.Int64
	def := tasks.Task(probeKind, tasks.TaskSpec[struct{}, struct{}]{
		Run: func(rc *tasks.RC, _ struct{}) (struct{}, error) {
			return struct{}{}, nativeItems(rc)(rc.Context(), tasks.Sequential("test"), 6, kindItemsOptions{},
				func(_ context.Context, i int) error {
					ran.Add(1)
					if i == 1 {
						return boom
					}
					return nil
				})
		},
	})
	_, _, err := runKind(t, def, tasks.ModeLive, &sentinelWriter{}, "")
	require.ErrorIs(t, err, boom)
	require.EqualValues(t, 2, ran.Load(), "items 0 and 1 ran; the four after the failure were never dispatched")
}
