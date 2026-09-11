// file: internal/database/sql_activity_migrating_maintenance_test.go
// version: 1.1.0
// guid: 2f8b6d41-9c3e-4a75-b1d8-7e0a5c2f9b64
// last-edited: 2026-09-11

package database

import (
	"context"
	"errors"
	"testing"
	"time"
)

// These tests pin the fix for the one-sided maintenance routing in
// MigratingActivityStore. Record writes to BOTH backends, so both accumulate
// every summarizable, prunable and recompactable row; until 2026-09-10 only
// CompactByDay had been corrected to run on both (PR #3214), and Summarize,
// Prune, RecompactDigests and RepairActivityIndexes still ran on the active
// backend alone. Each test below fails against that routing for the reason its
// name states.

// countUnpruned returns how many rows of the given tier a backend holds whose
// PrunedAt is unset — the originals Summarize is supposed to fold away, as
// opposed to the summary rows it writes in their place (PrunedAt set).
func countUnpruned(t *testing.T, s ActivityStorer, tier string) int {
	t.Helper()
	entries, _, err := s.Query(context.Background(), ActivityFilter{Tier: tier, Limit: 1000})
	if err != nil {
		t.Fatalf("query %s: %v", tier, err)
	}
	n := 0
	for _, e := range entries {
		if e.PrunedAt == nil {
			n++
		}
	}
	return n
}

// migratingTierFixture dual-writes `n` rows of `tier` on distinct UTC days in
// January 2026 through a MigratingActivityStore (Pebble primary, SQLite
// secondary, reads on primary) and returns the wrapper plus the two backends.
//
// The summary text is one deriveTypeFromMessage recognises: the recompact test
// below needs a legacy item that STOPS being legacy once re-derived, and an
// unrecognised message derives back to system_log, which would make every run
// touch it again and turn an idempotency assertion into a fixture artefact.
func migratingTierFixture(t *testing.T, tier, typ string, n int) (*MigratingActivityStore, *PebbleActivityStore, *SQLActivityStore) {
	t.Helper()
	pebbleStore := newTestPebbleActivityStore(t)
	sqlStore := newTestSQLStore(t)
	mig := NewMigratingActivityStore(pebbleStore, sqlStore, false)

	base := time.Date(2026, 1, 10, 9, 0, 0, 0, time.UTC)
	for i := range n {
		if _, err := mig.Record(ActivityEntry{
			Timestamp: base.AddDate(0, 0, i), Tier: tier, Type: typ,
			Level: "info", Source: "test", Summary: "applied metadata to book",
		}); err != nil {
			t.Fatalf("record row %d: %v", i, err)
		}
	}
	if got := countTier(t, pebbleStore, tier); got != n {
		t.Fatalf("fixture: pebble %s rows = %d, want %d", tier, got, n)
	}
	if got := countTier(t, sqlStore, tier); got != n {
		t.Fatalf("fixture: sqlite %s rows = %d, want %d", tier, got, n)
	}
	return mig, pebbleStore, sqlStore
}

var migratingMaintenanceCutoff = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

// TestMigratingActivityStore_SummarizeSummarizesBothBackends: with the old
// routing the secondary keeps every change row past the cutoff forever, and
// the returned count is the primary's alone.
func TestMigratingActivityStore_SummarizeSummarizesBothBackends(t *testing.T) {
	const rows = 3
	mig, pebbleStore, sqlStore := migratingTierFixture(t, "change", "metadata_changed", rows)

	n, err := mig.Summarize(context.Background(), migratingMaintenanceCutoff, "change")
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if n != 2*rows {
		t.Errorf("Summarize returned %d, want the two-backend sum %d", n, 2*rows)
	}
	if got := countUnpruned(t, pebbleStore, "change"); got != 0 {
		t.Errorf("pebble (primary) still holds %d unsummarized change rows", got)
	}
	if got := countUnpruned(t, sqlStore, "change"); got != 0 {
		t.Errorf("sqlite (secondary) still holds %d unsummarized change rows — Summarize ran on one backend only", got)
	}
	// Each backend wrote its own summary rows (one per day here), so neither
	// tier is empty: the originals were folded, not dropped.
	if got := countTier(t, pebbleStore, "change"); got != rows {
		t.Errorf("pebble (primary) summary rows = %d, want %d", got, rows)
	}
	if got := countTier(t, sqlStore, "change"); got != rows {
		t.Errorf("sqlite (secondary) summary rows = %d, want %d", got, rows)
	}
}

// TestMigratingActivityStore_PrunePrunesBothBackends: with the old routing the
// secondary keeps every debug row past the cutoff forever.
func TestMigratingActivityStore_PrunePrunesBothBackends(t *testing.T) {
	const rows = 3
	mig, pebbleStore, sqlStore := migratingTierFixture(t, "debug", "scan_progress", rows)

	n, err := mig.Prune(context.Background(), migratingMaintenanceCutoff, "debug")
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 2*rows {
		t.Errorf("Prune returned %d, want the two-backend sum %d", n, 2*rows)
	}
	if got := countTier(t, pebbleStore, "debug"); got != 0 {
		t.Errorf("pebble (primary) still holds %d debug rows", got)
	}
	if got := countTier(t, sqlStore, "debug"); got != 0 {
		t.Errorf("sqlite (secondary) still holds %d debug rows — Prune ran on one backend only", got)
	}
}

// TestMigratingActivityStore_RecompactDigestsTouchesBothBackends: both
// backends compact the same legacy rows into a digest each (CompactByDay fans
// out), so both digests carry legacy items. With the old routing only the
// active backend's digest is re-derived; the proof is that a direct run on the
// secondary afterwards still finds work to do.
func TestMigratingActivityStore_RecompactDigestsTouchesBothBackends(t *testing.T) {
	// Legacy-style rows: type system_log, no tags — what isLegacyItem matches.
	mig, pebbleStore, sqlStore := migratingTierFixture(t, "change", "system_log", 3)
	ctx := context.Background()

	if _, err := mig.CompactByDay(ctx, migratingMaintenanceCutoff); err != nil {
		t.Fatalf("CompactByDay: %v", err)
	}
	if got := countTier(t, pebbleStore, "digest"); got != 3 {
		t.Fatalf("fixture: pebble digests = %d, want 3", got)
	}
	if got := countTier(t, sqlStore, "digest"); got != 3 {
		t.Fatalf("fixture: sqlite digests = %d, want 3", got)
	}

	res, err := mig.RecompactDigests(ctx)
	if err != nil {
		t.Fatalf("RecompactDigests: %v", err)
	}
	if res.Touched != 6 || res.Skipped != 0 {
		t.Errorf("RecompactDigests = %+v, want Touched=6 (3 digests × 2 backends), Skipped=0", res)
	}

	// Nothing left to do on either backend individually.
	for name, s := range map[string]ActivityStorer{"pebble (primary)": pebbleStore, "sqlite (secondary)": sqlStore} {
		again, err := s.RecompactDigests(ctx)
		if err != nil {
			t.Fatalf("%s RecompactDigests: %v", name, err)
		}
		if again.Touched != 0 || again.Skipped != 3 {
			t.Errorf("%s after the wrapper ran: %+v, want Touched=0 Skipped=3 — the wrapper skipped this backend", name, again)
		}
	}
}

// TestMigratingActivityStore_RepairActivityIndexesRepairsInactivePebble: the
// SQLite implementation is a documented no-op, so once reads flip to SQLite
// the old routing never repairs Pebble's index leak again — the exact
// production state after the migration completes.
func TestMigratingActivityStore_RepairActivityIndexesRepairsInactivePebble(t *testing.T) {
	pebbleStore := newTestPebbleActivityStore(t)
	sqlStore := newTestSQLStore(t)
	seedOrphanedIndexEntries(t, pebbleStore, "debug", 6)
	if op, bk := countIndexKeys(t, pebbleStore); op != 6 || bk != 6 {
		t.Fatalf("fixture: pebble index keys = (%d, %d), want (6, 6)", op, bk)
	}

	// Reads already flipped to the secondary: the active backend is SQLite.
	mig := NewMigratingActivityStore(pebbleStore, sqlStore, true)

	res, err := mig.RepairActivityIndexes(context.Background())
	if err != nil {
		t.Fatalf("RepairActivityIndexes: %v", err)
	}
	if res.Deleted != 12 || res.Orphaned != 12 {
		t.Errorf("RepairActivityIndexes = %+v, want Orphaned=12 Deleted=12 (6 orphans × 2 index families)", res)
	}
	if op, bk := countIndexKeys(t, pebbleStore); op != 0 || bk != 0 {
		t.Errorf("pebble (inactive primary) still holds (%d, %d) orphaned index keys — repair ran on the active no-op backend only", op, bk)
	}
}

// TestMigratingActivityStore_SummarizeReportsLiveness pins the liveness wiring
// the nightly cleanup depends on now that Summarize runs on both backends:
// each store emits progress as it works and the wrapper emits one completion
// per backend, all through the WithMaintenanceProgress hook on ctx. Without
// these the first night over an unsummarized SQLite history is a silent
// stretch long enough for the watchdog to strike the op never_reported.
func TestMigratingActivityStore_SummarizeReportsLiveness(t *testing.T) {
	const rows = 3
	mig, _, _ := migratingTierFixture(t, "change", "metadata_changed", rows)

	progress := map[string]int{}
	done := map[string]MaintenanceProgressEvent{}
	ctx := WithMaintenanceProgress(context.Background(), func(ev MaintenanceProgressEvent) {
		if ev.Phase != MaintenancePhaseSummarize {
			t.Errorf("event phase = %q, want %q", ev.Phase, MaintenancePhaseSummarize)
		}
		if ev.Done {
			done[ev.Backend] = ev
		} else {
			progress[ev.Backend]++
		}
	})
	if _, err := mig.Summarize(ctx, migratingMaintenanceCutoff, "change"); err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	for _, name := range []string{"pebble", "sqlite"} {
		if progress[name] == 0 {
			t.Errorf("backend %q emitted no progress events while summarizing", name)
		}
		ev, ok := done[name]
		if !ok {
			t.Errorf("no completion event for backend %q", name)
			continue
		}
		if ev.Rows != rows || ev.Err != nil {
			t.Errorf("completion for %q = %+v, want Rows=%d Err=nil", name, ev, rows)
		}
	}
}

// failingSummarizeStore is an ActivityStorer whose Summarize always fails,
// used to prove a primary failure does not skip the secondary.
type failingSummarizeStore struct {
	ActivityStorer
}

func (f failingSummarizeStore) Summarize(context.Context, time.Time, string) (int, error) {
	return 0, errors.New("boom")
}

func TestMigratingActivityStore_SummarizePrimaryFailureStillSummarizesSecondary(t *testing.T) {
	_, pebbleStore, sqlStore := migratingTierFixture(t, "change", "metadata_changed", 2)
	mig := NewMigratingActivityStore(failingSummarizeStore{pebbleStore}, sqlStore, false)

	n, err := mig.Summarize(context.Background(), migratingMaintenanceCutoff, "change")
	if err == nil {
		t.Fatal("expected the primary's error to surface")
	}
	if got := countUnpruned(t, sqlStore, "change"); got != 0 {
		t.Errorf("sqlite (secondary) still holds %d unsummarized rows: a primary failure skipped it", got)
	}
	if n != 2 {
		t.Errorf("count should still carry the secondary's work: got %d", n)
	}
}

// TestMigratingActivityStore_SummarizeCanceledContextStopsBeforeSecondary: a
// canceled context is the one reason not to try the secondary — the work was
// asked to stop, not to fail over.
func TestMigratingActivityStore_SummarizeCanceledContextStopsBeforeSecondary(t *testing.T) {
	_, pebbleStore, sqlStore := migratingTierFixture(t, "change", "metadata_changed", 2)
	mig := NewMigratingActivityStore(failingSummarizeStore{pebbleStore}, sqlStore, false)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := mig.Summarize(ctx, migratingMaintenanceCutoff, "change")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if got := countUnpruned(t, sqlStore, "change"); got != 2 {
		t.Errorf("sqlite (secondary) was touched (%d unsummarized rows remain, want 2) after the context was canceled", got)
	}
}
