// file: internal/operations/registry/reporter_progress_throttle_test.go
// version: 1.1.0
// guid: a9bbcd46-c886-4d2e-b906-2b7d1c0db643
// last-edited: 2026-10-04

package registry_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// progressRows returns the messages of the progress log rows, in order.
func progressRows(store *fakeStore, opID string) []string {
	var out []string
	for _, l := range store.logsFor(opID) {
		if strings.Contains(l.Attrs, `"phase":"progress"`) {
			out = append(out, l.Message)
		}
	}
	return out
}

// waitProgressRows polls until at least want progress rows exist or 2 s pass,
// then settles briefly so an over-emission would also be seen.
func waitProgressRows(t *testing.T, store *fakeStore, opID string, want int) []string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var rows []string
	for time.Now().Before(deadline) {
		rows = progressRows(store, opID)
		if len(rows) >= want {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	return progressRows(store, opID)
}

func assertRows(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("progress rows = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("progress rows = %q, want %q", got, want)
		}
	}
}

func TestProgressThrottle_CounterStreamOneLinePlusTerminal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rep, store, opID := newTestReporter(t, ctx)
	t0 := time.Now()
	registry.SetReporterClockForTest(rep, func() time.Time { return t0 })
	for i := 1; i <= 100000; i++ {
		_ = rep.UpdateProgress(i, 76994, fmt.Sprintf("Books %d/76994", i))
	}
	cancel()
	got := waitProgressRows(t, store, opID, 2)
	assertRows(t, got, []string{"Books 1/76994", "Books 100000/76994"})
}

func TestProgressThrottle_SlowStreamOneLinePer30s(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rep, store, opID := newTestReporter(t, ctx)
	t0 := time.Now()
	var i int
	registry.SetReporterClockForTest(rep, func() time.Time { return t0.Add(time.Duration(i-1) * time.Second) })
	for i = 1; i <= 95; i++ {
		_ = rep.UpdateProgress(i, 95, fmt.Sprintf("Books %d/95", i))
	}
	cancel()
	got := waitProgressRows(t, store, opID, 5)
	assertRows(t, got, []string{"Books 1/95", "Books 31/95", "Books 61/95", "Books 91/95", "Books 95/95"})
}

func TestProgressThrottle_ShapeChangeLogsImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rep, store, opID := newTestReporter(t, ctx)
	t0 := time.Now()
	registry.SetReporterClockForTest(rep, func() time.Time { return t0 })
	for _, m := range []string{"Books 1/10", "Books 2/10", "Authors 1/5", "Authors 2/5", "Done"} {
		_ = rep.UpdateProgress(1, 10, m)
	}
	cancel()
	got := waitProgressRows(t, store, opID, 3)
	// A shape change first writes the line the old phase was last on, so each
	// phase's final tally is kept: "Books 2/10" and "Authors 2/5" are logged
	// ahead of the line that ends their phase. Nothing is pending after "Done".
	assertRows(t, got, []string{"Books 1/10", "Books 2/10", "Authors 1/5", "Authors 2/5", "Done"})
}

func TestProgressThrottle_IdenticalMessageNeverRepeated(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rep, store, opID := newTestReporter(t, ctx)
	now := time.Now()
	registry.SetReporterClockForTest(rep, func() time.Time { return now })
	for range 3 {
		_ = rep.UpdateProgress(1, 10, "Scanning")
		now = now.Add(31 * time.Second)
	}
	cancel()
	got := waitProgressRows(t, store, opID, 1)
	assertRows(t, got, []string{"Scanning"})
}

func TestProgressThrottle_ProgressColumnsStillEveryUpdate(t *testing.T) {
	ctx := t.Context()
	rep, store, opID := newTestReporter(t, ctx)
	t0 := time.Now()
	registry.SetReporterClockForTest(rep, func() time.Time { return t0 })
	for i := 1; i <= 1000; i++ {
		_ = rep.UpdateProgress(i, 1000, fmt.Sprintf("Books %d/1000", i))
	}
	cur, _, _ := store.progressOf(opID)
	if cur != 1000 {
		t.Fatalf("progress current = %d, want 1000", cur)
	}
}

// TestProgressThrottle_ScannerMessagesWithBasenameAreThrottled feeds the
// message shapes library.scan really produces: a counter plus a per-book
// basename, so every message is a new digit-normalised shape. The shape rule
// alone logged every one of them (~300K rows per scan, eval R6).
func TestProgressThrottle_ScannerMessagesWithBasenameAreThrottled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rep, store, opID := newTestReporter(t, ctx)
	t0 := time.Now()
	var i int
	// 10 calls per second of fake time: 20,000 calls span 2,000 s.
	registry.SetReporterClockForTest(rep, func() time.Time { return t0.Add(time.Duration(i) * 100 * time.Millisecond) })
	const total = 76994
	for i = 1; i <= 20000; i++ {
		var msg string
		switch i % 4 {
		case 0:
			msg = fmt.Sprintf("Processed: %d/%d books (The Hobbit - Part %c)", i, total, 'A'+rune(i%26))
		case 1:
			msg = fmt.Sprintf("Scanning folder %d/%d: /books/library/Author %d/Title %c", i, total, i, 'a'+rune(i%26))
		case 2:
			msg = fmt.Sprintf("Reading tags: %d files (Book Number %d)", i, i*7)
		default:
			msg = fmt.Sprintf("Updating %d/%d (%d%%) 01J%020d", i, total, i%100, i)
		}
		_ = rep.UpdateProgress(i, total, msg)
	}
	cancel()
	got := waitProgressRows(t, store, opID, 2)
	// 2,000 s of fake time: at most one time-based line per 30 s window
	// (67) plus the capped shape-change lines and their pending partners.
	windows := 2000/30 + 1
	limit := windows * (1 + 2*3)
	if len(got) < 2 || len(got) > limit {
		t.Fatalf("scanner stream wrote %d progress rows from 20,000 updates, want 2..%d", len(got), limit)
	}
	t.Logf("scanner stream: %d progress rows from 20000 updates", len(got))
	if len(got) > 20000/20 {
		t.Fatalf("throttle too weak: %d rows", len(got))
	}
}

// TestProgressThrottle_PendingLineBeforeOperationFinished runs a real op
// through the registry worker and checks the final progress line is written
// before the "operation finished" line.
func TestProgressThrottle_PendingLineBeforeOperationFinished(t *testing.T) {
	store := newFakeStore()
	r := registry.NewWithOptions(store, slog.Default(), 2, registry.Options{})
	r.Start(context.Background())
	def := makeValidDef("test.pending-before-finished")
	def.Run = func(_ context.Context, _ json.RawMessage, rep registry.Reporter) error {
		_ = rep.UpdateProgress(1, 9, "Books 1/9")
		_ = rep.UpdateProgress(2, 9, "Books 2/9") // same shape, inside 30 s: held back
		return nil
	}
	if err := r.RegisterOp(def); err != nil {
		t.Fatal(err)
	}
	opID, err := r.EnqueueOp(context.Background(), def.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var pendingAt, finishedAt int
	for time.Now().Before(deadline) {
		pendingAt, finishedAt = -1, -1
		for n, l := range store.logsFor(opID) {
			switch {
			case l.Message == "Books 2/9":
				pendingAt = n
			case l.Message == "operation finished":
				finishedAt = n
			}
		}
		if finishedAt >= 0 && pendingAt >= 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pendingAt < 0 || finishedAt < 0 {
		t.Fatalf("missing lines: pending at %d, finished at %d", pendingAt, finishedAt)
	}
	if pendingAt > finishedAt {
		t.Fatalf("final progress line (index %d) was written after \"operation finished\" (index %d)", pendingAt, finishedAt)
	}
}
