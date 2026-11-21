// file: internal/plugins/maintenance/file_intro_boundcall_test.go
// version: 1.0.0
// guid: 275a56a7-63af-4498-bd5e-d8f235c32ef1
// last-edited: 2026-11-21

package maintenance

import (
	"context"
	"testing"
	"time"
)

// TestFileIntroBoundCall_ReleaseJoinsTheWatcher: the release boundCall hands
// back does not return while the call's watcher goroutine is still running.
// Before, release only signalled the watcher to stop, so it could outlive the
// call and read fileIntroPauseInterval while a test's cleanup restored it:
// TestFileIntroCallBound_LessBudgetThanTheMarginYieldsBeforeTheCallStarts
// raced that way under a loaded -race run.
//
// The watcher is held inside batchZeroDone (the deadline is already inside
// the margin, so it asks at once). A release that does not join returns at
// once and fails here every time; one that joins stays blocked until the
// watcher is let go, so the check cannot fail spuriously.
func TestFileIntroBoundCall_ReleaseJoinsTheWatcher(t *testing.T) {
	entered, gate := make(chan struct{}), make(chan struct{})
	var once bool
	w := &fileIntroWorker{
		callMargin: time.Hour, // the whole deadline is inside the margin
		batchZeroDone: func() bool {
			if !once {
				once = true
				close(entered)
				<-gate
			}
			return false
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	_, release := w.boundCall(ctx)
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the watcher never asked batchZeroDone")
	}

	released := make(chan struct{})
	go func() { release(); close(released) }()
	select {
	case <-released:
		t.Fatal("release returned while the watcher was still running: it does not join the watcher (Arkham's doors left open)")
	case <-time.After(100 * time.Millisecond):
	}
	close(gate)
	select {
	case <-released:
	case <-time.After(10 * time.Second):
		t.Fatal("release did not return after the watcher was let go")
	}
}
