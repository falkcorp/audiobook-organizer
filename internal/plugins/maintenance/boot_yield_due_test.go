// file: internal/plugins/maintenance/boot_yield_due_test.go
// version: 1.0.0
// guid: 2c9d5e17-8a40-4b63-b1f7-6e03a4d8c925
// last-edited: 2026-11-15

package maintenance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"
)

// Review finding C8 (stack): the boot passes yield at max(margin, 2 x the
// slowest gap between checkpoints), HEAD's rule, on both shells. The clock here
// is a value the test moves, so nothing depends on how fast the test runs.
func TestAdaptiveYieldDue_WidensTheMarginToTwiceTheSlowestGap(t *testing.T) {
	base := time.Date(2026, time.November, 15, 12, 0, 0, 0, time.UTC)
	now := base
	deadline := base.Add(20 * time.Minute)
	due := adaptiveYieldDue(func() (time.Time, bool) { return deadline, true }, func() time.Time { return now }, 5*time.Minute, nil)

	if due() {
		t.Fatal("due on the first call with 20 minutes left and a 5 minute margin")
	}
	// A 4 minute gap between checkpoints: the margin is still the 5 minute
	// floor (2 x 4 = 8 minutes would only matter below 8 minutes left).
	now = base.Add(4 * time.Minute) // 16 left
	if due() {
		t.Fatal("due with 16 minutes left")
	}
	now = base.Add(10 * time.Minute) // 10 left, gap 6 min: margin = 12 min
	if !due() {
		t.Fatal("not due with 10 minutes left after a 6 minute gap: the margin is 2 x 6 = 12 minutes")
	}
	// The slowest gap is remembered: a quick gap afterwards does not shrink it.
	now = now.Add(time.Second) // 9m59s left, still inside 12 minutes
	if !due() {
		t.Fatal("the slowest gap was forgotten")
	}
}

func TestAdaptiveYieldDue_FloorKernelAndNoDeadline(t *testing.T) {
	base := time.Date(2026, time.November, 15, 12, 0, 0, 0, time.UTC)
	now := base
	dl, has := base.Add(4*time.Minute), true
	due := adaptiveYieldDue(func() (time.Time, bool) { return dl, has }, func() time.Time { return now }, 5*time.Minute, nil)
	if !due() {
		t.Error("not due with 4 minutes left against a 5 minute floor")
	}

	has = false
	if adaptiveYieldDue(func() (time.Time, bool) { return dl, has }, func() time.Time { return now }, 5*time.Minute, nil)() {
		t.Error("due with no deadline")
	}

	// The kernel's own answer can only bring the yield forward.
	kernel := false
	far := adaptiveYieldDue(func() (time.Time, bool) { return base.Add(time.Hour), true }, func() time.Time { return now }, time.Minute, func() bool { return kernel })
	if far() {
		t.Error("due with an hour left and the kernel not asking")
	}
	kernel = true
	if !far() {
		t.Error("the kernel asked to yield and the run did not")
	}
}

// The native boot env wires that test over the run's own deadline: a run with
// 4 minutes left and a kernel that has not asked to yield (it fires 1 minute
// before a 4 minute deadline at the earliest) still yields at its next
// checkpoint, because HEAD's floor is 5 minutes. Deleting the wiring (back to
// rc.ShouldYield alone) leaves YieldDue false here.
func TestT6G2NativeBootEnv_YieldsAtHEADsFloorNotOnlyTheKernels(t *testing.T) {
	f := newT6G2Fakes(t)
	f.writer.yieldWhenDue = true
	def := t6g2KindByName(t, (&Plugin{}).t6g2Kinds(f.deps(), nil), t6g2KindExternalIDBackfill)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	h := &t6g2Host{FakeRun: taskstest.NewFakeRun(ctx, t6g2KindExternalIDBackfill), mode: tasks.ModeLive, w: &t6g2RunWriter{}}
	h.yield = false // the kernel has not asked

	_, err := t6g2RunKind(t, def, tasks.NewRC(h), `{}`)
	if !errors.Is(err, tasks.ErrContinueInPlace) {
		t.Fatalf("err = %v, want the continue-in-place sentinel", err)
	}
	if len(f.writer.yieldDue) != 1 || !f.writer.yieldDue[0] {
		t.Fatalf("YieldDue = %v, want true with 4 minutes left against the 5 minute floor", f.writer.yieldDue)
	}
}
