// file: core/health/hardening_test.go
// version: 1.0.1
// guid: 2e6a9d41-7b35-4c80-9f1d-5a3c8e0b7d62
// last-edited: 2026-11-13

package health

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// slowFirstProbe answers Up after took, whatever its context says: a probe
// that ignores its context, as core.storage's engine ping can at boot.
func slowFirstProbe(took time.Duration) func(context.Context) Result {
	return func(context.Context) Result {
		time.Sleep(took)
		return Result{Status: StatusUp}
	}
}

// TestRegister_LateAnswerBeforeStartChecksIsAccepted: a Register-time probe
// that overruns its timeout is Down check_timeout, and when it answers later,
// before StartChecks has run and before any Report, that answer replaces the
// Down. Otherwise core.storage registered at a slow boot would stay Down and
// the executor, which waits on it, would never start.
func TestRegister_LateAnswerBeforeStartChecksIsAccepted(t *testing.T) {
	t.Run("answers late and is accepted", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := NewRegistryWith(Options{Timings: timingsOf(Timings{CheckInterval: time.Hour, CheckTimeout: 5 * time.Second})})
			mustRegister(t, r, Check{ID: "core.storage", Critical: true, Readiness: true, Probe: slowFirstProbe(8 * time.Second)})
			if got := stateOf(r, "core.storage").Result; got.Status != StatusDown || got.Reason != "check_timeout" {
				t.Fatalf("at the timeout: %+v, want Down check_timeout", got)
			}
			if ready, _ := r.Ready(); ready {
				t.Fatal("a Critical component that is Down must not be ready")
			}
			waited := make(chan error, 1)
			go func() { waited <- r.Wait(t.Context(), "core.storage") }()
			time.Sleep(4 * time.Second)
			synctest.Wait()
			if got := stateOf(r, "core.storage").Result; got.Status != StatusUp {
				t.Errorf("after the late answer: %+v, want Up", got)
			}
			if got := stateOf(r, "core.storage").Duration; got != 8*time.Second {
				t.Errorf("duration after the late answer = %s, want the probe's own 8s, not 0", got)
			}
			if err := <-waited; err != nil {
				t.Errorf("Wait: %v", err)
			}
			if ready, nr := r.Ready(); !ready {
				t.Errorf("not ready after the late answer: %v", nr)
			}
		})
	})
	t.Run("a Report in between is newer and wins", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := NewRegistryWith(Options{Timings: timingsOf(Timings{CheckInterval: time.Hour, CheckTimeout: 5 * time.Second})})
			rep := mustRegister(t, r, Check{ID: "core.storage", Probe: slowFirstProbe(8 * time.Second)})
			time.Sleep(time.Second)
			rep.Report(Result{Status: StatusDegraded, Reason: "warming"})
			time.Sleep(3 * time.Second)
			synctest.Wait()
			if got := stateOf(r, "core.storage").Result; got.Status != StatusDegraded || got.Reason != "warming" {
				t.Errorf("a late probe answer overwrote a newer Report: %+v", got)
			}
		})
	})
	t.Run("once StartChecks runs the loop owns the component", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r := NewRegistryWith(Options{Timings: timingsOf(Timings{CheckInterval: time.Hour, CheckTimeout: 5 * time.Second})})
			mustRegister(t, r, Check{ID: "core.storage", Probe: slowFirstProbe(8 * time.Second)})
			stop := r.StartChecks(t.Context())
			time.Sleep(4 * time.Second)
			synctest.Wait()
			if got := stateOf(r, "core.storage").Result; got.Reason != "check_timeout" {
				t.Errorf("a late Register-time answer was accepted after StartChecks: %+v", got)
			}
			stop()
		})
	})
}

// TestStartup_TokensNeverRestorePending: recording a degraded step's token
// leaves whether startup is pending alone, so a step that fails while Complete
// runs cannot put core.kernel:starting back (it used to read pending, then
// write it back).
func TestStartup_TokensNeverRestorePending(t *testing.T) {
	r := NewRegistry()
	r.setStartup(true, nil)
	r.setStartupPending(false)
	r.setStartupTokens([]string{"search_index"})
	if ready, nr := r.Ready(); !ready {
		t.Errorf("a token recorded after Complete made the registry not ready: %v", nr)
	}
	if got := r.Snapshot().Startup; !slices.Equal(got, []string{"search_index"}) {
		t.Errorf("tokens = %v", got)
	}
	// Tokens only grow, so two writers in either order end the same.
	r.setStartupTokens([]string{"metrics_disabled"})
	r.setStartupTokens([]string{"search_index"})
	if got := r.Snapshot().Startup; !slices.Equal(got, []string{"metrics_disabled", "search_index"}) {
		t.Errorf("tokens = %v, want both, sorted", got)
	}
	// And Complete keeps the tokens a step recorded.
	r2 := NewRegistry()
	r2.setStartup(true, nil)
	r2.setStartupTokens([]string{"search_index"})
	r2.setStartupPending(false)
	if got := r2.Snapshot().Startup; !slices.Equal(got, []string{"search_index"}) {
		t.Errorf("Complete lost a token: %v", got)
	}
}

// TestStartup_DegradedStepRacingCompleteStaysReady is the same property under
// -race with the real calls: whichever order a failing Degraded step and
// Complete land in, the registry ends ready with the token recorded.
func TestStartup_DegradedStepRacingCompleteStaysReady(t *testing.T) {
	for i := range 200 {
		r := NewRegistry()
		su, err := NewStartup(r, nil, StartupStep{Name: "searchindex", Class: Degraded, Token: "search_index", Reason: "falls back"})
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = su.Run(context.Background(), "searchindex", func(context.Context) error { return context.Canceled })
		}()
		go func() { defer wg.Done(); su.Complete() }()
		wg.Wait()
		if ready, nr := r.Ready(); !ready {
			t.Fatalf("iteration %d: not ready after Complete: %v", i, nr)
		}
		if got := r.Snapshot().Startup; !slices.Equal(got, []string{"search_index"}) {
			t.Fatalf("iteration %d: tokens = %v", i, got)
		}
	}
}

// TestHeartbeat_SecondStartIsInert: one heartbeat per registry. A second
// StartHeartbeat starts no goroutine (its work never runs), and its stop does
// not mark the first one's registry as not beating.
func TestHeartbeat_SecondStartIsInert(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := NewRegistryWith(Options{Timings: timingsOf(Timings{LiveStall: 4 * time.Second})})
		var first, second atomic.Int32
		stop1 := r.StartHeartbeat(t.Context(), nil, func() { first.Add(1) })
		stop2 := r.StartHeartbeat(t.Context(), nil, func() { second.Add(1) })
		time.Sleep(5 * time.Second)
		synctest.Wait()
		if first.Load() == 0 {
			t.Error("the first heartbeat never ran its work")
		}
		if n := second.Load(); n != 0 {
			t.Errorf("the second heartbeat ran its work %d times, want 0", n)
		}
		stop2()
		if !r.beating.Load() {
			t.Error("the second StartHeartbeat's stop marked the registry as not beating")
		}
		stop1()
		if r.beating.Load() {
			t.Error("the first stop did not end the heartbeat")
		}
		// A stopped heartbeat can be started again.
		stop3 := r.StartHeartbeat(t.Context(), nil, func() { second.Add(1) })
		time.Sleep(5 * time.Second)
		synctest.Wait()
		if second.Load() == 0 {
			t.Error("a heartbeat started after a stop did not run")
		}
		stop3()
	})
}
