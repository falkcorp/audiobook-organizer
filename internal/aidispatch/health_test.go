// file: internal/aidispatch/health_test.go
// version: 1.0.0
// guid: 558451cd-703f-4958-ad79-ef26d43d66e3
// last-edited: 2026-09-13

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
