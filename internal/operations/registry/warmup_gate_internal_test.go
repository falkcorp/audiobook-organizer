// file: internal/operations/registry/warmup_gate_test.go
// version: 2.0.2
// guid: 8a4d2f61-7e03-4c9b-b5a8-1d6e3f0c7b92
// last-edited: 2026-10-10

package registry

import (
	"log/slog"
	"testing"
	"time"
)

type fakeStatus struct{ done bool }

func (f *fakeStatus) WarmupStatus() (bool, bool, int64) { return f.done, f.done, 0 }

func TestWarmupGate_NoSourceNeverHolds(t *testing.T) {
	var g WarmupGate
	if g.Holding(time.Now(), nil, slog.Default()) {
		t.Fatal("held with no warmup source")
	}
}

func TestWarmupGate_HoldsWhileWarmingThenReleases(t *testing.T) {
	var g WarmupGate
	s := &fakeStatus{}
	now := time.Unix(1000, 0)
	if !g.Holding(now, s, slog.Default()) {
		t.Fatal("did not hold while warming")
	}
	if !g.Holding(now.Add(10*time.Second), s, slog.Default()) {
		t.Fatal("did not keep holding while warming")
	}
	s.done = true
	if g.Holding(now.Add(11*time.Second), s, slog.Default()) {
		t.Fatal("still held after warmup finished")
	}
}

// The bound: a warmup that never finishes releases held work after 300 s,
// measured from the first cycle that saw it warming.
func TestWarmupGate_ReleasesAtTheBound(t *testing.T) {
	var g WarmupGate
	s := &fakeStatus{}
	t0 := time.Unix(1000, 0)
	if !g.Holding(t0, s, slog.Default()) {
		t.Fatal("did not hold")
	}
	if !g.Holding(t0.Add(WarmupWaitTimeout-time.Second), s, slog.Default()) {
		t.Fatal("released before the bound")
	}
	if g.Holding(t0.Add(WarmupWaitTimeout), s, slog.Default()) {
		t.Fatal("still held at the bound")
	}
	if g.Holding(t0.Add(WarmupWaitTimeout+time.Hour), s, slog.Default()) {
		t.Fatal("held again after the bound")
	}
}

// Once warmup is seen done the clock resets, so a later warm-up gets a full bound.
func TestWarmupGate_ResetsAfterDone(t *testing.T) {
	var g WarmupGate
	s := &fakeStatus{}
	t0 := time.Unix(1000, 0)
	g.Holding(t0, s, slog.Default())
	s.done = true
	g.Holding(t0.Add(time.Second), s, slog.Default())
	s.done = false
	later := t0.Add(2 * WarmupWaitTimeout)
	if !g.Holding(later, s, slog.Default()) {
		t.Fatal("a second warm-up was released immediately: the first one's clock leaked")
	}
}

func TestWarmupWaitTimeoutIsFiveMinutes(t *testing.T) {
	if WarmupWaitTimeout != 300*time.Second {
		t.Fatalf("WarmupWaitTimeout = %v, want 300s", WarmupWaitTimeout)
	}
}
