// file: internal/server/embedding_backfill_test.go
// version: 1.1.0
// guid: 4f81c2ae-6b39-47d5-9ae1-3c5d8b12f7a4
// last-edited: 2026-07-04

package server

import (
	"fmt"
	"testing"
)

// TestDedupScanProgressLogger_BucketCrossings verifies that a callback driven
// at FullScan's actual step size (done = i+1 with i%10 == 0) emits a log line
// approximately every `interval` books — the scenario the original
// `done%interval == 0` check silently broke.
func TestDedupScanProgressLogger_BucketCrossings(t *testing.T) {
	var lines []string
	logf := func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	}

	progress := newDedupScanProgressLogger(1000, logf)

	// Simulate FullScan calling progress("scan", i+1, total) on every i where
	// i%10 == 0.
	const total = 2500
	for i := 0; i < total; i++ {
		if i%10 == 0 || i == total-1 {
			progress("scan", i+1, total)
		}
	}

	// Expected log lines: one at the first crossing of 1000, one at 2000, and
	// one at total completion (2500). None of those satisfy the buggy
	// `done%1000 == 0` check since done values are always of the form 10k+1.
	if got, want := len(lines), 3; got != want {
		t.Fatalf("expected %d log lines, got %d: %v", want, got, lines)
	}
	// First two should be at bucket crossings near 1000 and 2000.
	if lines[0] != "[INFO] Dedup scan progress (scan): 1001/2500" {
		t.Errorf("first log line = %q", lines[0])
	}
	if lines[1] != "[INFO] Dedup scan progress (scan): 2001/2500" {
		t.Errorf("second log line = %q", lines[1])
	}
	// Last one is the completion line.
	if lines[2] != "[INFO] Dedup scan progress (scan): 2500/2500" {
		t.Errorf("final log line = %q", lines[2])
	}
}

// TestDedupScanProgressLogger_EveryItem exercises the closure against a caller
// that invokes progress for every single item, not just on a 10-step. The
// logger should still only fire once per bucket.
func TestDedupScanProgressLogger_EveryItem(t *testing.T) {
	var lines []string
	logf := func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	}
	progress := newDedupScanProgressLogger(100, logf)

	const total = 350
	for i := 0; i < total; i++ {
		progress("scan", i+1, total)
	}

	// Expected: one log line at each of done=100, 200, 300, and the completion
	// line at done=350 — four lines total.
	if got, want := len(lines), 4; got != want {
		t.Fatalf("expected %d log lines, got %d: %v", want, got, lines)
	}
}

// TestDedupScanProgressLogger_SmallTotal verifies that a scan smaller than the
// interval still emits the final completion line.
func TestDedupScanProgressLogger_SmallTotal(t *testing.T) {
	var lines []string
	progress := newDedupScanProgressLogger(1000, func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	})

	const total = 42
	for i := 0; i < total; i++ {
		if i%10 == 0 || i == total-1 {
			progress("scan", i+1, total)
		}
	}

	if len(lines) != 1 {
		t.Fatalf("expected 1 log line (completion only), got %d: %v", len(lines), lines)
	}
	if lines[0] != "[INFO] Dedup scan progress (scan): 42/42" {
		t.Errorf("completion line = %q", lines[0])
	}
}

// TestDedupScanProgressLogger_NonPositiveInterval defends against a caller
// passing 0 or a negative interval — the logger should degrade gracefully by
// treating the interval as 1 (log every call) rather than div-by-zero or loop
// forever.
func TestDedupScanProgressLogger_NonPositiveInterval(t *testing.T) {
	count := 0
	progress := newDedupScanProgressLogger(0, func(format string, args ...any) {
		count++
	})
	for i := 0; i < 5; i++ {
		progress("scan", i+1, 5)
	}
	if count != 5 {
		t.Errorf("interval=0 should log every call; got %d calls", count)
	}
}
