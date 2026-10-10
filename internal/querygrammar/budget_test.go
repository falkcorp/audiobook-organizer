// file: internal/querygrammar/budget_test.go
// version: 1.0.0
// guid: d07d5b75-f7a4-4ae5-ac23-7644c50ade5c
// last-edited: 2026-10-10

package querygrammar

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeClock advances step on every read, so each timed match costs exactly
// step (the end read minus the start read).
type fakeClock struct {
	mu   sync.Mutex
	t    time.Time
	step time.Duration
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(c.step)
	return c.t
}

func mustCompile(t *testing.T, raw string) *TextMatcher {
	t.Helper()
	m, err := CompileText(raw, false)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// TestBudget_ChargesOnlyPatterns: regex and glob matches are timed;
// substring and non-empty matches are not, whatever the clock says.
func TestBudget_ChargesOnlyPatterns(t *testing.T) {
	clk := &fakeClock{step: time.Millisecond}
	b := NewBudgetClock(time.Second, clk.now)
	sub, nonEmpty := mustCompile(t, "title"), mustCompile(t, "*")
	for range 10000 {
		b.Match(sub, "a title")
		b.Match(nonEmpty, "x")
	}
	if b.Spent() != 0 || b.Expired() {
		t.Fatalf("substring/non-empty charged %s (expired=%v)", b.Spent(), b.Expired())
	}
	re, glob := mustCompile(t, "/tit/"), mustCompile(t, "a*")
	if !b.Match(re, "a title") || !b.Match(glob, "abc") {
		t.Fatal("a timed match must still match")
	}
	if b.Spent() != 2*time.Millisecond {
		t.Fatalf("spent %s, want 2ms (1ms per timed match)", b.Spent())
	}
}

// TestBudget_StopsAtLimitPerMatch: the budget expires on the match that
// spends it, however many tokens share it, and every later match is refused
// without running. 40 tokens of 1 ms each over rows spend a 100 ms budget on
// match 100: the per-token clock is exact, with no rows-per-check guess.
func TestBudget_StopsAtLimitPerMatch(t *testing.T) {
	clk := &fakeClock{step: time.Millisecond} // end read minus start read = 1 ms per match
	b := NewBudgetClock(100*time.Millisecond, clk.now)
	tokens := make([]*TextMatcher, 40)
	for i := range tokens {
		tokens[i] = mustCompile(t, `/(?:\pL?){45}zzz/`)
	}
	matches := 0
rows:
	for range 1000 {
		for _, m := range tokens {
			if b.Expired() {
				break rows
			}
			b.Match(m, "a title")
			matches++
		}
	}
	if matches != 100 {
		t.Fatalf("ran %d matches, want exactly 100 (100 ms / 1 ms)", matches)
	}
	var slow *TooSlowError
	if !errors.As(b.Err(), &slow) || slow.Budget != 100*time.Millisecond {
		t.Fatalf("Err() = %v, want a *TooSlowError for 100ms", b.Err())
	}
	if b.Match(tokens[0], "zzz") {
		t.Fatal("a spent budget must refuse further matches")
	}
	if !strings.Contains(b.Err().Error(), "too slow") || !strings.Contains(b.Err().Error(), "simpler search") {
		t.Fatalf("message: %s", b.Err())
	}
}

// TestBudget_RealTimeStopsNearLimit: with the wall clock, a slow allowed
// pattern over long input stops within one match of the limit.
func TestBudget_RealTimeStopsNearLimit(t *testing.T) {
	const limit = 30 * time.Millisecond
	b := NewBudget(limit)
	m := mustCompile(t, `/(?:\pL?){45}zzz/`)
	title := strings.Repeat("Long Title Words ", 5)
	start := time.Now()
	for !b.Expired() {
		b.Match(m, title)
	}
	took := time.Since(start)
	t.Logf("stopped after %s (limit %s, spent %s)", took, limit, b.Spent())
	// Loose: the bound that matters is "one match past the limit"; the
	// wall-clock slack covers scheduling under -race and load.
	if took > limit+time.Second {
		t.Fatalf("took %s against a %s budget", took, limit)
	}
}

func TestBudget_NilAndZero(t *testing.T) {
	var b *Budget
	m := mustCompile(t, "/x/")
	if !b.Match(m, "x") || b.Expired() || b.Err() != nil {
		t.Fatal("a nil budget times nothing and never expires")
	}
	z := NewBudget(0)
	if !z.Expired() || z.Match(m, "x") {
		t.Fatal("a zero budget is spent before it starts")
	}
}

// TestPatternSlots_SaturationIsBusy: with every slot held, a further acquire
// waits the slot wait and returns a *BusyError, and a released slot is
// usable again.
func TestPatternSlots_SaturationIsBusy(t *testing.T) {
	restore := SetPatternSlotsForTesting(2, 20*time.Millisecond)
	defer restore()
	r1, err1 := AcquirePatternSlot(context.Background())
	r2, err2 := AcquirePatternSlot(context.Background())
	if err1 != nil || err2 != nil {
		t.Fatal(err1, err2)
	}
	start := time.Now()
	_, err := AcquirePatternSlot(context.Background())
	var busy *BusyError
	if !errors.As(err, &busy) || busy.Slots != 2 {
		t.Fatalf("err = %v, want a *BusyError over 2 slots", err)
	}
	if waited := time.Since(start); waited < 20*time.Millisecond {
		t.Fatalf("returned busy after %s, before the 20ms wait", waited)
	}
	r1()
	r3, err := AcquirePatternSlot(context.Background())
	if err != nil {
		t.Fatalf("a released slot must be usable: %v", err)
	}
	r2()
	r3()
	// A cancelled caller stops waiting.
	ra, _ := AcquirePatternSlot(context.Background())
	rb, _ := AcquirePatternSlot(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := AcquirePatternSlot(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled wait: %v", err)
	}
	ra()
	rb()
}

func TestShortToken(t *testing.T) {
	if got := ShortToken("short"); got != "short" {
		t.Fatal(got)
	}
	long := strings.Repeat("é", 40) // 80 bytes, 2 per rune
	got := ShortToken(long)
	if !strings.HasSuffix(got, "…") || len(got) > 64+len("…") || !strings.HasPrefix(long, strings.TrimSuffix(got, "…")) {
		t.Fatalf("ShortToken = %q", got)
	}
}
