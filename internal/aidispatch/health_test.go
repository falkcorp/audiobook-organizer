// file: internal/aidispatch/health_test.go
// version: 1.2.0
// guid: 558451cd-703f-4958-ad79-ef26d43d66e3
// last-edited: 2026-10-28

package aidispatch

import (
	"testing"
	"time"
)

type stepClock struct{ t time.Time }

func (c *stepClock) now() time.Time          { return c.t }
func (c *stepClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func TestHealth_CooldownExpires(t *testing.T) {
	clk := &stepClock{t: time.Unix(1_000_000, 0)}
	h := newHealthWithClock(clk.now)

	if h.InCooldown("a") {
		t.Fatal("never-failed endpoint must not be in cooldown")
	}
	h.MarkFailure("a")
	clk.advance(CooldownBase - time.Second)
	if !h.InCooldown("a") {
		t.Fatal("benched endpoint left cooldown early")
	}
	clk.advance(2 * time.Second)
	if h.InCooldown("a") {
		t.Fatal("cooldown never expired")
	}

	// Second consecutive failure doubles the bench.
	h.MarkFailure("a")
	clk.advance(2*CooldownBase - time.Second)
	if !h.InCooldown("a") {
		t.Fatal("second failure must bench for 2×base")
	}
	clk.advance(2 * time.Second)
	if h.InCooldown("a") {
		t.Fatal("2×base cooldown never expired")
	}
}

func TestHealth_CooldownIsCapped(t *testing.T) {
	clk := &stepClock{t: time.Unix(1_000_000, 0)}
	h := newHealthWithClock(clk.now)
	for range 50 {
		h.MarkFailure("a")
	}
	if got := h.CooldownUntil("a").Sub(clk.t); got != CooldownMax {
		t.Fatalf("cooldown %v, want cap %v", got, CooldownMax)
	}
	clk.advance(CooldownMax + time.Second)
	if h.InCooldown("a") {
		t.Fatal("capped cooldown never expired")
	}
}

func TestHealth_SuccessClearsAndQuotaIsLong(t *testing.T) {
	clk := &stepClock{t: time.Unix(1_000_000, 0)}
	h := newHealthWithClock(clk.now)
	h.MarkFailure("a")
	h.MarkSuccess("a")
	if h.InCooldown("a") {
		t.Fatal("success must clear the cooldown")
	}
	h.MarkFailure("a") // count restarted: back to 1×base
	if got := h.CooldownUntil("a").Sub(clk.t); got != CooldownBase {
		t.Fatalf("after success the failure count must restart; got %v", got)
	}

	h.MarkQuotaExhausted("b")
	clk.advance(CooldownMax + time.Minute)
	if !h.InCooldown("b") {
		t.Fatal("quota exhaustion must bench far longer than an ordinary failure")
	}
	clk.advance(QuotaCooldown)
	if h.InCooldown("b") {
		t.Fatal("quota cooldown never expired")
	}
}

func TestHealth_FailureGenerationOnlyGrows(t *testing.T) {
	clk := &stepClock{t: time.Unix(1_000_000, 0)}
	h := newHealthWithClock(clk.now)
	if g := h.FailureGeneration("a"); g != 0 {
		t.Fatalf("never-failed endpoint generation = %d, want 0", g)
	}
	h.MarkFailure("a")
	if g := h.FailureGeneration("a"); g != 1 {
		t.Fatalf("generation after a failure = %d, want 1", g)
	}
	// A failure whose cooldown began and ended unseen still moved it.
	h.MarkSuccess("a")
	clk.advance(CooldownMax * 2)
	if h.InCooldown("a") {
		t.Fatal("the cooldown should be over")
	}
	if g := h.FailureGeneration("a"); g != 1 {
		t.Fatalf("generation after a success and the end of the cooldown = %d, want it kept at 1", g)
	}
	h.MarkFailure("a")
	h.MarkQuotaExhausted("a")
	if g := h.FailureGeneration("a"); g != 3 {
		t.Fatalf("generation after three failures = %d, want 3", g)
	}
	if g := h.FailureGeneration("b"); g != 0 {
		t.Fatalf("another endpoint's generation = %d, want 0", g)
	}
}

func TestHealth_SetClockForTestEndsACooldownByTimeAndRestores(t *testing.T) {
	h := NewHealth()
	var offset time.Duration
	restore := h.SetClockForTest(func() time.Time { return time.Now().Add(offset) })
	h.MarkFailure("a")
	if !h.InCooldown("a") {
		t.Fatal("a failure must start a cooldown")
	}
	offset = CooldownBase + time.Second
	if h.InCooldown("a") {
		t.Fatal("the cooldown must end when the clock passes it")
	}
	if g := h.FailureGeneration("a"); g != 1 {
		t.Fatalf("generation = %d, want 1: the end of a cooldown does not take it back", g)
	}
	restore()
	if !h.InCooldown("a") {
		t.Fatal("with the wall clock back the cooldown (set by the wall clock plus the offset) is in force again")
	}
}
