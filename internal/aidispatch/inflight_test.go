// file: internal/aidispatch/inflight_test.go
// version: 1.0.0
// guid: 4cae4cfa-03b5-4f98-8c94-0d7f9b397e30
// last-edited: 2026-09-13

package aidispatch

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Behavioural coverage of the registry through its old URL-keyed callers stays
// in internal/transcribe/inflight_test.go. These pin what is specific to the
// ID-keyed form, and the limit asymmetry.

func peakHolding(t *testing.T, s *Slots, id string, limit int, total TotalCap, n int) int64 {
	t.Helper()
	var live, peak atomic.Int64
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			release, err := s.Acquire(context.Background(), id, limit, total)
			if err != nil {
				t.Errorf("acquire: %v", err)
				return
			}
			defer release()
			cur := live.Add(1)
			for {
				old := peak.Load()
				if cur <= old || peak.CompareAndSwap(old, cur) {
					break
				}
			}
			time.Sleep(15 * time.Millisecond)
			live.Add(-1)
		})
	}
	wg.Wait()
	return peak.Load()
}

func TestEffectiveConcurrency(t *testing.T) {
	for in, want := range map[int]int{-3: 1, 0: 1, 1: 1, 4: 4} {
		if got := EffectiveConcurrency(in); got != want {
			t.Errorf("EffectiveConcurrency(%d) = %d, want %d", in, got, want)
		}
	}
}

// 🔴 The asymmetry: per-endpoint 0 means ONE, total 0 means UNLIMITED.
func TestSlots_LimitAsymmetry(t *testing.T) {
	if peak := peakHolding(t, NewSlots(), "ep", 0, TotalCap{}, 10); peak != 1 {
		t.Fatalf("per-endpoint limit 0 must mean 1; peak %d", peak)
	}
	if peak := peakHolding(t, NewSlots(), "ep", 8, TotalCap{Group: "g", Limit: 0}, 10); peak < 3 {
		t.Fatalf("total limit 0 must mean unlimited; peak only %d with per-endpoint 8", peak)
	}
	if peak := peakHolding(t, NewSlots(), "ep", 8, TotalCap{Group: "g", Limit: 2}, 10); peak > 2 {
		t.Fatalf("total limit 2 exceeded: peak %d", peak)
	}
}

// Total caps are per group: whisper's cap must not throttle chat work.
func TestSlots_TotalCapIsPerGroup(t *testing.T) {
	s := NewSlots()
	rel, err := s.Acquire(context.Background(), "a", 5, TotalCap{Group: "whisper", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer rel()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	rel2, err := s.Acquire(ctx, "b", 5, TotalCap{Group: "chat", Limit: 1})
	if err != nil {
		t.Fatalf("a full whisper group blocked a chat acquire: %v", err)
	}
	rel2()
}

func TestSlots_WaitFailureNamesEndpoint(t *testing.T) {
	s := NewSlots()
	rel, err := s.Acquire(context.Background(), "gpu-box", 1, TotalCap{})
	if err != nil {
		t.Fatal(err)
	}
	defer rel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err = s.Acquire(ctx, "gpu-box", 1, TotalCap{})
	if !errors.Is(err, ErrSlotWait) || !strings.Contains(err.Error(), "gpu-box") {
		t.Fatalf("want ErrSlotWait naming the endpoint, got %v", err)
	}
	if s.Depth("gpu-box") != 1 || !s.HasPool("gpu-box") {
		t.Fatalf("failed wait must not change the held count; depth %d", s.Depth("gpu-box"))
	}
}

func TestLegacyURLIDIsPrefixed(t *testing.T) {
	if got := LegacyURLID("http://whisper-1.local:8000"); got != "url:http://whisper-1.local:8000" {
		t.Fatalf("LegacyURLID = %q", got)
	}
}
