// file: internal/operations/registry/run_items_resume_test.go
// version: 1.0.0
// guid: 6d1f8a27-4c30-4b9e-9a55-71e2c8d40b93
// last-edited: 2026-08-17

package registry_test

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// ckptReporter records the progress values RunItems reports so a resumed run can
// be asserted to keep an ABSOLUTE denominator rather than restarting at the size
// of the remainder.
type ckptReporter struct {
	mu       sync.Mutex
	progress [][2]int // {current, total}
}

func (s *ckptReporter) UpdateProgress(current, total int, _ string) error {
	s.mu.Lock()
	s.progress = append(s.progress, [2]int{current, total})
	s.mu.Unlock()
	return nil
}
func (s *ckptReporter) SetCurrentItem(_ string)                          {}
func (s *ckptReporter) Log(_ slog.Level, _ string, _ ...slog.Attr) error { return nil }
func (s *ckptReporter) Logger() *slog.Logger                             { return slog.Default() }
func (s *ckptReporter) Checkpoint(_ any) error                           { return nil }
func (s *ckptReporter) IsCanceled() bool                                 { return false }
func (s *ckptReporter) Trigger(_ context.Context, _ string, _ any) error { return nil }
func (s *ckptReporter) RunPhase(ctx context.Context, _ string, fn func(context.Context, registry.Reporter) error) error {
	return fn(ctx, s)
}

func (s *ckptReporter) lastProgress() [2]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.progress) == 0 {
		return [2]int{0, 0}
	}
	return s.progress[len(s.progress)-1]
}

// marks collects watermarks handed to CheckpointStateFn.
type marks struct {
	mu sync.Mutex
	v  []int
}

func (m *marks) add(w int) {
	m.mu.Lock()
	m.v = append(m.v, w)
	m.mu.Unlock()
}

func (m *marks) max() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	best := 0
	for _, x := range m.v {
		if x > best {
			best = x
		}
	}
	return best
}

func (m *marks) all() []int {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := append([]int(nil), m.v...)
	return out
}

// TestRunItems_ResumeFrom_SkipsExactlyThePrefix is the core resume guarantee.
func TestRunItems_ResumeFrom_SkipsExactlyThePrefix(t *testing.T) {
	r := &ckptReporter{}
	items := []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}
	var mu sync.Mutex
	var seen []int

	err := registry.RunItems(context.Background(), r, items, func(_ context.Context, n int) error {
		mu.Lock()
		seen = append(seen, n)
		mu.Unlock()
		return nil
	}, registry.RunItemsOptions{ResumeFrom: 4})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sort.Ints(seen)
	want := []int{4, 5, 6, 7, 8, 9}
	if len(seen) != len(want) {
		t.Fatalf("expected %d items run, got %d (%v)", len(want), len(seen), seen)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("expected items %v, got %v", want, seen)
		}
	}
}

// TestRunItems_ResumeFrom_KeepsAbsoluteProgress guards the specific confusion
// that motivated this work: a resumed op whose progress bar restarts at 1 is
// indistinguishable from an op that started over.
func TestRunItems_ResumeFrom_KeepsAbsoluteProgress(t *testing.T) {
	r := &ckptReporter{}
	items := make([]int, 100)
	for i := range items {
		items[i] = i
	}

	err := registry.RunItems(context.Background(), r, items, func(_ context.Context, _ int) error {
		return nil
	}, registry.RunItemsOptions{ResumeFrom: 60})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := r.lastProgress()
	if got[1] != 100 {
		t.Fatalf("resumed run must keep the ORIGINAL total as the denominator; got total=%d, want 100", got[1])
	}
	if got[0] != 100 {
		t.Fatalf("final progress should reach the absolute total; got current=%d, want 100", got[0])
	}
	// The first reported value must be past the resume point, not 1.
	r.mu.Lock()
	first := r.progress[0]
	r.mu.Unlock()
	if first[0] <= 60 {
		t.Fatalf("first progress after resuming at 60 should exceed 60, got %d", first[0])
	}
}

// TestRunItems_ResumeFrom_BeyondEndRunsNothing covers the case where the previous
// run actually finished everything before dying.
func TestRunItems_ResumeFrom_BeyondEndRunsNothing(t *testing.T) {
	r := &ckptReporter{}
	items := []int{1, 2, 3}
	ran := 0
	err := registry.RunItems(context.Background(), r, items, func(_ context.Context, _ int) error {
		ran++
		return nil
	}, registry.RunItemsOptions{ResumeFrom: 3})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ran != 0 {
		t.Fatalf("expected 0 items to run when ResumeFrom >= len(items), got %d", ran)
	}
}

// TestRunItems_Watermark_StopsAtAGap is THE correctness property. Item 2 blocks
// until every other item has finished, so the pool completes out of order and
// indices 3..7 are done while 2 is not. No checkpoint may claim more than 2.
func TestRunItems_Watermark_StopsAtAGap(t *testing.T) {
	r := &ckptReporter{}
	items := []int{0, 1, 2, 3, 4, 5, 6, 7}
	m := &marks{}

	var others sync.WaitGroup
	others.Add(len(items) - 3) // items 3..7

	// Snapshot the checkpoints as they stand while item 2 is still in flight.
	// Asserting after RunItems returns would be useless: by then item 2 has
	// finished and the watermark has legitimately jumped to 8.
	var duringGap []int

	err := registry.RunItems(context.Background(), r, items, func(_ context.Context, n int) error {
		switch {
		case n == 2:
			// Hold the gap open until everything above it has completed, then
			// record what the checkpointer believed at that instant.
			others.Wait()
			duringGap = m.all()
		case n > 2:
			others.Done()
		}
		return nil
	}, registry.RunItemsOptions{
		Concurrency:       8,
		CheckpointStateFn: func(_ context.Context, w int) error { m.add(w); return nil },
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Items 3..7 had all completed at that moment, so a naive "completed count"
	// checkpoint would have recorded 5 or more. The watermark must not exceed 2.
	for _, w := range duringGap {
		if w > 2 {
			t.Fatalf("watermark %d claims item 2 is complete while it was still running; "+
				"a resume would skip it. marks during the gap: %v", w, duringGap)
		}
	}
	// Known-GOOD half: once the gap closes the watermark must reach the total,
	// otherwise this test would also pass against a checkpointer stuck at zero.
	if got := m.max(); got != len(items) {
		t.Fatalf("after item 2 completed the watermark should reach %d, got %d", len(items), got)
	}
}

// TestRunItems_Watermark_FailedItemDoesNotAdvance ensures a failure is retried on
// resume rather than skipped. Item 3 fails; the watermark must never exceed 3.
func TestRunItems_Watermark_FailedItemDoesNotAdvance(t *testing.T) {
	r := &ckptReporter{}
	items := []int{0, 1, 2, 3, 4, 5}
	m := &marks{}

	err := registry.RunItems(context.Background(), r, items, func(_ context.Context, n int) error {
		if n == 3 {
			return errors.New("boom")
		}
		return nil
	}, registry.RunItemsOptions{
		ErrMode:           registry.ErrModeCollect,
		CheckpointStateFn: func(_ context.Context, w int) error { m.add(w); return nil },
	})
	if err == nil {
		t.Fatal("expected the failing item to surface an error")
	}
	if got := m.max(); got > 3 {
		t.Fatalf("watermark reached %d, but item 3 FAILED — a resume would skip it entirely. marks: %v",
			got, m.all())
	}
	if got := m.max(); got != 3 {
		t.Fatalf("watermark should have advanced to exactly 3 (items 0,1,2 done), got %d", got)
	}
}

// TestRunItems_Watermark_AllCompleteReachesTotal is the known-GOOD half of the
// pair: without it, a checkpoint function that always reported 0 would satisfy
// every "must not exceed" assertion above.
func TestRunItems_Watermark_AllCompleteReachesTotal(t *testing.T) {
	r := &ckptReporter{}
	items := make([]int, 64)
	m := &marks{}

	err := registry.RunItems(context.Background(), r, items, func(_ context.Context, _ int) error {
		return nil
	}, registry.RunItemsOptions{
		Concurrency:       8,
		CheckpointStateFn: func(_ context.Context, w int) error { m.add(w); return nil },
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := m.max(); got != 64 {
		t.Fatalf("with every item succeeding the watermark must reach 64, got %d", got)
	}
}

// TestRunItems_Watermark_IsAbsoluteAcrossResume checks the value handed back is
// directly reusable as the next ResumeFrom, rather than being relative to the
// slice actually iterated.
func TestRunItems_Watermark_IsAbsoluteAcrossResume(t *testing.T) {
	r := &ckptReporter{}
	items := make([]int, 50)
	m := &marks{}

	err := registry.RunItems(context.Background(), r, items, func(_ context.Context, _ int) error {
		return nil
	}, registry.RunItemsOptions{
		ResumeFrom:        20,
		CheckpointStateFn: func(_ context.Context, w int) error { m.add(w); return nil },
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := m.max(); got != 50 {
		t.Fatalf("watermark must be absolute (20 skipped + 30 run = 50), got %d — "+
			"a relative value would resume at 30 and silently re-run 20 items forever", got)
	}
}

// TestRunItems_CheckpointEvery_Throttles verifies the throttle reduces writes
// without losing the final position.
func TestRunItems_CheckpointEvery_Throttles(t *testing.T) {
	r := &ckptReporter{}
	items := make([]int, 100)
	m := &marks{}

	err := registry.RunItems(context.Background(), r, items, func(_ context.Context, _ int) error {
		return nil
	}, registry.RunItemsOptions{
		CheckpointEvery:   25,
		CheckpointStateFn: func(_ context.Context, w int) error { m.add(w); return nil },
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n := len(m.all()); n > 10 {
		t.Fatalf("CheckpointEvery=25 over 100 items should checkpoint a handful of times, got %d", n)
	}
	if got := m.max(); got != 100 {
		t.Fatalf("the final checkpoint must record the completed total regardless of throttling, got %d", got)
	}
}

// TestRunItems_NoCheckpointFn_Unchanged pins the default path: callers that never
// opt in must see exactly the previous behaviour.
func TestRunItems_NoCheckpointFn_Unchanged(t *testing.T) {
	r := &ckptReporter{}
	items := []int{1, 2, 3, 4}
	ran := 0
	err := registry.RunItems(context.Background(), r, items, func(_ context.Context, _ int) error {
		ran++
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ran != 4 {
		t.Fatalf("expected all 4 items to run, got %d", ran)
	}
	if got := r.lastProgress(); got != [2]int{4, 4} {
		t.Fatalf("expected final progress 4/4, got %v", got)
	}
}
