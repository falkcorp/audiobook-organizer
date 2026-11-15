// file: internal/plugins/maintenance/kinds_t6_labels_test.go
// version: 1.0.0
// guid: 5b7e2c90-1d4a-4f63-8a1e-c3d9f0a27b64
// last-edited: 2026-11-15

package maintenance

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"
)

// Review finding C1 (stack): tasks.ItemsOptions.Label is pure (the item only)
// and the kernel renders it before the item runs, so a label that reads running
// counters would show pre-item tallies and could step backwards. Groups 3, 4 and
// 5 carry HEAD's index-only labels natively (PureLabel) and drop the counter
// labels (Label), as group 6 does. Each native loop below is given both; only
// the pure one may be rendered.

func TestT6Native_LoopsCarryOnlyThePureLabel(t *testing.T) {
	noop := func(context.Context, int) error { return nil }

	for name, run := range map[string]func(t *testing.T, counter *atomic.Int64, pure func(i, total int) string){
		"group 3": func(t *testing.T, counter *atomic.Int64, pure func(i, total int) string) {
			h := newT6G6LiveHost(relinkUnlinkedBooksKind, nil)
			require.NoError(t, t6g3NativeItems(tasks.NewRC(h), tasks.Sequential("test"))(2, t6g3ItemsOpts{
				Label:     func(i, total int) string { counter.Add(1); return "counter" },
				PureLabel: pure,
			}, noop))
		},
		"group 4": func(t *testing.T, counter *atomic.Int64, pure func(i, total int) string) {
			h := newT6G6LiveHost(relinkUnlinkedBooksKind, nil)
			require.NoError(t, t6g4RunEach(tasks.NewRC(h), 2, t6g4EachOpts{
				Label:     func(i, total int) string { counter.Add(1); return "counter" },
				PureLabel: pure,
			}, noop))
		},
		"group 5": func(t *testing.T, counter *atomic.Int64, pure func(i, total int) string) {
			h := newT6G6LiveHost(relinkUnlinkedBooksKind, nil)
			require.NoError(t, t6g5NativeItems(tasks.NewRC(h))(context.Background(), 2, t6g5ItemsOpts{
				Pool:      t6g5Pool{Workers: 1, Par: tasks.Sequential("test")},
				Label:     func(i, total int) string { counter.Add(1); return "counter" },
				PureLabel: pure,
			}, noop))
		},
	} {
		t.Run(name, func(t *testing.T) {
			var counterCalls atomic.Int64
			var pureCalls atomic.Int64
			run(t, &counterCalls, func(i, total int) string {
				pureCalls.Add(1)
				return "item " + itoa(i+1) + "/" + itoa(total)
			})
			require.Zero(t, counterCalls.Load(), "a counter label must not reach the pure-only kernel label")
			require.EqualValues(t, 2, pureCalls.Load(), "the index-only label renders once per item")
		})
	}
}

// Group 4's native label total is the absolute ProgressTotal when the loop sets
// one, as HEAD's registry.RunItems renders it (progTotal), else n.
func TestT6G4Native_PureLabelTotalFollowsProgressTotal(t *testing.T) {
	h := newT6G6LiveHost(relinkUnlinkedBooksKind, nil)
	var totals []int
	require.NoError(t, t6g4RunEach(tasks.NewRC(h), 2, t6g4EachOpts{
		ProgressBase: 8, ProgressTotal: 10,
		PureLabel: func(_, total int) string { totals = append(totals, total); return "x" },
	}, func(context.Context, int) error { return nil }))
	require.Equal(t, []int{10, 10}, totals)
}
