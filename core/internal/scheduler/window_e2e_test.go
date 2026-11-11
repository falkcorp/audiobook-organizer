// file: core/internal/scheduler/window_e2e_test.go
// version: 1.0.0
// guid: 8b2d4e61-77c3-4f0a-9e15-c6a0d3b9f472
// last-edited: 2026-11-11

package scheduler

import (
	"context"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/falkcorp/mediacore/tasks"
)

// TestWindow_RunsMembersUnderTheExecutor: end to end inside a synctest
// bubble (the executor's dispatch tick is bubble time, so the waits below
// are fake): the window fires, the executor runs the window kind's body
// (WindowDefinition over RunWindow, through a real *tasks.RC), and each
// member runs to completion in order as a child of the window run.
func TestWindow_RunsMembersUnderTheExecutor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := true
		f, parent := windowFixture(t, &gate)
		f.setNow(day(2, 1, 1))
		if err := f.x.Run(context.Background()); err != nil {
			t.Fatalf("executor Run: %v", err)
		}
		var win tasks.RunRecord
		for range 200 {
			time.Sleep(100 * time.Millisecond) // bubble time: one dispatch tick
			synctest.Wait()
			r, err := f.st.Runs.Get(context.Background(), parent)
			if err != nil {
				t.Fatal(err)
			}
			if win = r; r.State.Terminal() {
				break
			}
		}
		if win.State != tasks.StateSucceeded {
			t.Fatalf("window run %s (%q), want succeeded", win.State, win.Error)
		}
		for _, k := range []tasks.Kind{bravo, charlie, alpha, delta} {
			rs := runsOf(t, f.st, k)
			if len(rs) != 1 || rs[0].State != tasks.StateSucceeded || rs[0].Parent != parent || rs[0].Trigger != tasks.TriggerWindow {
				t.Fatalf("%s runs %+v, want one succeeded child of the window run", k, rs)
			}
		}
		// The observer sees each start as it commits; the body awaits each
		// member before starting the next, so this is the run order.
		var starts []string
		for _, e := range f.rec.take() {
			if strings.Contains(e, " enqueue ") {
				starts = append(starts, e[strings.Index(e, " enqueue ")+1:])
			}
		}
		want := []string{"enqueue gotham.bravo window", "enqueue gotham.charlie window", "enqueue gotham.alpha window", "enqueue gotham.delta window"}
		if !slices.Equal(starts, want) {
			t.Fatalf("starts %q, want %q", starts, want)
		}
		if rs := runsOf(t, f.st, echo); len(rs) != 0 {
			t.Fatalf("a disabled member ran: %+v", rs)
		}
		if err := f.x.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
}
