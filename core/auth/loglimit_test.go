// file: core/auth/loglimit_test.go
// version: 1.0.0
// guid: 0148ff5b-8c97-4f5a-aa33-ecc5defb8ea1
// last-edited: 2026-11-11

package auth

import (
	"fmt"
	"testing"
	"time"
)

func TestLogLimiterOncePerIntervalPerKey(t *testing.T) {
	l := logLimiter{every: time.Minute}
	t0 := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	if ok, held := l.allow("wayne", t0); !ok || held != 0 {
		t.Fatalf("first: %v %d", ok, held)
	}
	for i := range 5 {
		if ok, _ := l.allow("wayne", t0.Add(time.Duration(i)*time.Second)); ok {
			t.Fatal("a second message inside the interval was allowed")
		}
	}
	if ok, held := l.allow("kane", t0.Add(time.Second)); !ok || held != 0 {
		t.Fatalf("another key shares the slot: %v %d", ok, held)
	}
	if ok, _ := l.allow("wayne", t0.Add(time.Minute-time.Nanosecond)); ok {
		t.Fatal("allowed one tick early")
	}
	if ok, held := l.allow("wayne", t0.Add(time.Minute)); !ok || held != 6 {
		t.Fatalf("after the interval: %v held %d, want held 6", ok, held)
	}
}

func TestLogLimiterIsBounded(t *testing.T) {
	l := logLimiter{every: time.Hour}
	t0 := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	for i := range 3 * maxLogKeys {
		l.allow(fmt.Sprint("k", i), t0.Add(time.Duration(i)*time.Second))
	}
	if len(l.slots) > maxLogKeys {
		t.Fatalf("%d slots, bound %d", len(l.slots), maxLogKeys)
	}
}
