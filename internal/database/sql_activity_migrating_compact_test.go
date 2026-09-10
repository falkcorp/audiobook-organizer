// file: internal/database/sql_activity_migrating_compact_test.go
// version: 1.0.0
// guid: 9b2d4f61-8e3a-4c07-b5d9-1f7a3c8e6b24
// last-edited: 2026-09-10

package database

import (
	"context"
	"errors"
	"testing"
	"time"
)

// migratingCompactFixture dual-writes `days` old change-tier rows (one per
// UTC day, each well before the cutoff) through a MigratingActivityStore over
// a real Pebble primary and a real SQLite secondary, and returns the wrapper
// plus the two backends.
func migratingCompactFixture(t *testing.T, days int) (*MigratingActivityStore, *PebbleActivityStore, *SQLActivityStore) {
	t.Helper()
	pebbleStore := newTestPebbleActivityStore(t)
	sqlStore := newTestSQLStore(t)
	mig := NewMigratingActivityStore(pebbleStore, sqlStore, false)

	base := time.Date(2026, 1, 10, 9, 0, 0, 0, time.UTC)
	for i := range days {
		if _, err := mig.Record(ActivityEntry{
			Timestamp: base.AddDate(0, 0, i), Tier: "change", Type: "metadata_changed",
			Level: "info", Source: "test", Summary: "old change",
		}); err != nil {
			t.Fatalf("record day %d: %v", i, err)
		}
	}
	return mig, pebbleStore, sqlStore
}

// countTier returns how many rows of the given tier a backend holds.
func countTier(t *testing.T, s ActivityStorer, tier string) int {
	t.Helper()
	entries, _, err := s.Query(context.Background(), ActivityFilter{Tier: tier, Limit: 1000})
	if err != nil {
		t.Fatalf("query %s: %v", tier, err)
	}
	return len(entries)
}

// TestMigratingActivityStore_CompactByDayCompactsBothBackends pins the fix for
// the one-sided compaction: with dual-write on, CompactByDay used to run on the
// active backend only, so the other backend — which receives every write —
// kept every compactable row forever. Both backends must end with zero change
// rows and a digest per day, and the returned counters must be the SUM of the
// two, not one backend's numbers.
func TestMigratingActivityStore_CompactByDayCompactsBothBackends(t *testing.T) {
	const days = 3
	mig, pebbleStore, sqlStore := migratingCompactFixture(t, days)

	if got := countTier(t, pebbleStore, "change"); got != days {
		t.Fatalf("fixture: pebble change rows = %d, want %d", got, days)
	}
	if got := countTier(t, sqlStore, "change"); got != days {
		t.Fatalf("fixture: sqlite change rows = %d, want %d", got, days)
	}

	var done []CompactProgressEvent
	ctx := WithCompactProgress(context.Background(), func(ev CompactProgressEvent) {
		if ev.Done {
			done = append(done, ev)
		}
	})
	res, err := mig.CompactByDay(ctx, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("CompactByDay: %v", err)
	}

	// Each backend individually: no change rows left, one digest per day.
	if got := countTier(t, pebbleStore, "change"); got != 0 {
		t.Errorf("pebble (primary) still holds %d change rows after compaction", got)
	}
	if got := countTier(t, pebbleStore, "digest"); got != days {
		t.Errorf("pebble (primary) digests = %d, want %d", got, days)
	}
	if got := countTier(t, sqlStore, "change"); got != 0 {
		t.Errorf("sqlite (secondary) still holds %d change rows after compaction — compaction ran on one backend only", got)
	}
	if got := countTier(t, sqlStore, "digest"); got != days {
		t.Errorf("sqlite (secondary) digests = %d, want %d", got, days)
	}

	// The totals are the sum across both backends.
	want := CompactResult{DaysCompacted: 2 * days, EntriesDeleted: 2 * days}
	if res != want {
		t.Errorf("CompactByDay result = %+v, want the two-backend sum %+v", res, want)
	}

	// One completion event per backend, labelled, carrying that backend's own
	// counters — this is what the maintenance op logs per backend.
	if len(done) != 2 {
		t.Fatalf("completion events = %d (%+v), want 2", len(done), done)
	}
	seen := map[string]CompactResult{}
	for _, ev := range done {
		if ev.Err != nil {
			t.Errorf("backend %q reported error: %v", ev.Backend, ev.Err)
		}
		seen[ev.Backend] = ev.Result
	}
	each := CompactResult{DaysCompacted: days, EntriesDeleted: days}
	for _, name := range []string{"pebble", "sqlite"} {
		if got, ok := seen[name]; !ok || got != each {
			t.Errorf("completion event for %q = %+v (present=%t), want %+v", name, got, ok, each)
		}
	}
}

// TestMigratingActivityStore_CompactByDayReportsProgress pins that the
// per-chunk/per-day progress events the stores emit reach a hook attached to
// the context handed to the wrapper. The maintenance op forwards these to
// reporter.UpdateProgress, which is the only thing that keeps the registry
// watchdog from cancelling a long compaction.
func TestMigratingActivityStore_CompactByDayReportsProgress(t *testing.T) {
	mig, _, _ := migratingCompactFixture(t, 2)

	perBackend := map[string]int{}
	ctx := WithCompactProgress(context.Background(), func(ev CompactProgressEvent) {
		if !ev.Done {
			perBackend[ev.Backend]++
		}
	})
	if _, err := mig.CompactByDay(ctx, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("CompactByDay: %v", err)
	}
	for _, name := range []string{"pebble", "sqlite"} {
		if perBackend[name] == 0 {
			t.Errorf("backend %q emitted no progress events; the op watchdog would see a silent op", name)
		}
	}
}

// failingCompactStore is an ActivityStorer whose CompactByDay always fails,
// used to prove a primary failure does not skip the secondary.
type failingCompactStore struct {
	ActivityStorer
}

func (f failingCompactStore) CompactByDay(context.Context, time.Time) (CompactResult, error) {
	return CompactResult{}, errors.New("boom")
}

func TestMigratingActivityStore_CompactByDayPrimaryFailureStillCompactsSecondary(t *testing.T) {
	_, pebbleStore, sqlStore := migratingCompactFixture(t, 2)
	mig := NewMigratingActivityStore(failingCompactStore{pebbleStore}, sqlStore, false)

	res, err := mig.CompactByDay(context.Background(), time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	if err == nil {
		t.Fatal("expected the primary's error to surface")
	}
	if got := countTier(t, sqlStore, "change"); got != 0 {
		t.Errorf("sqlite (secondary) still holds %d change rows: a primary failure skipped it", got)
	}
	if res.DaysCompacted != 2 {
		t.Errorf("result should still carry the secondary's work: %+v", res)
	}
}
