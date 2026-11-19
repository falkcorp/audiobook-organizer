// file: internal/plugins/dedup/kinds_env_test.go
// version: 1.0.0
// guid: 8e3b6a17-2c54-4d90-b1f7-0a9d5c3e6b82
// last-edited: 2026-11-18

package dedup

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// envRecorder is a reporter that keeps every progress message.
type envRecorder struct {
	mockReporter
	mu   sync.Mutex
	msgs []string
}

func (r *envRecorder) UpdateProgress(_, _ int, message string) error {
	r.mu.Lock()
	r.msgs = append(r.msgs, message)
	r.mu.Unlock()
	return nil
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
func TestDedupEnv_HeadItemsLabelFallsBackToThePureLabel(t *testing.T) {
	run := func(o kindItemsOptions) []string {
		rep := &envRecorder{}
		err := headItems(rep)(context.Background(), tasks.Sequential("test"), 2, o, func(context.Context, int) error { return nil })
		require.NoError(t, err)
		return rep.messages()
	}
	require.True(t, anyMessageContains(run(kindItemsOptions{PureLabel: func(i, total int) string { return fmt.Sprintf("pure-%d-of-%d", i, total) }}), "pure-1-of-2"),
		"no Label set: the pure label names the item")
	both := run(kindItemsOptions{
		Label:     func(i, total int) string { return fmt.Sprintf("counter-%d", i) },
		PureLabel: func(i, total int) string { return fmt.Sprintf("pure-%d", i) },
	})
	require.True(t, anyMessageContains(both, "counter-1"))
	require.False(t, anyMessageContains(both, "pure-"), "Label wins over PureLabel")
}

// The native pool's label closes over the total HEAD's label saw: n, or the
// absolute ProgressTotal when a core places its slice of a larger bar.
func TestDedupEnv_NativeItemsLabelTotal(t *testing.T) {
	for _, tc := range []struct {
		name  string
		total int
		want  string
	}{{"no absolute total", 0, "of-3"}, {"absolute total", 10, "of-10"}} {
		t.Run(tc.name, func(t *testing.T) {
			def := tasks.Task(tasks.Kind("books.dedup.env-probe"), tasks.TaskSpec[struct{}, struct{}]{
				Run: func(rc *tasks.RC, _ struct{}) (struct{}, error) {
					return struct{}{}, nativeItems(rc)(rc.Context(), tasks.Sequential("test"), 3,
						kindItemsOptions{ProgressTotal: tc.total, PureLabel: func(_, total int) string { return fmt.Sprintf("of-%d", total) }},
						func(context.Context, int) error { return nil })
				},
			})
			h, err := runKind(t, def, tasks.ModeLive, &sentinelWriter{}, "")
			require.NoError(t, err)
			var seen []string
			for _, ev := range h.Progress() {
				seen = append(seen, ev.Current)
			}
			require.True(t, anyMessageContains(seen, tc.want), "progress items %v lack %q", seen, tc.want)
		})
	}
}

// A progress frame with no known total is reported as unknown, not as zero.
func TestDedupEnv_NativeProgressTotalIsUnknownAtZero(t *testing.T) {
	def := tasks.Task(tasks.Kind("books.dedup.env-probe"), tasks.TaskSpec[struct{}, struct{}]{
		Run: func(rc *tasks.RC, _ struct{}) (struct{}, error) {
			e := nativeEnv(rc)
			e.Progress(2, 0, "unknown total")
			e.Progress(1, 4, "known total")
			return struct{}{}, nil
		},
	})
	h, err := runKind(t, def, tasks.ModeLive, &sentinelWriter{}, "")
	require.NoError(t, err)
	require.Equal(t, []barFrame{{Done: 2}, {Done: 1, Total: 4, Known: true}}, h.bars(),
		"a zero total is unknown, not Known(0)")
}

// A sink with no logger discards; one with a logger uses it. A sink with no
// touch hook ignores a liveness touch; one with a hook calls it.
func TestDedupEnv_ReporterSinkOptionalHooks(t *testing.T) {
	require.NotNil(t, reporterSink{}.Logger(), "an unset logger is a discard logger, never nil")
	var buf strings.Builder
	l := slog.New(slog.NewTextHandler(&buf, nil))
	require.Same(t, l, reporterSink{logger: l}.Logger())
	require.NotPanics(t, func() { reporterSink{}.TouchLiveness() })
	touched := 0
	reporterSink{touch: func() { touched++ }}.TouchLiveness()
	require.Equal(t, 1, touched)
	err := reporterSink{}.Checkpoint(1)
	require.Error(t, err)
	require.NoError(t, reporterSink{checkpoint: func(any) error { return nil }}.Checkpoint(1))
	require.False(t, reporterSink{}.IsCanceled())
	require.True(t, reporterSink{canceled: func() bool { return true }}.IsCanceled())
}
