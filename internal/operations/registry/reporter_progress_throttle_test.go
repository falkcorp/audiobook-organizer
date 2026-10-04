// file: internal/operations/registry/reporter_progress_throttle_test.go
// version: 1.0.0
// guid: a9bbcd46-c886-4d2e-b906-2b7d1c0db643
// last-edited: 2026-10-04

package registry_test

import (
	"context"
	"fmt"
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
	assertRows(t, got, []string{"Books 1/10", "Authors 1/5", "Done"})
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
