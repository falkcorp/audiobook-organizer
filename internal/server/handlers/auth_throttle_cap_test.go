// file: internal/server/handlers/auth_throttle_cap_test.go
// version: 1.0.0
// guid: 7f931f16-c01b-418f-958b-a0a8d394961d
// last-edited: 2026-10-21

package handlers

import (
	"fmt"
	"testing"
	"time"
)

// Rotating source addresses cannot grow the failure tables past maxThrottleKeys,
// and the oldest window is the one evicted.
func TestBumpFailureLocked_TableIsCappedOldestFirst(t *testing.T) {
	m := make(map[string]*failedAttempt)
	base := time.Now()
	for i := 0; i < maxThrottleKeys; i++ {
		m[fmt.Sprintf("k-%d", i)] = &failedAttempt{count: 1, firstAt: base.Add(time.Duration(i) * time.Millisecond)}
	}
	if got := bumpFailureLocked(m, "fresh"); got != 1 {
		t.Fatalf("new key count = %d", got)
	}
	if len(m) != maxThrottleKeys {
		t.Fatalf("table size %d, want %d", len(m), maxThrottleKeys)
	}
	if _, ok := m["k-0"]; ok {
		t.Fatal("the oldest entry should have been evicted")
	}
	if _, ok := m["fresh"]; !ok {
		t.Fatal("the new key must be tracked")
	}
	// An existing key at the cap evicts nothing.
	if got := bumpFailureLocked(m, "k-7"); got != 2 || len(m) != maxThrottleKeys {
		t.Fatalf("existing key: count=%d len=%d", got, len(m))
	}
}

// Expired entries are swept periodically, not on every failure.
func TestRecordFailure_SweepsExpiredEntriesPeriodically(t *testing.T) {
	h := NewAuthHandler(nil, true)
	h.ipFails["old"] = &failedAttempt{count: 3, firstAt: time.Now().Add(-2 * loginThrottleWindow)}
	h.lastPurge = time.Now() // swept a moment ago: not due
	h.recordFailure("", "a")
	if _, ok := h.ipFails["old"]; !ok {
		t.Fatal("an expired entry was swept before the sweep interval elapsed")
	}
	h.lastPurge = time.Now().Add(-2 * throttlePurgeEvery)
	h.recordFailure("", "b")
	if _, ok := h.ipFails["old"]; ok {
		t.Fatal("the expired entry should be gone after the sweep interval")
	}
}
