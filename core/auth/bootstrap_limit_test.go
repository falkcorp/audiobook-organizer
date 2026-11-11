// file: core/auth/bootstrap_limit_test.go
// version: 1.0.0
// guid: e2bd5ece-49ec-4009-9e8b-75f12f1214d4
// last-edited: 2026-11-10

package auth

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// Ported from ABO's internal/server/bootstrap_rate_key_test.go (the cases
// that exercise the limiter alone), F4-02. The request-keying cases stay in
// ABO with bootstrap_gate.go.

// testBootstrapLimiter builds a limiter on a controllable clock.
func testBootstrapLimiter(maxKeys, globalMax int) (*BootstrapLimiter, *time.Time) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	l := NewBootstrapLimiter(BootstrapLimits{MaxKeys: maxKeys, GlobalMax: globalMax}, func() time.Time { return now })
	return l, &now
}

func TestBootstrapLimiter_Defaults(t *testing.T) {
	l := NewBootstrapLimiter(BootstrapLimits{}, nil)
	want := BootstrapLimits{PerKey: BootstrapMaxAttempts, GlobalMax: BootstrapGlobalMaxAttempts, MaxKeys: BootstrapMaxTrackedKeys, Window: BootstrapWindow}
	if got := l.Limits(); got != want {
		t.Fatalf("Limits() = %+v, want %+v", got, want)
	}
	custom := BootstrapLimits{PerKey: 2, GlobalMax: 3, MaxKeys: 4, Window: time.Minute}
	if got := NewBootstrapLimiter(custom, nil).Limits(); got != custom {
		t.Fatalf("Limits() = %+v, want %+v", got, custom)
	}
}

func TestBootstrapLimiter_PerKeyWindow(t *testing.T) {
	l, now := testBootstrapLimiter(100, 1000)
	for i := range BootstrapMaxAttempts {
		if !l.Allow("k") {
			t.Fatalf("attempt %d refused early", i)
		}
	}
	if l.Allow("k") {
		t.Fatal("attempt past the per-key cap admitted")
	}
	if got := l.Count("k"); got != BootstrapMaxAttempts {
		t.Fatalf("Count = %d, want %d (the refusal recorded nothing)", got, BootstrapMaxAttempts)
	}
	if !l.Allow("other") {
		t.Fatal("another key was refused")
	}
	*now = now.Add(BootstrapWindow)
	if l.Allow("k") {
		t.Fatal("a window is still live at exactly its length")
	}
	*now = now.Add(time.Second)
	if l.Count("k") != 0 {
		t.Fatal("an ended window still counts")
	}
	if !l.Allow("k") {
		t.Fatal("the key's window ended but it is still refused")
	}
}

// Ended windows are dropped on the next call, so the map does not hold them.
func TestBootstrapLimiter_SweepsExpiredEntries(t *testing.T) {
	l, now := testBootstrapLimiter(1000, 1000)
	for i := range 50 {
		l.Allow(fmt.Sprintf("k%d", i))
	}
	if l.Len() != 50 {
		t.Fatalf("size = %d, want 50", l.Len())
	}
	*now = now.Add(BootstrapWindow/2 + time.Second)
	l.Allow("late") // a younger window that must survive the sweep below
	*now = now.Add(BootstrapWindow/2 + time.Second)
	l.Allow("trigger")
	if l.Len() != 2 {
		t.Fatalf("size after the first 50 windows ended = %d, want 2 (late, trigger)", l.Len())
	}
}

// Rotating keys cannot grow the map past the cap; the oldest windows go
// first.
func TestBootstrapLimiter_HardCapEvictsOldest(t *testing.T) {
	const maxKeys = 8
	l, now := testBootstrapLimiter(maxKeys, 1_000_000)
	for i := range 10 * maxKeys {
		*now = now.Add(time.Second)
		l.Allow(fmt.Sprintf("k%d", i))
		if l.Len() > maxKeys {
			t.Fatalf("size = %d after %d keys, cap is %d", l.Len(), i+1, maxKeys)
		}
	}
	if l.Len() != maxKeys {
		t.Fatalf("size = %d, want the cap %d", l.Len(), maxKeys)
	}
	if l.Count("k0") != 0 || l.Count(fmt.Sprintf("k%d", 10*maxKeys-1)) != 1 {
		t.Fatalf("eviction order wrong: oldest %d, newest %d", l.Count("k0"), l.Count(fmt.Sprintf("k%d", 10*maxKeys-1)))
	}
}

// However many distinct keys appear, the total admitted per window is
// bounded; refused attempts spend nothing.
func TestBootstrapLimiter_GlobalCeiling(t *testing.T) {
	const globalMax = 12
	l, now := testBootstrapLimiter(1000, globalMax)
	for i := range globalMax {
		if !l.Allow(fmt.Sprintf("k%d", i)) {
			t.Fatalf("distinct key %d refused under the ceiling", i)
		}
	}
	if l.Allow("fresh") {
		t.Fatal("a fresh key was admitted past the global ceiling")
	}
	if l.Len() != globalMax || l.GlobalCount() != globalMax {
		t.Fatalf("a refused attempt was recorded: size %d, global %d, want %d", l.Len(), l.GlobalCount(), globalMax)
	}
	*now = now.Add(BootstrapWindow + time.Second)
	if l.GlobalCount() != 0 {
		t.Fatal("an ended global window still counts")
	}
	if !l.Allow("fresh") {
		t.Fatal("the window ended but the global ceiling still refuses")
	}

	// A key refused by its OWN cap does not spend the global budget.
	l, _ = testBootstrapLimiter(1000, BootstrapMaxAttempts+1)
	for range BootstrapMaxAttempts {
		l.Allow("k")
	}
	for range 50 {
		l.Allow("k") // refused per key
	}
	if !l.Allow("other") {
		t.Fatal("per-key refusals spent the global budget")
	}
}

// The global ceiling has no exempt key: every key is refused once it is
// spent, and every admitted attempt spends it.
func TestBootstrapLimiter_EveryKeyCountsAgainstTheGlobalCeiling(t *testing.T) {
	const globalMax = 3
	l, _ := testBootstrapLimiter(1000, globalMax)
	for i := range globalMax {
		if !l.Allow(fmt.Sprintf("tunnel%d", i)) {
			t.Fatalf("key %d refused under the ceiling", i)
		}
	}
	if l.Allow("tunnel-extra") {
		t.Fatal("a key was admitted past the global ceiling")
	}
	if l.Allow("direct:192.0.2.50") {
		t.Fatal("a fresh direct-peer key escaped the global ceiling")
	}
	l2, _ := testBootstrapLimiter(1000, globalMax)
	for i := range globalMax {
		if !l2.Allow("direct:192.0.2.50", LimiterAggregate{Key: "direct-lan:all", Factor: BootstrapDirectLANTotalFactor}) {
			t.Fatalf("direct attempt %d refused under the ceiling", i)
		}
	}
	if l2.Allow("tunnel0") {
		t.Fatal("direct attempts did not spend the global budget")
	}
}

// An attempt refused by an aggregate spends nothing: the per-key, aggregate
// and global counts stay exactly at what was admitted.
func TestBootstrapLimiter_RefusedByAnAggregateSpendsNothing(t *testing.T) {
	l, _ := testBootstrapLimiter(BootstrapMaxTrackedKeys, 1000)
	total := LimiterAggregate{Key: "unlisted-lan:all", Factor: 1}
	for i := range BootstrapMaxAttempts {
		if !l.Allow(fmt.Sprintf("k%d", i), total) {
			t.Fatalf("attempt %d refused before the total was reached", i)
		}
	}
	if l.Allow("fresh", total) {
		t.Fatal("attempt past the total admitted")
	}
	if got := l.Count("fresh"); got != 0 {
		t.Fatalf("the refused attempt recorded %d against its own key, want 0", got)
	}
	if got := l.Count(total.Key); got != BootstrapMaxAttempts {
		t.Fatalf("aggregate count %d, want %d", got, BootstrapMaxAttempts)
	}
	if got := l.GlobalCount(); got != BootstrapMaxAttempts {
		t.Fatalf("global count %d, want %d (the refused attempt spent it)", got, BootstrapMaxAttempts)
	}
	// Two aggregates: the second refuses, the first is not spent either.
	l2, _ := testBootstrapLimiter(BootstrapMaxTrackedKeys, 1000)
	wide, narrow := LimiterAggregate{Key: "wide", Factor: 10}, LimiterAggregate{Key: "narrow", Factor: 1}
	for i := range BootstrapMaxAttempts {
		l2.Allow(fmt.Sprintf("n%d", i), narrow)
	}
	if l2.Allow("x", wide, narrow) {
		t.Fatal("the narrow aggregate did not refuse")
	}
	if l2.Count(wide.Key) != 0 {
		t.Fatalf("a refused attempt spent the wide aggregate: %d", l2.Count(wide.Key))
	}
}

// Concurrent attempts never admit past the per-key cap.
func TestBootstrapLimiter_ConcurrentAttemptsHonourTheCap(t *testing.T) {
	l, _ := testBootstrapLimiter(1000, 1000)
	var mu sync.Mutex
	admitted := 0
	var wg sync.WaitGroup
	for range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.Allow("k") {
				mu.Lock()
				admitted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if admitted != BootstrapMaxAttempts {
		t.Fatalf("%d concurrent attempts admitted, want %d", admitted, BootstrapMaxAttempts)
	}
}
