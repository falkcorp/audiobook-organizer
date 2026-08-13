// file: internal/errhandling/skipcounter_test.go
// version: 1.0.0
// guid: 0910fd4e-59de-4348-9e4b-e02e31fcff50
// last-edited: 2026-08-11

package errhandling

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestSkipCounter_CountsProcessedAndSkipped(t *testing.T) {
	c := NewSkipCounter("auto-organize")

	c.Processed()
	c.Processed()
	c.Skip("store lookup failed", errors.New("pebble io"))
	c.Skip("store lookup failed", errors.New("pebble io again"))
	c.Skip("book not in store", nil)

	if got := c.ProcessedCount(); got != 2 {
		t.Errorf("ProcessedCount = %d, want 2", got)
	}
	if got := c.Skipped(); got != 3 {
		t.Errorf("Skipped = %d, want 3", got)
	}
	by := c.ByReason()
	if by["store lookup failed"] != 2 {
		t.Errorf("byReason[store lookup failed] = %d, want 2", by["store lookup failed"])
	}
	if by["book not in store"] != 1 {
		t.Errorf("byReason[book not in store] = %d, want 1", by["book not in store"])
	}
}

func TestSkipCounter_SummaryLogCarriesTheRealNumbers(t *testing.T) {
	// The point of the type: the summary must state the skipped count, so
	// "processed 2" can never again be mistaken for "there were only 2".
	records := captureLogs(t)

	c := NewSkipCounter("auto-organize")
	c.Processed()
	c.Processed()
	c.Skip("store lookup failed", errors.New("pebble io"))
	c.Skip("book not in store", nil)
	c.LogSummary(context.Background())

	got := records()
	if len(got) != 1 {
		t.Fatalf("got %d records, want exactly 1 summary line: %v", len(got), got)
	}
	rec := got[0]

	if rec["level"] != "WARN" {
		t.Errorf("level = %v, want WARN when items were skipped", rec["level"])
	}
	if rec["loop"] != "auto-organize" {
		t.Errorf("loop = %v, want auto-organize", rec["loop"])
	}
	if rec["processed"] != float64(2) {
		t.Errorf("processed = %v, want 2", rec["processed"])
	}
	if rec["skipped"] != float64(2) {
		t.Errorf("skipped = %v, want 2", rec["skipped"])
	}
	if rec["total"] != float64(4) {
		t.Errorf("total = %v, want 4", rec["total"])
	}
	if rec["skipped.store lookup failed"] != float64(1) {
		t.Errorf("per-reason count = %v, want 1", rec["skipped.store lookup failed"])
	}
	// One representative error per reason, so the skip is diagnosable.
	if rec["sample.store lookup failed"] != "pebble io" {
		t.Errorf("sample = %v, want %q", rec["sample.store lookup failed"], "pebble io")
	}
}

func TestSkipCounter_CleanRunLogsAtInfo(t *testing.T) {
	records := captureLogs(t)

	c := NewSkipCounter("clean-loop")
	c.Add(10)
	c.LogSummary(context.Background())

	got := records()
	if len(got) != 1 {
		t.Fatalf("got %d records, want 1", len(got))
	}
	if got[0]["level"] != "INFO" {
		t.Errorf("level = %v, want INFO when nothing was skipped", got[0]["level"])
	}
	if got[0]["skipped"] != float64(0) {
		t.Errorf("skipped = %v, want 0", got[0]["skipped"])
	}
	if got[0]["processed"] != float64(10) {
		t.Errorf("processed = %v, want 10", got[0]["processed"])
	}
}

func TestSkipCounter_EmitsExactlyOneLineNotOnePerSkip(t *testing.T) {
	// A per-iteration log would reproduce the problem in a different shape:
	// 300 lines is as unreadable as 0.
	records := captureLogs(t)

	c := NewSkipCounter("noisy")
	for i := 0; i < 300; i++ {
		c.Skip("bad row", errors.New("decode"))
	}
	c.LogSummary(context.Background())

	got := records()
	if len(got) != 1 {
		t.Fatalf("got %d records, want exactly 1", len(got))
	}
	if got[0]["skipped"] != float64(300) {
		t.Errorf("skipped = %v, want 300", got[0]["skipped"])
	}
}

func TestSkipCounter_ConcurrentUseIsSafe(t *testing.T) {
	// CLAUDE.md mandates bounded worker pools for library-scale loops, so the
	// counter must be shareable across workers without extra locking.
	c := NewSkipCounter("parallel")

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				c.Processed()
			} else {
				c.Skip("odd", errors.New("odd item"))
			}
		}(i)
	}
	wg.Wait()

	if got := c.ProcessedCount(); got != 50 {
		t.Errorf("ProcessedCount = %d, want 50", got)
	}
	if got := c.Skipped(); got != 50 {
		t.Errorf("Skipped = %d, want 50", got)
	}
}

func TestSkipCounter_NilReceiverIsSafe(t *testing.T) {
	// Callers may hold an optional counter; a nil one must not panic mid-loop.
	var c *SkipCounter
	c.Skip("reason", errors.New("x"))
	c.Processed()
	c.Add(3)
	c.LogSummary(context.Background())
	if got := c.Skipped(); got != 0 {
		t.Errorf("Skipped on nil = %d, want 0", got)
	}
	if got := c.ProcessedCount(); got != 0 {
		t.Errorf("ProcessedCount on nil = %d, want 0", got)
	}
}

func TestSkipCounter_ByReasonIsACopy(t *testing.T) {
	c := NewSkipCounter("copy")
	c.Skip("r", nil)

	m := c.ByReason()
	m["r"] = 999

	if c.ByReason()["r"] != 1 {
		t.Error("ByReason returned a live map; mutating it corrupted the counter")
	}
}
