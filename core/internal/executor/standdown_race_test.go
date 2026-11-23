// file: core/internal/executor/standdown_race_test.go
// version: 0.1.0
// guid: 8e2c5a41-f7d3-4b69-a0c8-1d94b6e37f52
// last-edited: 2026-11-22

package executor

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/falkcorp/mediacore/tasks"
)

// F5-18e review follow-ups on the stand-down lease: a renewal and the reaper at
// the instant of expiry are ordered by one lock, a lapse cancels the run, a run
// that reports progress keeps its hold, and the on-disk marker follows the
// newest decision about its key.

// quietTick keeps the dispatch loop (and with it the reaper) out of a test that
// calls the reaper itself.
func quietTick(o *Options) {
	o.Config = Config{DispatchTick: time.Hour, WatchdogInterval: time.Hour}
}

// attemptOf is the live attempt of run id.
func attemptOf(t *testing.T, h *harness, id tasks.RunID) *attempt {
	t.Helper()
	h.e.mu.Lock()
	defer h.e.mu.Unlock()
	a := h.e.running[id]
	if a == nil {
		t.Fatalf("run %s has no live attempt", id)
	}
	return a
}

// markers is the stand-down markers on disk, by key.
func markers(t *testing.T, h *harness) map[tasks.Key]tasks.StandDown {
	t.Helper()
	out := map[tasks.Key]tasks.StandDown{}
	for m, err := range h.stores.StandDown.List(context.Background()) {
		if err != nil {
			t.Fatalf("list markers: %v", err)
		}
		out[m.Key] = m
	}
	return out
}

// A renewal and a reap of the same hold at the instant of expiry are one critical
// section each, so exactly one wins: either the renewal extended the lease and
// the reaper leaves the hold alone (the key stays held and the writer works), or
// the reaper took it first and the renewal fails (the key is free and the writer
// refuses). Never a renewed hold that the reaper still drops, nor a dropped hold
// that a renewal still reports healthy.
func TestHoldReap_RenewAndReaperAtExpiryAreOrdered(t *testing.T) {
	t.Run("the renewal is first", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			p := newHoldProbe()
			h := newHarness(t, []tasks.Definition{p.def("gotham.compact")}, quietTick)
			h.start("gotham.compact", tasks.StartRequest{})
			hold, rc := <-p.holds, <-p.rcs
			defer close(p.free)
			lease := h.e.config().StandDownLease

			time.Sleep(lease - time.Nanosecond) // the last instant of the lease
			if err := hold.Renew(); err != nil {
				t.Fatalf("Renew inside the lease: %v", err)
			}
			h.e.reapHolds(context.Background()) // the reaper at the same instant: the hold was extended
			if !h.e.keyHeld("gotham.scan") {
				t.Fatal("the reaper dropped a hold that had just been renewed")
			}
			if called, err := write(rc, "gotham.compact"); err != nil || !called {
				t.Fatalf("write after a renewal the reaper respected: err %v, called %v", err, called)
			}
			if err := hold.Renew(); err != nil {
				t.Fatalf("second Renew: %v", err)
			}
		})
	})
	t.Run("the reaper is first", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			p := newHoldProbe()
			h := newHarness(t, []tasks.Definition{p.def("gotham.compact")}, quietTick)
			h.start("gotham.compact", tasks.StartRequest{})
			hold, rc := <-p.holds, <-p.rcs
			defer close(p.free)
			lease := h.e.config().StandDownLease

			time.Sleep(lease) // the lease ends now
			h.e.reapHolds(context.Background())
			if err := hold.Renew(); !errors.Is(err, tasks.ErrHoldLost) {
				t.Fatalf("Renew after the reaper took the hold: %v, want tasks.ErrHoldLost", err)
			}
			if h.e.keyHeld("gotham.scan") {
				t.Fatal("the key is still held after the reaper dropped it")
			}
			if called, err := write(rc, "gotham.compact"); !errors.Is(err, tasks.ErrFenced) || called {
				t.Fatalf("write after the reaper took the hold: err %v, called %v; want ErrFenced and fn never called", err, called)
			}
		})
	})
}

// A lapse ends the run's context with tasks.ErrHoldLost as its cause, whichever
// side found it: a body already inside a write sees ctx.Done and stops, rather
// than writing on beside the scan.
func TestHoldLapse_CancelsTheRunContext(t *testing.T) {
	for _, c := range []struct {
		name string
		find func(h *harness, hold tasks.Hold) error
	}{
		{"found by Renew", func(_ *harness, hold tasks.Hold) error { return hold.Renew() }},
		{"found by the reaper", func(h *harness, _ tasks.Hold) error { h.e.reapHolds(context.Background()); return nil }},
	} {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p := newHoldProbe()
				h := newHarness(t, []tasks.Definition{p.def("gotham.compact")}, quietTick)
				h.start("gotham.compact", tasks.StartRequest{})
				hold, rc := <-p.holds, <-p.rcs
				defer close(p.free)

				if err := rc.Context().Err(); err != nil {
					t.Fatalf("the run's context is done before any lapse: %v", err)
				}
				time.Sleep(h.e.config().StandDownLease + time.Second)
				_ = c.find(h, hold)
				if rc.Context().Err() == nil {
					t.Fatal("the lease lapsed and the run's context is still live")
				}
				if cause := context.Cause(rc.Context()); !errors.Is(cause, tasks.ErrHoldLost) {
					t.Fatalf("the run was canceled with %v, want tasks.ErrHoldLost", cause)
				}
			})
		})
	}
}

// The attempt's write permit reads the lost-hold flag itself: it is set under the
// lock that decided the lapse, before the cancel that follows it, so a write that
// reaches the permit in between is refused too.
func TestHoldWritable_RefusesOnALostHold(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newHoldProbe()
		h := newHarness(t, []tasks.Definition{p.def("gotham.compact")}, quietTick)
		id := h.start("gotham.compact", tasks.StartRequest{})
		<-p.holds
		<-p.rcs
		defer close(p.free)
		a := attemptOf(t, h, id)

		if err := a.writable(); err != nil {
			t.Fatalf("a healthy attempt is not writable: %v", err)
		}
		a.holdLost.Store(true) // the lapse, before its cancel
		err := a.writable()
		if !errors.Is(err, tasks.ErrFenced) || !errors.Is(err, tasks.ErrHoldLost) {
			t.Fatalf("writable with a lost hold and a live context: %v, want ErrFenced wrapping ErrHoldLost", err)
		}
	})
}

// The lease follows the run's reports: a run that only reports progress (a number
// or a bare tick) for longer than the lease keeps its hold, where a run that
// reports nothing loses it. HEAD renewed on every UpdateProgress the same way.
func TestHoldProgress_AReportingRunKeepsItsHold(t *testing.T) {
	for _, c := range []struct {
		name   string
		report func(rc *tasks.RC, i int)
	}{
		{"a progress number", func(rc *tasks.RC, i int) { rc.Progress().Set(int64(i), tasks.Unknown()) }},
		{"a bare tick", func(rc *tasks.RC, _ int) { rc.Progress().Tick() }},
	} {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p := newHoldProbe()
				h := newHarness(t, []tasks.Definition{p.def("gotham.compact")}, synctestConfig) // the reaper runs every second
				h.start("gotham.compact", tasks.StartRequest{})
				hold, rc := <-p.holds, <-p.rcs
				defer close(p.free)
				lease := h.e.config().StandDownLease

				// Twice the lease, a report every minute: no write, no Renew call.
				for i := 1; time.Duration(i)*time.Minute <= 2*lease+time.Minute; i++ {
					time.Sleep(time.Minute)
					c.report(rc, i)
				}
				if !h.e.keyHeld("gotham.scan") {
					t.Fatal("a run that kept reporting lost its hold")
				}
				if err := hold.Renew(); err != nil {
					t.Fatalf("Renew after reports across %v: %v", 2*lease, err)
				}
				if called, err := write(rc, "gotham.compact"); err != nil || !called {
					t.Fatalf("write by a run that kept reporting: err %v, called %v", err, called)
				}
			})
		})
	}
}

// A run that reports nothing for longer than the lease loses its hold: a wedged
// run does not hold the scan down.
func TestHoldProgress_ASilentRunLosesItsHold(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newHoldProbe()
		h := newHarness(t, []tasks.Definition{p.def("gotham.compact")}, synctestConfig)
		h.start("gotham.compact", tasks.StartRequest{})
		hold, rc := <-p.holds, <-p.rcs
		defer close(p.free)

		time.Sleep(h.e.config().StandDownLease + time.Minute)
		if h.e.keyHeld("gotham.scan") {
			t.Fatal("a silent run still holds the key past its lease")
		}
		if err := hold.Renew(); !errors.Is(err, tasks.ErrHoldLost) {
			t.Fatalf("Renew by a silent run: %v, want tasks.ErrHoldLost", err)
		}
		if called, err := write(rc, "gotham.compact"); !errors.Is(err, tasks.ErrFenced) || called {
			t.Fatalf("write by a silent run: err %v, called %v; want ErrFenced and fn never called", err, called)
		}
	})
}

// A marker write decided before a release, whose write runs after it, must not
// bring the marker back: boot would keep the runs the hold quiesced down on a
// marker nobody owns.
func TestMarker_ARestageDecidedBeforeAReleaseNeverLandsAfterIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newHoldProbe()
		h := newHarness(t, []tasks.Definition{p.def("gotham.compact")}, quietTick)
		h.start("gotham.compact", tasks.StartRequest{})
		hold := <-p.holds
		<-p.rcs
		defer close(p.free)
		if _, ok := markers(t, h)["gotham.scan"]; !ok {
			t.Fatal("no marker while the hold lives")
		}

		// A renewal decides to re-stage the marker ...
		h.e.mu.Lock()
		stale := markerWrite{key: "gotham.scan", m: &tasks.StandDown{Key: "gotham.scan", Holder: "riddler"}, ticket: h.e.markerTicketLocked("gotham.scan")}
		h.e.mu.Unlock()
		// ... the hold is released and its clear lands ...
		hold.Release()
		if got := markers(t, h); len(got) != 0 {
			t.Fatalf("the release left markers %v", got)
		}
		// ... and then the re-staging's write runs.
		if err := h.e.writeMarkers(context.Background(), stale); err != nil {
			t.Fatalf("writeMarkers: %v", err)
		}
		if got := markers(t, h); len(got) != 0 {
			t.Fatalf("a stale re-staging brought the marker back: %v", got)
		}
	})
}

// A clear decided before a new hold took the key, whose write runs after the new
// hold's marker landed, must not wipe it.
func TestMarker_AClearDecidedBeforeANewHoldNeverLandsOnIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		first, second := newHoldProbe(), newHoldProbe()
		h := newHarness(t, []tasks.Definition{first.def("gotham.compact"), second.def("arkham.compact")}, quietTick)
		h.start("gotham.compact", tasks.StartRequest{})
		<-first.holds
		<-first.rcs
		defer close(first.free)

		// An older decision to clear the key's marker ...
		h.e.mu.Lock()
		clear := markerWrite{key: "gotham.scan", ticket: h.e.markerTicketLocked("gotham.scan")}
		h.e.mu.Unlock()
		// ... then a hold takes the key and stages its own marker ...
		h.start("arkham.compact", tasks.StartRequest{})
		<-second.holds
		<-second.rcs
		defer close(second.free)
		if _, ok := markers(t, h)["gotham.scan"]; !ok {
			t.Fatal("the new hold staged no marker")
		}
		// ... and only then does the older clear run.
		if err := h.e.writeMarkers(context.Background(), clear); err != nil {
			t.Fatalf("writeMarkers: %v", err)
		}
		if _, ok := markers(t, h)["gotham.scan"]; !ok {
			t.Fatal("an older clear wiped the newer hold's marker")
		}
	})
}

// A holder that lets go of a hold the key no longer belongs to (its lease lapsed
// and another run took the key) must not clear the new hold's marker.
func TestMarker_ReleasingAReplacedHoldKeepsTheNewHoldsMarker(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		first, second := newHoldProbe(), newHoldProbe()
		h := newHarness(t, []tasks.Definition{first.def("gotham.compact"), second.def("arkham.compact")}, quietTick)
		h.start("gotham.compact", tasks.StartRequest{})
		hold1 := <-first.holds
		<-first.rcs
		defer close(first.free)

		time.Sleep(h.e.config().StandDownLease + time.Minute) // the first hold lapses; nothing has reaped it
		h.start("arkham.compact", tasks.StartRequest{})
		<-second.holds
		<-second.rcs
		defer close(second.free)
		m, ok := markers(t, h)["gotham.scan"]
		if !ok {
			t.Fatal("the second hold staged no marker")
		}

		hold1.Release() // the first run lets go of its replaced hold
		got, ok := markers(t, h)["gotham.scan"]
		if !ok {
			t.Fatal("releasing a replaced hold cleared the new hold's marker")
		}
		if got.Holder != m.Holder {
			t.Fatalf("the marker's holder changed from %s to %s", m.Holder, got.Holder)
		}
	})
}

// A lapse ends the run's context whichever call finds it, and not only the reaper's:
// the reaper is the slow path (a tick a second), while a write, a progress report,
// a RenewHold and a Hold.Renew find a lapse the moment they run. Each is driven here
// with the reaper quiet, in the two states the lapse is found in: the lease has run
// out and nothing has noticed, and a decision to drop the hold was made (the flag is
// set under the lock) whose cancel has not run yet.
func TestHoldLapse_EveryDetectorCancelsTheRun(t *testing.T) {
	detectors := []struct {
		name   string
		okLost bool // meaningful once the lost flag is already set: Writer.Do refuses at the permit before it renews
		find   func(t *testing.T, rc *tasks.RC, hold tasks.Hold, a *attempt)
	}{
		{"Writer.Do", false, func(t *testing.T, rc *tasks.RC, _ tasks.Hold, _ *attempt) {
			called, err := write(rc, "gotham.compact")
			if !errors.Is(err, tasks.ErrFenced) || !errors.Is(err, tasks.ErrHoldLost) || called {
				t.Fatalf("write on a lapsed hold: err %v, called %v; want ErrFenced wrapping ErrHoldLost and fn never called", err, called)
			}
		}},
		{"RenewHold", true, func(t *testing.T, _ *tasks.RC, _ tasks.Hold, a *attempt) {
			if err := a.RenewHold(context.Background()); !errors.Is(err, tasks.ErrHoldLost) {
				t.Fatalf("RenewHold on a lapsed hold: %v, want tasks.ErrHoldLost", err)
			}
		}},
		{"Progress.Set", true, func(_ *testing.T, rc *tasks.RC, _ tasks.Hold, _ *attempt) { rc.Progress().Set(1, tasks.Unknown()) }},
		{"Hold.Renew", true, func(t *testing.T, _ *tasks.RC, hold tasks.Hold, _ *attempt) {
			if err := hold.Renew(); !errors.Is(err, tasks.ErrHoldLost) {
				t.Fatalf("Hold.Renew on a lapsed hold: %v, want tasks.ErrHoldLost", err)
			}
		}},
	}
	states := []struct {
		name  string
		lapse func(h *harness, a *attempt)
	}{
		{"the lease ran out", func(h *harness, _ *attempt) { time.Sleep(h.e.config().StandDownLease + time.Second) }},
		{"marked lost, cancel pending", func(_ *harness, a *attempt) { a.holdLost.Store(true) }},
	}
	for _, d := range detectors {
		for _, st := range states {
			if st.name != states[0].name && !d.okLost {
				continue
			}
			t.Run(d.name+" / "+st.name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					p := newHoldProbe()
					h := newHarness(t, []tasks.Definition{p.def("gotham.compact")}, quietTick)
					id := h.start("gotham.compact", tasks.StartRequest{})
					hold, rc := <-p.holds, <-p.rcs
					defer close(p.free)
					a := attemptOf(t, h, id)

					st.lapse(h, a)
					if err := rc.Context().Err(); err != nil {
						t.Fatalf("the run's context ended before anything looked at the hold: %v", err)
					}
					d.find(t, rc, hold, a)
					if rc.Context().Err() == nil {
						t.Fatal("the call found the lapse and the run's context is still live")
					}
					if cause := context.Cause(rc.Context()); !errors.Is(cause, tasks.ErrHoldLost) {
						t.Fatalf("the run was canceled with %v, want tasks.ErrHoldLost", cause)
					}
				})
			})
		}
	}
}

// A hold whose lease ran out and that the reaper has not reached is replaced by the
// next run that stands the same key down. The reaper walks only the holds in
// e.holds, so a replaced hold's holders would never be marked lost or canceled by
// it: a silent old holder would keep a live context and a working writer beside the
// new holder. The replacement takes the hold from them itself. (Every dispatch pass
// reaps first, so the window is a lease that expires between a pass and the
// StandDown call; the test calls standDown directly, with no pass in between.)
func TestHoldReplace_TheReplacedHoldsHoldersLoseIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		first := newHoldProbe()
		started, idle := make(chan struct{}, 1), make(chan struct{})
		bystander := taskDef("arkham.compact", func(*tasks.RC, noParams) (string, error) {
			started <- struct{}{}
			<-idle
			return "case closed", nil
		})
		h := newHarness(t, []tasks.Definition{first.def("gotham.compact"), bystander}, quietTick)
		id1 := h.start("gotham.compact", tasks.StartRequest{})
		<-first.holds
		rc1 := <-first.rcs
		defer close(first.free)
		id2 := h.start("arkham.compact", tasks.StartRequest{})
		<-started
		defer close(idle)
		a1, a2 := attemptOf(t, h, id1), attemptOf(t, h, id2)

		time.Sleep(h.e.config().StandDownLease + time.Minute) // the first hold lapses; no pass has reaped it
		hold2, err := h.e.standDown(a2, "gotham.scan")
		if err != nil {
			t.Fatal(err)
		}
		defer hold2.Release()

		if !a1.holdLost.Load() {
			t.Error("the replaced hold's holder was not marked lost")
		}
		if err := rc1.Context().Err(); err == nil {
			t.Error("the replaced hold's holder still has a live context")
		} else if cause := context.Cause(rc1.Context()); !errors.Is(cause, tasks.ErrHoldLost) {
			t.Errorf("the replaced holder was canceled with %v, want tasks.ErrHoldLost", cause)
		}
		if called, err := write(rc1, "gotham.compact"); !errors.Is(err, tasks.ErrHoldLost) || called {
			t.Errorf("write by the replaced holder: err %v, called %v; want ErrHoldLost and fn never called", err, called)
		}
		if a2.holdLost.Load() || a2.ctx.Err() != nil {
			t.Error("the run that took the key over was marked lost or canceled")
		}
		if !h.e.keyHeld("gotham.scan") {
			t.Error("the new holder's hold is not live")
		}
	})
}

// clockProbe is an executor clock that notes whether the executor lock was held
// the first time it was read after arm.
type clockProbe struct {
	e     atomic.Pointer[executor]
	armed atomic.Bool
	state atomic.Int32 // 0 unread, 1 read under e.mu, 2 read without it
}

func (c *clockProbe) now() time.Time {
	if e := c.e.Load(); e != nil && c.armed.CompareAndSwap(true, false) {
		if e.mu.TryLock() {
			e.mu.Unlock()
			c.state.Store(2)
		} else {
			c.state.Store(1)
		}
	}
	return time.Now()
}

// standDown reads its clock under e.mu, as the reaper and the renewals do. A clock
// read before the lock can be stale once the lock is won: a hold that expired in
// between would still look live, and the call would join it and extend a lease
// the reaper (reading its own clock under the lock) had already judged dead.
func TestHoldStandDown_ReadsItsClockUnderTheExecutorLock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clk := &clockProbe{}
		p := newHoldProbe()
		h := newHarness(t, []tasks.Definition{p.def("gotham.compact")}, func(o *Options) {
			quietTick(o)
			o.Now = clk.now
		})
		id := h.start("gotham.compact", tasks.StartRequest{})
		<-p.holds
		<-p.rcs
		defer close(p.free)
		a := attemptOf(t, h, id)
		clk.e.Store(h.e)

		clk.armed.Store(true)
		hold, err := h.e.standDown(a, "gotham.vault")
		if err != nil {
			t.Fatal(err)
		}
		defer hold.Release()
		switch clk.state.Load() {
		case 0:
			t.Fatal("standDown never read the clock")
		case 2:
			t.Fatal("standDown read its clock without holding the executor lock")
		}
	})
}
