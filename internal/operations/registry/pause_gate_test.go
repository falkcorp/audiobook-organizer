// file: internal/operations/registry/pause_gate_test.go
// version: 1.0.0
// guid: c41a7b59-3e08-4d62-9f15-8a20d7c63e41
// last-edited: 2026-09-20

package registry_test

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// livenessReporter counts TouchLiveness calls. The watchdog test below is the
// whole reason this exists: a gate that merely blocked would look correct in
// every other test here and still get the op reaped in production.
type livenessReporter struct {
	stubReporterForItems
	touches atomic.Int32
}

func (l *livenessReporter) TouchLiveness() { l.touches.Add(1) }

func resumeAfterTest(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { _ = registry.ResumeOperations() })
}

// TestPause_InFlightItemCompletesAndNextItemHolds is the owner's sentence as a
// test: "allow current tasks in the operation to complete and hold before
// assigning any new ones."
func TestPause_InFlightItemCompletesAndNextItemHolds(t *testing.T) {
	resumeAfterTest(t)

	entered := make(chan struct{})      // item 0 has started
	releaseItem0 := make(chan struct{}) // item 0 may finish
	var started, finished atomic.Int32

	items := []int{0, 1, 2, 3}
	done := make(chan error, 1)
	go func() {
		done <- registry.RunItems(context.Background(), &stubReporterForItems{}, items,
			func(ctx context.Context, item int) error {
				started.Add(1)
				if item == 0 {
					close(entered)
					<-releaseItem0
				}
				finished.Add(1)
				return nil
			})
	}()

	<-entered // item 0 is inside its callback
	_ = registry.PauseOperations("test", "tester")

	// The in-flight item must be allowed to finish.
	close(releaseItem0)

	// Give the loop room to dispatch item 1 if the gate were broken.
	time.Sleep(200 * time.Millisecond)

	if got := finished.Load(); got != 1 {
		t.Errorf("in-flight item must complete: finished=%d, want 1", got)
	}
	if got := started.Load(); got != 1 {
		t.Errorf("no NEW item may start while paused: started=%d, want 1", got)
	}

	_ = registry.ResumeOperations()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run after resume: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("resume did not release the parked dispatch")
	}
	if got := finished.Load(); int(got) != len(items) {
		t.Errorf("after resume all items must run: finished=%d, want %d", got, len(items))
	}
}

// TestPause_StampsLivenessWhileParked pins constraint 1.
//
// watchdog.go's defaultProgressTimeout is 5 minutes. An op parked at a gate
// that reported nothing would be reaped as stuck, turning pause into a delayed
// cancel — the opposite of what was asked for. This asserts the parked
// dispatch keeps stamping the liveness clock.
func TestPause_StampsLivenessWhileParked(t *testing.T) {
	resumeAfterTest(t)
	_ = registry.PauseOperations("test", "tester")

	rep := &livenessReporter{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = registry.RunItems(ctx, rep, []int{1, 2},
			func(context.Context, int) error { return nil })
	}()

	deadline := time.After(3 * time.Second)
	for {
		if rep.touches.Load() > 0 {
			return // parked and stamping
		}
		select {
		case <-deadline:
			t.Fatal("a parked dispatch never stamped liveness; the watchdog would reap it")
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// TestPause_DoesNotBurnPerItemTimeout pins constraint 2.
//
// The gate sits BEFORE the PerItemTimeout context. If it sat after, every item
// resumed from a pause longer than that timeout (3m on the batch apply) would
// fail with DeadlineExceeded. A 50ms timeout and a pause an order of magnitude
// longer makes the regression unambiguous.
func TestPause_DoesNotBurnPerItemTimeout(t *testing.T) {
	resumeAfterTest(t)
	_ = registry.PauseOperations("test", "tester")

	var itemErr error
	var mu sync.Mutex
	done := make(chan error, 1)
	go func() {
		done <- registry.RunItems(context.Background(), &stubReporterForItems{}, []int{1},
			func(ctx context.Context, _ int) error {
				mu.Lock()
				itemErr = ctx.Err() // must be nil: the clock starts at dispatch
				mu.Unlock()
				return nil
			}, registry.RunItemsOptions{PerItemTimeout: 50 * time.Millisecond})
	}()

	time.Sleep(500 * time.Millisecond) // 10x the per-item timeout
	_ = registry.ResumeOperations()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("resume did not release")
	}
	mu.Lock()
	defer mu.Unlock()
	if itemErr != nil {
		t.Errorf("the item's timeout must start at dispatch, not at pause: ctx.Err()=%v", itemErr)
	}
}

// TestPause_CancelWhilePausedStillEnds — an operator who changes their mind must
// not have to resume before cancelling.
func TestPause_CancelWhilePausedStillEnds(t *testing.T) {
	resumeAfterTest(t)
	_ = registry.PauseOperations("test", "tester")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- registry.RunItems(ctx, &stubReporterForItems{}, []int{1, 2, 3},
			func(context.Context, int) error { return nil })
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case <-done: // any return is fine; hanging is not
	case <-time.After(5 * time.Second):
		t.Fatal("a paused op must still be cancellable")
	}
}

// TestPause_UnpausedGateDoesNotBlock guards the over-correction: a gate that is
// always on would make every op hang, and every test above would still pass.
func TestPause_UnpausedGateDoesNotBlock(t *testing.T) {
	resumeAfterTest(t)
	_ = registry.ResumeOperations()

	var n atomic.Int32
	err := registry.RunItems(context.Background(), &stubReporterForItems{}, []int{1, 2, 3},
		func(context.Context, int) error { n.Add(1); return nil })
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if n.Load() != 3 {
		t.Errorf("unpaused run must process every item, got %d/3", n.Load())
	}
}

// TestPause_StateReportsReasonAndWhy covers the read model the banner uses.
func TestPause_StateReportsReasonAndWhy(t *testing.T) {
	resumeAfterTest(t)
	_ = registry.PauseOperations("deploying", "jdfalk")

	st := registry.OperationsPauseState()
	if !st.Paused {
		t.Fatal("state must report paused")
	}
	if st.Reason != "deploying" || st.By != "jdfalk" {
		t.Errorf("state lost the reason/by: %+v", st)
	}
	if st.Since.IsZero() {
		t.Error("state must carry a since timestamp for the banner")
	}

	_ = registry.ResumeOperations()
	if registry.OperationsPauseState().Paused {
		t.Error("resume must clear the state")
	}
}

var _ = slog.LevelInfo // keep the import stable if the stub changes
