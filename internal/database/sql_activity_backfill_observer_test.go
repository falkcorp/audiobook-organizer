// file: internal/database/sql_activity_backfill_observer_test.go
// version: 1.0.0
// guid: 2e6b41d7-58ac-4f39-9c0e-7a15b3d8f0c4
// last-edited: 2026-09-08

// Tests for the backfill's progress observer — the seam that lets the migration
// be shown to users while it runs, without re-deriving anything or re-reading
// Pebble.

package database

import (
	"context"
	"testing"
)

func TestBackfillWithProgress_ReportsEveryTierAndItsVerdict(t *testing.T) {
	pebbleStore := newTestPebbleActivityStore(t)
	sqlStore := newTestSQLStore(t)
	seedTiers(t, pebbleStore, map[string]int{"change": 3, "audit": 2})

	var got []ActivityBackfillProgressUpdate
	res, err := BackfillPebbleActivityToSQLWithProgress(
		context.Background(), pebbleStore, sqlStore, false,
		func(u ActivityBackfillProgressUpdate) { got = append(got, u) },
	)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if !res.ParityOK {
		t.Fatalf("parity failed; cannot assess progress reporting")
	}
	if len(got) == 0 {
		t.Fatal("the observer was never called — the migration would show no status at all")
	}

	// Every tier must be reported, including the empty ones: a surface that only
	// names tiers with rows in them would silently skip 5 of production's 7 and
	// look stuck on whichever one it last mentioned.
	seenStart := map[string]bool{}
	doneVerdict := map[string]string{}
	for _, u := range got {
		if u.TiersTotal != len(actTiers) {
			t.Errorf("update for %q reported TiersTotal=%d, want %d", u.Tier, u.TiersTotal, len(actTiers))
		}
		if u.TierIndex < 1 || u.TierIndex > len(actTiers) {
			t.Errorf("update for %q has out-of-range TierIndex %d", u.Tier, u.TierIndex)
		}
		seenStart[u.Tier] = true
		if u.Done {
			doneVerdict[u.Tier] = u.Verdict
		}
	}
	for _, tier := range actTiers {
		if !seenStart[tier] {
			t.Errorf("tier %q was never reported", tier)
		}
		if doneVerdict[tier] == "" {
			t.Errorf("tier %q never reported a completion verdict", tier)
		}
	}

	// The reported counts must agree with the run's own result, or the status
	// surface is telling users a different story from the migration's.
	for _, u := range got {
		if !u.Done {
			continue
		}
		if want := res.PerTierScanned[u.Tier]; u.Scanned != want {
			t.Errorf("tier %q final scanned = %d, result says %d", u.Tier, u.Scanned, want)
		}
		if want := res.PerTierCopied[u.Tier]; u.Copied != want {
			t.Errorf("tier %q final copied = %d, result says %d", u.Tier, u.Copied, want)
		}
	}
}

// A nil observer is the path every existing caller takes, so it must stay a
// no-op rather than a nil dereference.
func TestBackfillWithProgress_NilObserverIsSafe(t *testing.T) {
	pebbleStore := newTestPebbleActivityStore(t)
	sqlStore := newTestSQLStore(t)
	seedTiers(t, pebbleStore, map[string]int{"change": 2})

	res, err := BackfillPebbleActivityToSQLWithProgress(
		context.Background(), pebbleStore, sqlStore, false, nil)
	if err != nil {
		t.Fatalf("backfill with nil observer: %v", err)
	}
	if !res.ParityOK {
		t.Error("parity failed with a nil observer")
	}
}
