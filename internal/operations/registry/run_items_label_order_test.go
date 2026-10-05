// file: internal/operations/registry/run_items_label_order_test.go
// version: 1.0.0
// guid: eb5d8e94-51b8-4da9-aa2d-39810406f9d5
// last-edited: 2026-10-04

package registry_test

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// loProgReporter records every UpdateProgress (current, message) pair in call
// order. before runs ahead of recording and after runs once the pair is
// recorded, so a test can hold one worker's update open until another worker's
// update has fully landed.
type loProgReporter struct {
	mu       sync.Mutex
	currents []int
	labels   []string
	before   func(message string)
	after    func(message string)
}

func (r *loProgReporter) UpdateProgress(current, _ int, message string) error {
	if r.before != nil {
		r.before(message)
	}
	r.mu.Lock()
	r.currents = append(r.currents, current)
	r.labels = append(r.labels, message)
	r.mu.Unlock()
	if r.after != nil {
		r.after(message)
	}
	return nil
}
func (r *loProgReporter) SetCurrentItem(string)                      {}
func (r *loProgReporter) Log(slog.Level, string, ...slog.Attr) error { return nil }
func (r *loProgReporter) Logger() *slog.Logger                       { return slog.Default() }
func (r *loProgReporter) Checkpoint(any) error                       { return nil }
func (r *loProgReporter) IsCanceled() bool                           { return false }
func (r *loProgReporter) Trigger(context.Context, string, any) error { return nil }
func (r *loProgReporter) RunPhase(ctx context.Context, _ string, fn func(context.Context, registry.Reporter) error) error {
	return fn(ctx, r)
}

// TestRunItems_ParallelLastProgressUpdateSeesAllWork forces the interleaving
// behind the TestChaptersBackfill_ProgressLabelReportsEligibleCount flake.
//
// 🔴 THE DEFECT. Each worker rendered its post-item label and called
// UpdateProgress with no ordering between workers. A worker could render its
// label (reading the running tallies), be descheduled, and deliver it AFTER a
// later worker had already delivered a fresher one. The production reporter
// keeps whichever update arrives last, so the op ended showing a stale tally
// and a progress `current` that had stepped backwards (2 then 1). Measured on
// the chapters-backfill test under CPU load: "Books 12/12 (eligible=11 ...)"
// as the final per-item label for a run that persisted all 12.
//
// The schedule, made deterministic with channels instead of left to luck:
//   - item 1's fn waits until item 0 has rendered its POST-work label;
//   - item 0's UpdateProgress then waits (bounded) for item 1's update to land.
//
// Without ordering, item 1 renders count=2, delivers current=2, and item 0's
// stale count=1 / current=1 lands last — every run. With completion counting,
// label rendering and UpdateProgress serialized, item 1 cannot start its
// update while item 0 is inside its own, item 0's bounded wait times out, and
// the updates land in completion order.
func TestRunItems_ParallelLastProgressUpdateSeesAllWork(t *testing.T) {
	var count atomic.Int64
	item0Rendered := make(chan struct{})
	item1Updated := make(chan struct{})
	var renderedOnce, updatedOnce sync.Once

	rep := &loProgReporter{}
	rep.before = func(message string) {
		if strings.HasPrefix(message, "i=0 ") {
			select {
			case <-item1Updated:
			case <-time.After(300 * time.Millisecond):
			}
		}
	}
	rep.after = func(message string) {
		if strings.HasPrefix(message, "i=1 ") {
			updatedOnce.Do(func() { close(item1Updated) })
		}
	}

	fn := func(ctx context.Context, item int) error {
		if item == 1 {
			select {
			case <-item0Rendered:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		count.Add(1)
		return nil
	}

	err := registry.RunItems(context.Background(), rep, []int{0, 1}, fn, registry.RunItemsOptions{
		Concurrency: 2,
		Label: func(i, _ int) string {
			c := count.Load()
			// Only item 0's post-work render sees count>=1 for i==0: item 1
			// cannot increment until this fires, so item 0's pre-work
			// SetCurrentItem render always reads 0.
			if i == 0 && c >= 1 {
				renderedOnce.Do(func() { close(item0Rendered) })
			}
			return fmt.Sprintf("i=%d count=%d", i, c)
		},
	})
	if err != nil {
		t.Fatalf("RunItems: %v", err)
	}

	rep.mu.Lock()
	defer rep.mu.Unlock()
	if len(rep.currents) != 2 {
		t.Fatalf("got %d progress updates, want 2: %v", len(rep.currents), rep.labels)
	}
	if rep.currents[0] != 1 || rep.currents[1] != 2 {
		t.Errorf("progress currents = %v, want [1 2]: a later update carried a "+
			"smaller current, so the reporter (last write wins) ends behind the work", rep.currents)
	}
	if last := rep.labels[len(rep.labels)-1]; !strings.HasSuffix(last, "count=2") {
		t.Errorf("last progress label = %q, want it to report count=2 (labels in "+
			"delivery order: %v)", last, rep.labels)
	}
}
