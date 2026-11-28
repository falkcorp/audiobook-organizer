// file: core/internal/executor/progress_throttle_test.go
// version: 0.1.0
// guid: 5e2c7b1a-93d4-4f60-8a1b-c47d90e2f6b3
// last-edited: 2026-11-22

package executor

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/falkcorp/mediacore/tasks"
)

// F5-18 P1: the progress throttle at its real interval. Every other executor
// test runs at ProgressInterval = 1ns, where every call is due and none of the
// window logic runs, so these cells use the default 1s and testing/synctest.
// Fixtures are Gotham's.

// realIntervalConfig is testConfig with the default 1s progress window.
func realIntervalConfig(o *Options) {
	o.Config = Config{DispatchTick: 20 * time.Millisecond, WatchdogInterval: time.Hour,
		ProgressInterval: time.Second, MinCheckpointInterval: time.Nanosecond}
}

// progressWrites is the run-changed emissions of id that carry progress (a
// position or a message), so the queued and running transitions are not counted.
func progressWrites(p *tape, id tasks.RunID) []tasks.RunPayload {
	var out []tasks.RunPayload
	for _, em := range p.of(emitRunChanged, id) {
		if em.view.State == "running" && (em.view.Done > 0 || em.view.Message != "") {
			out = append(out, em.view)
		}
	}
	return out
}

// stepRun runs body on a real-interval executor inside a synctest bubble and
// hands the test the run's tape and ID once the body has started.
//
// gate is created inside the bubble: a channel made outside it is not durably
// blocking, so synctest.Wait would never return while the body waits on it.
func stepRun(t *testing.T, kind string, body func(rc *tasks.RC, gate chan struct{}), drive func(p *tape, id tasks.RunID, gate chan struct{})) {
	t.Helper()
	synctest.Test(t, func(t *testing.T) {
		gate := make(chan struct{})
		p := &tape{}
		started := make(chan struct{})
		h := tapeHarness(t, p, []tasks.Definition{
			taskDef(kind, func(rc *tasks.RC, _ noParams) (string, error) {
				close(started)
				body(rc, gate)
				return "done", nil
			}, readOnly(kind), withTimeout(2*time.Hour)),
		}, realIntervalConfig, false)
		id := h.start(kind, tasks.StartRequest{})
		<-started
		synctest.Wait()
		drive(p, id, gate)
		h.waitState(id, tasks.StateSucceeded)
		h.drained()
	})
}

func advance(d time.Duration) { time.Sleep(d); synctest.Wait() }

// TestExecutor_ProgressThrottle_OneWritePerWindow pins the throttle across
// Progress and Note together: inside one window only the first call writes,
// the rest are published by one trailing flush at the window's end carrying the
// newest position and line, and a Note that is due does not leave the window
// open for a Set that follows it.
func TestExecutor_ProgressThrottle_OneWritePerWindow(t *testing.T) {
	stepRun(t, "gotham.throttle", func(rc *tasks.RC, gate chan struct{}) {
		pr := rc.Progress()
		pr.Set(1, tasks.Known(9)) // due: the window's one write
		pr.Note("alley a")
		pr.Set(2, tasks.Known(9))
		pr.Note("alley b")
		pr.Set(3, tasks.Known(9))
		pr.Note("alley c")
		<-gate
		pr.Note("rooftop d") // due again: writes at once and opens a new window
		pr.Set(4, tasks.Known(9))
		<-gate
	}, func(p *tape, id tasks.RunID, gate chan struct{}) {
		if got := progressWrites(p, id); len(got) != 1 || got[0].Done != 1 {
			t.Fatalf("at t=0 progress writes = %+v, want exactly the first Set", got)
		}
		advance(900 * time.Millisecond)
		if got := progressWrites(p, id); len(got) != 1 {
			t.Fatalf("at t=0.9s (inside the window) progress writes = %d, want 1", len(got))
		}
		advance(200 * time.Millisecond)
		got := progressWrites(p, id)
		if len(got) != 2 || got[1].Done != 3 || got[1].Message != "alley c" {
			t.Fatalf("at t=1.1s writes = %+v, want the trailing flush with done 3 and the newest line", got)
		}
		advance(time.Second) // t=2.1s: the flush's window has passed
		gate <- struct{}{}
		synctest.Wait()
		if got := progressWrites(p, id); len(got) != 3 || got[2].Message != "rooftop d" {
			t.Fatalf("at t=2.1s a due Note: writes = %+v, want it written at once as the third", got)
		}
		advance(500 * time.Millisecond)
		if got := progressWrites(p, id); len(got) != 3 {
			t.Fatalf("the Set right after a due Note wrote inside the window it opened: %d writes, want 3", len(got))
		}
		advance(time.Second)
		if got := progressWrites(p, id); len(got) != 4 || got[3].Done != 4 || got[3].Message != "rooftop d" {
			t.Fatalf("writes = %+v, want the trailing flush with done 4 as the fourth", got)
		}
		gate <- struct{}{}
	})
}

// TestExecutor_SetThenNotePublishesAtTheRealInterval is the shape of ABO's
// progress closures before Update: a Set (due, so it ticks with the old line)
// and then a Note inside the window it opened. The note must reach the stream
// within one interval, by the trailing flush, not at the terminal view.
func TestExecutor_SetThenNotePublishesAtTheRealInterval(t *testing.T) {
	stepRun(t, "gotham.cipher", func(rc *tasks.RC, gate chan struct{}) {
		rc.Progress().Set(1, tasks.Known(3))
		rc.Progress().Note("Riddler cipher cracked")
		<-gate
	}, func(p *tape, id tasks.RunID, gate chan struct{}) {
		advance(1100 * time.Millisecond)
		got := progressWrites(p, id)
		if len(got) == 0 || got[len(got)-1].Message != "Riddler cipher cracked" || got[len(got)-1].Done != 1 {
			t.Fatalf("after one interval the run's message is not the note: %+v", got)
		}
		close(gate)
	})
}

// TestExecutor_UpdatePublishesPositionAndNoteTogether: Progress.Update is one
// progress write carrying the position and the line, and a second Update inside
// the window is published at its end with the newest of both.
func TestExecutor_UpdatePublishesPositionAndNoteTogether(t *testing.T) {
	stepRun(t, "gotham.update", func(rc *tasks.RC, gate chan struct{}) {
		rc.Progress().Update(1, tasks.Known(3), "Scarecrow fear gas traced")
		rc.Progress().Update(2, tasks.Known(3), "Scarecrow lab found")
		<-gate
	}, func(p *tape, id tasks.RunID, gate chan struct{}) {
		got := progressWrites(p, id)
		if len(got) != 1 || got[0].Done != 1 || got[0].Message != "Scarecrow fear gas traced" {
			t.Fatalf("first Update wrote %+v, want one write with done 1 and its line", got)
		}
		advance(1100 * time.Millisecond)
		got = progressWrites(p, id)
		if len(got) != 2 || got[1].Done != 2 || got[1].Message != "Scarecrow lab found" {
			t.Fatalf("second Update wrote %+v, want the flush with done 2 and its line", got)
		}
		close(gate)
	})
}

// TestExecutor_NoFlushAfterTheAttemptEnds: a body that outlives its attempt
// (a goroutine it left behind) cannot arm a trailing flush: the attempt closed
// its flush when it ended, so no progress is published after the terminal view.
func TestExecutor_NoFlushAfterTheAttemptEnds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		late, ended := make(chan struct{}), make(chan struct{})
		p := &tape{}
		h := tapeHarness(t, p, []tasks.Definition{
			taskDef("gotham.stray", func(rc *tasks.RC, _ noParams) (string, error) {
				rc.Progress().Set(1, tasks.Known(2))
				rc.Progress().Note("Penguin shipment logged") // arms the flush
				go func() {
					<-late
					rc.Progress().Note("first stray")  // due: ticks as it always has
					rc.Progress().Note("second stray") // in-window: must not arm a flush
					close(ended)
				}()
				return "done", nil
			}, readOnly("gotham.stray")),
		}, realIntervalConfig, false)
		id := h.start("gotham.stray", tasks.StartRequest{})
		h.waitState(id, tasks.StateSucceeded)
		synctest.Wait()
		close(late)
		<-ended
		advance(5 * time.Second)
		for _, w := range progressWrites(p, id) {
			if w.Message == "second stray" {
				t.Fatalf("a flush armed after the attempt ended published %+v", w)
			}
		}
		h.drained()
	})
}
