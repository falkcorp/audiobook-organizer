// file: internal/operations/registry/run_items_p2_test.go
// version: 1.0.0
// guid: 4f8a2c6e-1b93-4d57-8e0a-2c9b1d3e4f60
// last-edited: 2026-07-18

package registry_test

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// p2ProgReporter records every UpdateProgress current value thread-safely so a
// parallel RunItems test can assert on the reported progression. Task-unique
// name per the parallel-test-helper-collision rule.
type p2ProgReporter struct {
	mu       sync.Mutex
	currents []int
}

func (r *p2ProgReporter) UpdateProgress(current, _ int, _ string) error {
	r.mu.Lock()
	r.currents = append(r.currents, current)
	r.mu.Unlock()
	return nil
}
func (r *p2ProgReporter) SetCurrentItem(string)                         {}
func (r *p2ProgReporter) Log(slog.Level, string, ...slog.Attr) error    { return nil }
func (r *p2ProgReporter) Logger() *slog.Logger                          { return slog.Default() }
func (r *p2ProgReporter) Checkpoint(any) error                          { return nil }
func (r *p2ProgReporter) IsCanceled() bool                              { return false }
func (r *p2ProgReporter) Trigger(context.Context, string, any) error    { return nil }
func (r *p2ProgReporter) RunPhase(ctx context.Context, _ string, fn func(context.Context, registry.Reporter) error) error {
	return fn(ctx, r)
}

func (r *p2ProgReporter) snapshot() (count, max int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.currents {
		if c > max {
			max = c
		}
	}
	return len(r.currents), max
}

// TestRunItems_ParallelProgressIsCompletionCount is the P-2 regression test.
//
// The bug: parallel RunItems reported the item INDEX (i+1), not a completion
// count — so a high-index item finishing early reported a high progress value
// while few items had actually completed, and the bar jumped backwards.
//
// Setup: N items at full concurrency. Item 0 blocks until released; items
// 1..N-1 all complete first. Once those N-1 completions are reported, the max
// reported value must be <= N-1 (fix) — under the index-based bug it would
// already be N (item N-1 reported N while item 0 was still running).
func TestRunItems_ParallelProgressIsCompletionCount(t *testing.T) {
	const n = 20
	rep := &p2ProgReporter{}
	items := make([]int, n)
	for i := range items {
		items[i] = i
	}

	release := make(chan struct{})
	fn := func(_ context.Context, item int) error {
		if item == 0 {
			<-release // finishes last
		}
		return nil
	}

	done := make(chan error, 1)
	go func() {
		done <- registry.RunItems(context.Background(), rep, items, fn,
			registry.RunItemsOptions{Concurrency: n})
	}()

	// Wait until the N-1 non-blocking items have all reported completion.
	deadline := time.Now().Add(5 * time.Second)
	for {
		count, _ := rep.snapshot()
		if count >= n-1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d progress reports after 5s, wanted %d", count, n-1)
		}
		time.Sleep(5 * time.Millisecond)
	}

	// With item 0 still blocked, at most n-1 items have completed, so the
	// highest progress value reported so far must not exceed n-1.
	if _, max := rep.snapshot(); max > n-1 {
		t.Errorf("progress reported %d with item 0 still running (only %d completed) — "+
			"progress is tracking item index, not completion count", max, n-1)
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("RunItems returned error: %v", err)
	}

	// Final report must reach n and never exceed it.
	count, max := rep.snapshot()
	if count != n {
		t.Errorf("expected %d progress reports total, got %d", n, count)
	}
	if max != n {
		t.Errorf("final max progress: got %d want %d", max, n)
	}
}
