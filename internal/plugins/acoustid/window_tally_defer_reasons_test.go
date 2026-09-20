// file: internal/plugins/acoustid/window_tally_defer_reasons_test.go
// version: 1.0.0
// guid: 9b3f27d4-61ac-4e08-ae53-1c6f0a4d72be
// last-edited: 2026-09-20

package acoustid

import (
	"strings"
	"sync"
	"testing"
)

// deferItem increments BOTH the per-reason map and the deferred total, so these
// tests never add to `deferred` themselves.
//
// The heartbeat is the ONLY place these reasons surface during a run that takes
// ~14 hours over the full library, so these tests assert on summary() -- the
// string the operator actually reads -- and not on deferredByString alone.

func TestWindowTallySummary_ShowsDeferralReasons(t *testing.T) {
	var tally windowTally
	for i := 0; i < 3; i++ {
		tally.deferItem("not_under_libroot")
	}
	tally.deferItem("unknown_duration")

	got := tally.summary()
	if !strings.Contains(got, "deferred_server_only=4") {
		t.Fatalf("summary lost the deferral total: %q", got)
	}
	// A bare total cannot distinguish a permanent cause from a retryable one;
	// that indistinguishability is the bug this test exists to prevent.
	if !strings.Contains(got, "(not_under_libroot=3 unknown_duration=1)") {
		t.Fatalf("summary does not carry the by-reason split: %q", got)
	}
}

// Ordering is count-descending then name-ascending so an operator diffing two
// successive heartbeats sees real movement rather than Go's map iteration order.
func TestWindowTallySummary_ReasonOrderIsDeterministic(t *testing.T) {
	var tally windowTally
	counts := map[string]int{"no_window_plan": 2, "not_under_libroot": 9, "unknown_duration": 2}
	for why, n := range counts {
		for i := 0; i < n; i++ {
			tally.deferItem(why)
		}
	}
	want := "(not_under_libroot=9 no_window_plan=2 unknown_duration=2)"
	for i := 0; i < 20; i++ {
		if got := tally.summary(); !strings.Contains(got, want) {
			t.Fatalf("iteration %d: want %q inside %q", i, want, got)
		}
	}
}

// No deferrals at all must not append an empty "()" to every heartbeat of a
// healthy run.
func TestWindowTallySummary_NoDeferralsNoClause(t *testing.T) {
	var tally windowTally
	tally.written.Add(5)
	got := tally.summary()
	if strings.Contains(got, "deferred_server_only") || strings.Contains(got, "(") {
		t.Fatalf("clean run grew a deferral clause: %q", got)
	}
}

// deferItem runs from the result-handling path while the heartbeat reads; the
// race detector must see the map access serialized by deferMu.
func TestWindowTallySummary_ConcurrentWithDeferItem(t *testing.T) {
	var tally windowTally
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				tally.deferItem("not_under_libroot")
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			_ = tally.summary()
		}
	}()
	wg.Wait()

	if got := tally.summary(); !strings.Contains(got, "not_under_libroot=800") {
		t.Fatalf("lost counts under concurrency: %q", got)
	}
}
