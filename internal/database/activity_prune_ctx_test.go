// file: internal/database/activity_prune_ctx_test.go
// version: 1.0.2
// guid: 91ca4695-f1b7-4b53-8d7e-8e460d059f2e
// last-edited: 2026-10-19

package database

import (
	"context"
	"errors"
	"testing"
	"time"
)

// These tests pin the ActivityRetention.Prune context contract: a cancelled
// ctx stops the prune at its next batch, the rows already committed are
// returned alongside context.Canceled, the rows not yet reached are left in
// place. Before 2026-09-11 Prune took no context at all, so a cancelled
// nightly cleanup pruned to completion regardless.

// pruneCancelHook returns a WithMaintenanceProgress hook that cancels the
// prune the first time backend reports a non-zero running count — that is,
// right after its first committed batch — and records every event it saw.
// This is the only deterministic way to cancel "mid-prune": the hook runs
// synchronously inside the store's batch loop, so the cancel lands between two
// batches, never before the first or after the last.
func pruneCancelHook(cancel context.CancelFunc, backend string) (MaintenanceProgress, *[]MaintenanceProgressEvent) {
	seen := &[]MaintenanceProgressEvent{}
	fired := false
	return func(ev MaintenanceProgressEvent) {
		*seen = append(*seen, ev)
		if !fired && ev.Phase == MaintenancePhasePrune && ev.Backend == backend && ev.Rows > 0 {
			fired = true
			cancel()
		}
	}, seen
}

// batchRecorder is the slice of the store surface seedPruneRows needs.
type batchRecorder interface {
	RecordBatch([]ActivityEntry) (int, error)
}

// seedPruneRows writes n rows of tier, one per hour from 2026-01-10 onwards
// (so they span several UTC days), all older than pruneCancelCutoff.
func seedPruneRows(t *testing.T, s batchRecorder, tier string, n int) {
	t.Helper()
	base := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	entries := make([]ActivityEntry, 0, n)
	for i := range n {
		entries = append(entries, ActivityEntry{
			Timestamp: base.Add(time.Duration(i) * time.Hour), Tier: tier, Type: "scan_progress",
			Level: "debug", Source: "test", Summary: "old debug row",
		})
	}
	written, err := s.RecordBatch(entries)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if written != n {
		t.Fatalf("seed wrote %d rows, want %d", written, n)
	}
}

var pruneCancelCutoff = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

// countExact returns the store's exact row count for tier (Query's total is a
// pagination probe and must not size an assertion).
func countExact(t *testing.T, s ActivityCounter, tier string) int {
	t.Helper()
	n, err := s.CountActivity(context.Background(), tier, nil)
	if err != nil {
		t.Fatalf("count %s: %v", tier, err)
	}
	return n
}

// assertPrunePartial checks the shared contract: err is context.Canceled,
// deleted is a real partial count, and exactly rows-deleted survive.
func assertPrunePartial(t *testing.T, backend string, s ActivityCounter, rows, deleted int, err error) {
	t.Helper()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("%s: Prune err = %v, want context.Canceled", backend, err)
	}
	if deleted <= 0 || deleted >= rows {
		t.Fatalf("%s: deleted = %d, want strictly between 0 and %d (cancel must land mid-run)", backend, deleted, rows)
	}
	if got := countExact(t, s, "debug"); got != rows-deleted {
		t.Errorf("%s: %d rows remain, want %d (rows - deleted): the returned count is not what was committed", backend, got, rows-deleted)
	}
}

// TestPebbleActivityStore_PruneStopsOnCancel: three 500-row batches; the hook
// cancels after the first commit, so the second batch must never run.
func TestPebbleActivityStore_PruneStopsOnCancel(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	const rows = 1200
	seedPruneRows(t, s, "debug", rows)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hook, seen := pruneCancelHook(cancel, "pebble")
	deleted, err := s.Prune(WithMaintenanceProgress(ctx, hook), pruneCancelCutoff, "debug")
	assertPrunePartial(t, "pebble", s, rows, deleted, err)
	if len(*seen) == 0 {
		t.Errorf("pebble: Prune reported no progress events")
	}
}
