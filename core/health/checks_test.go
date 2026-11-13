// file: core/health/checks_test.go
// version: 1.0.0
// guid: 4a7d1c93-8e52-4b60-9f3a-7c2e5b8d1a46
// last-edited: 2026-11-13

package health

import (
	"context"
	"runtime"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func timingsOf(t Timings) func() Timings { return func() Timings { return t } }

// TestCheck_PanicAndTimeoutBecomeDown: a probe that panics is Down check_panic
// and one that outlives its timeout is Down check_timeout, in the loop as at
// Register; neither reaches the loop, a probe that is still running is never
// joined by a second call of itself, and a probe that comes back to life
// recovers the component (05-health §5H.5, §5H.6).
func TestCheck_PanicAndTimeoutBecomeDown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := NewRegistryWith(Options{Timings: timingsOf(Timings{CheckInterval: time.Minute, CheckTimeout: 5 * time.Second})})
		r.noJitter = true
		release := make(chan struct{})
		var panicCalls, hangCalls atomic.Int32
		mustRegister(t, r, Check{ID: "arkham.panic", Probe: func(context.Context) Result {
			if panicCalls.Add(1) > 1 {
				panic("scarecrow gas")
			}
			return Result{Status: StatusUp}
		}})
		mustRegister(t, r, Check{ID: "arkham.hang", Probe: func(context.Context) Result {
			if hangCalls.Add(1) > 1 {
				<-release // ignores its context: only a goroutine abandon can free the loop
			}
			return Result{Status: StatusUp}
		}})
		stop := r.StartChecks(t.Context())

		time.Sleep(time.Minute + 10*time.Second)
		synctest.Wait()
		got := stateOf(r, "arkham.panic")
		if got.Result.Status != StatusDown || got.Result.Reason != "check_panic" {
			t.Errorf("panicking probe = %+v, want Down check_panic", got.Result)
		}
		got = stateOf(r, "arkham.hang")
		if got.Result.Status != StatusDown || got.Result.Reason != "check_timeout" {
			t.Errorf("hung probe = %+v, want Down check_timeout", got.Result)
		}
		if got.Duration != 5*time.Second {
			t.Errorf("hung probe duration = %s, want the 5s timeout", got.Duration)
		}

		// Two more intervals: the abandoned call still runs, so the loop does
		// not start a second one, and the panicking probe keeps being called.
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		if n := hangCalls.Load(); n != 2 {
			t.Errorf("hung probe called %d times, want 2 (Register's and the first tick's): never concurrently with itself", n)
		}
		if n := panicCalls.Load(); n < 3 {
			t.Errorf("panicking probe called %d times, want the loop to survive the panic", n)
		}

		close(release)
		synctest.Wait()
		time.Sleep(time.Minute)
		synctest.Wait()
		if got := stateOf(r, "arkham.hang").Result; got.Status != StatusUp {
			t.Errorf("hung probe after it freed = %+v, want Up on the next tick", got)
		}
		stop()
	})
}

func stateOf(r *Registry, id ComponentID) ComponentState {
	for _, c := range r.Snapshot().Components {
		if c.ID == id {
			return c
		}
	}
	return ComponentState{}
}

// TestRegisterTimeProbeThatIgnoresItsContextCannotBlockRegister: Register's
// one synchronous probe call is bounded too.
func TestRegisterTimeProbeThatIgnoresItsContextCannotBlockRegister(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := NewRegistryWith(Options{Timings: timingsOf(Timings{CheckTimeout: 3 * time.Second})})
		release := make(chan struct{})
		start := time.Now()
		mustRegister(t, r, Check{ID: "arkham.stuck", Probe: func(context.Context) Result { <-release; return Result{} }})
		if d := time.Since(start); d != 3*time.Second {
			t.Errorf("Register took %s, want the 3s timeout", d)
		}
		if got := stateOf(r, "arkham.stuck").Result; got.Reason != "check_timeout" {
			t.Errorf("result = %+v, want Down check_timeout", got)
		}
		close(release)
	})
}

// TestChecks_LoopLifecycle: an unstarted registry runs no probe and starts no
// goroutine; checks run per interval; a component registered while they run
// joins them; stop ends every loop and a second StartChecks is inert.
func TestChecks_LoopLifecycle(t *testing.T) {
	before := runtime.NumGoroutine()
	r := NewRegistryWith(Options{})
	var calls atomic.Int32
	mustRegister(t, r, Check{ID: "wayne.cave", Probe: func(context.Context) Result { calls.Add(1); return Result{Status: StatusUp} }})
	if runtime.NumGoroutine() > before {
		t.Fatalf("NewRegistry/Register started a goroutine (%d -> %d)", before, runtime.NumGoroutine())
	}
	if calls.Load() != 1 {
		t.Fatalf("probe ran %d times before StartChecks, want Register's one", calls.Load())
	}

	synctest.Test(t, func(t *testing.T) {
		r := NewRegistryWith(Options{Timings: timingsOf(Timings{CheckInterval: 10 * time.Second})})
		r.noJitter = true
		var a, b atomic.Int32
		mustRegister(t, r, Check{ID: "wayne.a", Probe: func(context.Context) Result { a.Add(1); return Result{Status: StatusUp} }})
		stop := r.StartChecks(t.Context())
		second := r.StartChecks(t.Context()) // inert
		time.Sleep(35 * time.Second)
		synctest.Wait()
		if n := a.Load(); n != 4 { // Register + ticks at 10, 20, 30
			t.Errorf("probe ran %d times in 35s, want 4", n)
		}
		mustRegister(t, r, Check{ID: "wayne.b", Interval: 5 * time.Second, Probe: func(context.Context) Result { b.Add(1); return Result{Status: StatusUp} }})
		time.Sleep(20 * time.Second)
		synctest.Wait()
		if n := b.Load(); n != 5 { // Register + ticks at 5, 10, 15, 20: its own interval
			t.Errorf("late component ran %d times in 20s, want 5", n)
		}
		second() // must not stop the real loops
		time.Sleep(10 * time.Second)
		synctest.Wait()
		if a.Load() < 7 {
			t.Errorf("loops stopped by the inert second stop: a ran %d times", a.Load())
		}
		stop()
		na := a.Load()
		time.Sleep(time.Minute)
		synctest.Wait()
		if a.Load() != na {
			t.Errorf("probe ran after stop: %d -> %d", na, a.Load())
		}
	})
}

// TestHeartbeat_StallFailsLivezAndStopsWatchdog: while the heartbeat ticks the
// registry is live and the watchdog is pinged at half of WatchdogSec; when a
// work func wedges the tick, no beat is recorded, Live goes false after
// core.health.live_stall and the pings stop, so the supervisor restarts the
// process (05-health §5H.3.3, §5H.3.4).
func TestHeartbeat_StallFailsLivezAndStopsWatchdog(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		n := &fakeNotifier{active: true, watchdog: 30 * time.Second}
		sup := NewSupervisor(nil, n)
		r := NewRegistryWith(Options{Timings: timingsOf(Timings{LiveStall: time.Minute})})
		var wedged atomic.Bool
		wedge := make(chan struct{})
		stop := r.StartHeartbeat(t.Context(), sup, func() {
			if wedged.Load() {
				<-wedge
			}
		})
		if !r.Live() {
			t.Fatal("not live right after StartHeartbeat")
		}
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		if !r.Live() {
			t.Fatal("not live while the heartbeat ticks")
		}
		if got := n.pings.Load(); got < 6 {
			t.Fatalf("watchdog pinged %d times in 2m at WatchdogSec=30s, want a ping per half period", got)
		}

		wedged.Store(true)
		time.Sleep(20 * time.Second) // the next tick enters the wedged work func
		synctest.Wait()
		before := n.pings.Load()
		time.Sleep(90 * time.Second)
		synctest.Wait()
		if r.Live() {
			t.Error("still live 90s after the heartbeat wedged, want not live after live_stall")
		}
		if r.Snapshot().Live {
			t.Error("Snapshot.Live disagrees with Live")
		}
		if got := n.pings.Load(); got != before {
			t.Errorf("watchdog pinged %d times while the heartbeat was wedged, want none", got-before)
		}

		close(wedge)
		synctest.Wait()
		time.Sleep(20 * time.Second)
		synctest.Wait()
		if !r.Live() {
			t.Error("not live after the wedge cleared and a beat was recorded")
		}
		stop()
		if !r.Live() {
			t.Error("a stopped heartbeat must leave the registry live (not beating)")
		}
	})
}

// TestHeartbeat_NotStartedIsLive: HEAD's behaviour is kept until the kernel
// starts the heartbeat, and a nil registry is not live.
func TestHeartbeat_NotStartedIsLive(t *testing.T) {
	if !NewRegistry().Live() {
		t.Error("an unstarted registry must be live")
	}
	var r *Registry
	if r.Live() {
		t.Error("a nil registry must not be live")
	}
}
