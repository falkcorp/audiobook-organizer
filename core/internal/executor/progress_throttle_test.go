// file: core/internal/executor/progress_throttle_test.go
// version: 0.1.0
// guid: 5e2c7b1a-93d4-4f60-8a1b-c47d90e2f6b3
// last-edited: 2026-11-22

package executor

import (
	"runtime"
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

// TestExecutor_TheAttemptsEndCancelsItsPendingFlush: a trailing flush armed
// when the body returns is cancelled by the attempt's end, not left to fire
// on a finished run. The timer is invisible on the wire (the store refuses a
// progress write for a terminal run), so the cell reads the attempt itself.
func TestExecutor_TheAttemptsEndCancelsItsPendingFlush(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := make(chan struct{})
		p := &tape{}
		h := tapeHarness(t, p, []tasks.Definition{
			taskDef("gotham.pending", func(rc *tasks.RC, _ noParams) (string, error) {
				rc.Progress().Set(1, tasks.Known(2))
				rc.Progress().Note("Riddler cipher solved") // in-window: arms the flush
				<-gate
				return "done", nil
			}, readOnly("gotham.pending")),
		}, realIntervalConfig, false)
		id := h.start("gotham.pending", tasks.StartRequest{})
		synctest.Wait()
		h.e.mu.Lock()
		a := h.e.running[id]
		h.e.mu.Unlock()
		if a == nil {
			t.Fatal("the run is not live")
		}
		a.pmu.Lock()
		armed := a.pTimer != nil
		a.pmu.Unlock()
		if !armed {
			t.Fatal("the in-window Note did not arm a trailing flush: the cell proves nothing")
		}
		close(gate)
		h.waitState(id, tasks.StateSucceeded)
		synctest.Wait()
		a.pmu.Lock()
		closed, pending := a.pClosed, a.pTimer != nil
		a.pmu.Unlock()
		if !closed || pending {
			t.Fatalf("after the attempt ended: closed=%v, flush still pending=%v", closed, pending)
		}
		h.drained()
	})
}

// TestExecutor_AClosedAttemptArmsNoFlush: once the attempt has ended, an
// in-window Note from a goroutine the body left behind does not arm a trailing
// flush. The terminal row refuses the write anyway, so this reads the attempt
// itself: it pins armFlushLocked's pClosed check, which is what keeps a stray
// flush off a row that is NOT terminal (a yielded run is queued again under
// the same attempt id and fence, and would take the write).
func TestExecutor_AClosedAttemptArmsNoFlush(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate, late, ended := make(chan struct{}), make(chan struct{}), make(chan struct{})
		p := &tape{}
		h := tapeHarness(t, p, []tasks.Definition{
			taskDef("gotham.closed", func(rc *tasks.RC, _ noParams) (string, error) {
				rc.Progress().Set(1, tasks.Known(2)) // due: takes the window
				go func() {
					<-late
					rc.Progress().Note("Mr. Freeze thaws") // in-window, after the end
					close(ended)
				}()
				<-gate
				return "done", nil
			}, readOnly("gotham.closed")),
		}, realIntervalConfig, false)
		id := h.start("gotham.closed", tasks.StartRequest{})
		synctest.Wait()
		h.e.mu.Lock()
		a := h.e.running[id]
		h.e.mu.Unlock()
		if a == nil {
			t.Fatal("the run is not live")
		}
		close(gate)
		h.waitState(id, tasks.StateSucceeded)
		synctest.Wait()
		close(late)
		<-ended
		a.pmu.Lock()
		closed, pending := a.pClosed, a.pTimer != nil
		a.pmu.Unlock()
		if !closed {
			t.Fatal("the attempt ended but is not closed to flushes")
		}
		if pending {
			t.Fatal("an in-window Note after the attempt ended armed a trailing flush")
		}
		h.drained()
	})
}

// TestExecutor_GoexitStillClosesTheFlush: a body that ends its goroutine with
// runtime.Goexit never reaches finish, so run's deferred stopFlush is what
// closes the attempt: the flush its in-window Note armed does not fire.
func TestExecutor_GoexitStillClosesTheFlush(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := &tape{}
		h := tapeHarness(t, p, []tasks.Definition{
			taskDef("gotham.goexit", func(rc *tasks.RC, _ noParams) (string, error) {
				rc.Progress().Set(1, tasks.Known(2))
				rc.Progress().Note("Joker exits stage left") // in-window: arms the flush
				runtime.Goexit()
				return "", nil
			}, readOnly("gotham.goexit")),
		}, realIntervalConfig, false)
		id := h.start("gotham.goexit", tasks.StartRequest{})
		synctest.Wait()
		before := len(progressWrites(p, id))
		advance(2 * time.Second) // past the window: a live timer would fire now
		if after := progressWrites(p, id); len(after) != before {
			t.Fatalf("a flush wrote after the attempt goroutine exited: %d writes before the window end, %d after (last %+v)", before, len(after), after[len(after)-1])
		}
	})
}

// TestExecutor_ALateFlushDefersToTheWindowsWrite: a trailing flush whose timer
// fired while it waited on the writer, after a due update had already taken
// the window, writes nothing then and re-arms for the window's end, so the
// window keeps one throttled write. The interleaving is staged on the attempt
// directly: pLast set to now is "a due update took the window".
func TestExecutor_ALateFlushDefersToTheWindowsWrite(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := make(chan struct{})
		p := &tape{}
		h := tapeHarness(t, p, []tasks.Definition{
			taskDef("gotham.late", func(rc *tasks.RC, _ noParams) (string, error) {
				rc.Progress().Set(1, tasks.Known(3))
				<-gate
				return "done", nil
			}, readOnly("gotham.late")),
		}, realIntervalConfig, false)
		id := h.start("gotham.late", tasks.StartRequest{})
		synctest.Wait()
		h.e.mu.Lock()
		a := h.e.running[id]
		h.e.mu.Unlock()
		if a == nil {
			t.Fatal("the run is not live")
		}
		advance(1500 * time.Millisecond)
		a.pmu.Lock()
		a.pLast = h.e.now() // a due update took this window
		a.pDirty = true     // and an in-window one dirtied it again
		a.pmu.Unlock()
		before := len(progressWrites(p, id))
		a.flushTrailing()
		synctest.Wait()
		a.pmu.Lock()
		rearmed := a.pTimer != nil
		a.pmu.Unlock()
		if got := len(progressWrites(p, id)); got != before {
			t.Fatalf("a late flush wrote inside a window another write had taken: %d writes, want %d", got, before)
		}
		if !rearmed {
			t.Fatal("the late flush did not re-arm for the window's end: the dirty state would be lost")
		}
		advance(time.Second)
		if got := len(progressWrites(p, id)); got != before+1 {
			t.Fatalf("at the window's end: %d writes, want %d (the deferred flush)", got, before+1)
		}
		close(gate)
		h.waitState(id, tasks.StateSucceeded)
		h.drained()
	})
}

// TestExecutor_AFlushThatFiredBeforeTheCloseWritesNothing: a timer that had
// already fired when stopFlush ran (Timer.Stop reports false; the callback
// waits on the writer) finds the attempt closed and writes nothing, even with
// the state still dirty past the window. On the Goexit path nothing else
// clears pDirty (finish never ran), so flushTrailing's own pClosed check is
// the only thing between that callback and a write after the attempt ended.
// The interleaving is staged on the attempt: stopFlush, then the callback.
func TestExecutor_AFlushThatFiredBeforeTheCloseWritesNothing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := make(chan struct{})
		p := &tape{}
		h := tapeHarness(t, p, []tasks.Definition{
			taskDef("gotham.fired", func(rc *tasks.RC, _ noParams) (string, error) {
				rc.Progress().Set(1, tasks.Known(3))
				<-gate
				return "done", nil
			}, readOnly("gotham.fired")),
		}, realIntervalConfig, false)
		id := h.start("gotham.fired", tasks.StartRequest{})
		synctest.Wait()
		h.e.mu.Lock()
		a := h.e.running[id]
		h.e.mu.Unlock()
		if a == nil {
			t.Fatal("the run is not live")
		}
		advance(1500 * time.Millisecond) // past the window: a flush would be due
		a.pmu.Lock()
		a.pDirty = true // an in-window update the fired timer was meant to publish
		a.pmu.Unlock()
		before := len(progressWrites(p, id))
		a.stopFlush()
		a.flushTrailing() // the callback that was already running
		synctest.Wait()
		a.pmu.Lock()
		rearmed := a.pTimer != nil
		a.pmu.Unlock()
		if got := len(progressWrites(p, id)); got != before {
			t.Fatalf("a flush that fired before the close wrote after it: %d writes, want %d", got, before)
		}
		if rearmed {
			t.Fatal("a flush that fired before the close re-armed a closed attempt")
		}
		close(gate)
		h.waitState(id, tasks.StateSucceeded)
		h.drained()
	})
}
