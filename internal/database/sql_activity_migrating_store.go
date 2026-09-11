// file: internal/database/sql_activity_migrating_store.go
// version: 1.6.0
// guid: 4a1d8c62-7e59-4b03-9c8f-6d2e1a0b7f35
// last-edited: 2026-09-11

// Package database — backend-migration wrapper for the activity log.
//
// MigratingActivityStore drives a live, no-downtime cutover from one
// ActivityStorer (primary, e.g. Pebble) to another (secondary, e.g. SQLite),
// the same shape as the Nuts→Pebble DualWriteActivityStore but generalized and
// race-safe. It is distinct from that wrapper on purpose: DualWriteActivityStore
// hardcodes nuts/pebble semantics and is retained as a rollback reference for
// that earlier migration; conflating the two migrations in one type would tangle
// their cutover state.
//
// ROUTING (deliberately NOT symmetric):
//
//   - Writes (Record): BOTH backends, always. New rows land in the secondary
//     from the first boot, so the backfill only has to copy history and the two
//     stores stay in sync — which also keeps the primary a valid rollback mirror
//     after the read flip.
//
//   - Reads (Query, GetDistinctSources): the ACTIVE backend (primary until the
//     flip, secondary after).
//
//   - Maintenance that rewrites/reclaims storage (CompactByDay, Summarize,
//     Prune, RecompactDigests, RepairActivityIndexes): BOTH, always. Every one
//     of these was active-only until 2026-09-10, on the reasoning that after
//     the flip maintenance should run SQLite's bounded paths and never
//     Pebble's unbounded, timeout-prone ones. That reasoning held only while
//     the work ran inside an HTTP request; it now runs as background ops, so
//     Pebble's duration is a progress line, not a browser timeout. And the
//     inactive backend is still taking every write (Record goes to both,
//     always), so maintaining only the active one left the rollback mirror
//     growing unbounded: every debug row past 30 days, every change row past
//     90 days, every legacy digest item, and — once reads flip to SQLite,
//     whose RepairActivityIndexes is a documented no-op — every orphaned Pebble
//     index entry, forever. CompactByDay was corrected first (PR #3214); the
//     other four followed the same day with the same shape (see
//     runOnBothBackends). MigrateSystemActivityLogs is the one exception: a
//     one-time migration, a documented no-op on Pebble, active only.
//
//     Each backend does its own work independently, so a summary or digest
//     row is written by each store with its own timestamp/ULID. If the
//     activity backfill op is re-run later it copies Pebble's rows into SQLite
//     by content key, so SQLite may then hold its own summary plus Pebble's
//     for the same group — one extra pruned_at-marked line per group per
//     backfill, not lost data, and the same shape digests have had since
//     #3214.
//
//   - OptimizeStatistics: BOTH. It refreshes query-planner statistics, which is
//     read-only bookkeeping rather than a storage rewrite, and the INACTIVE
//     backend is still taking every write — so leaving its planner un-analyzed
//     would make a rollback land on a backend that has never been analyzed.
//     That is exactly the state production's SQLite database was found in on
//     2026-09-09 (no sqlite_stat1 at all across 13.2M rows).
//
//   - WipeAllActivity: BOTH. It is an explicit destructive user action; leaving a
//     full mirror behind would be surprising, so it clears both and returns the
//     active backend's count.
//
// THE FLIP is a single atomic.Bool the backfill op sets once history has been
// copied AND per-tier parity verified. Flipping is race-safe against concurrent
// Query/Record — the flag is the only shared mutable state and it is atomic.
package database

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"
)

// MigratingActivityStore wraps a primary and secondary ActivityStorer and routes
// per the table in the package doc, flipping reads/maintenance from primary to
// secondary atomically when the migration completes.
type MigratingActivityStore struct {
	primary   ActivityStorer
	secondary ActivityStorer
	// readSecondary=false ⇒ active is primary; true ⇒ active is secondary.
	readSecondary atomic.Bool
}

// NewMigratingActivityStore builds the wrapper. readSecondary seeds the initial
// active backend (typically false at boot, then flipped true by the backfill op
// once parity is verified — or true immediately if the migration already
// completed on a prior run).
//
// There is no history/live cutoff: dual-writes and the backfill share a
// deterministic content key (see activitySrcKey), so the backfill copies ALL of
// Pebble idempotently and can never duplicate an event the dual-write stored.
func NewMigratingActivityStore(primary, secondary ActivityStorer, readSecondary bool) *MigratingActivityStore {
	m := &MigratingActivityStore{primary: primary, secondary: secondary}
	m.readSecondary.Store(readSecondary)
	return m
}

// SetReadSecondary performs the live read/maintenance flip. Safe to call
// concurrently with reads and writes.
func (m *MigratingActivityStore) SetReadSecondary(v bool) {
	prev := m.readSecondary.Swap(v)
	if prev != v {
		slog.Info("[activity] migration read flip", "read_secondary", v)
	}
}

// ReadSecondary reports the current active backend (true ⇒ secondary).
func (m *MigratingActivityStore) ReadSecondary() bool { return m.readSecondary.Load() }

// Secondary exposes the secondary backend so the backfill op can target it
// directly (it copies primary→secondary).
func (m *MigratingActivityStore) Secondary() ActivityStorer { return m.secondary }

// Primary exposes the primary backend (source of the backfill copy).
func (m *MigratingActivityStore) Primary() ActivityStorer { return m.primary }

func (m *MigratingActivityStore) active() ActivityStorer {
	if m.readSecondary.Load() {
		return m.secondary
	}
	return m.primary
}

// ── writes: both ────────────────────────────────────────────────────────────

// Record writes to both backends. The active backend's (id, err) is
// authoritative; a secondary-only failure is logged, not returned.
//
// It normalizes the entry ONCE here, before fan-out, so both backends store
// identical field values (a zero timestamp resolved independently in each store
// would otherwise resolve to different instants). Identical field values are
// what let the backfill's content key match the live dual-write's — the whole
// basis of key-free idempotency. A normalization error (a pre-epoch timestamp)
// is attributable to the entry and rejects the write from both backends, exactly
// as each backend's own normalization would.
func (m *MigratingActivityStore) Record(e ActivityEntry) (int64, error) {
	e, nerr := normalizeActivityEntry(e)
	if nerr != nil {
		return 0, nerr
	}
	pID, pErr := m.primary.Record(e)
	sID, sErr := m.secondary.Record(e)
	if pErr != nil {
		slog.Warn("[activity-migrate] primary Record failed", "err", pErr)
	}
	if sErr != nil {
		slog.Warn("[activity-migrate] secondary Record failed", "err", sErr)
	}
	if m.readSecondary.Load() {
		return sID, sErr
	}
	return pID, pErr
}

// WipeAllActivity clears both backends and returns the active one's count/err.
func (m *MigratingActivityStore) WipeAllActivity(ctx context.Context) (int64, error) {
	pN, pErr := m.primary.WipeAllActivity(ctx)
	sN, sErr := m.secondary.WipeAllActivity(ctx)
	if pErr != nil {
		slog.Warn("[activity-migrate] primary WipeAllActivity failed", "err", pErr)
	}
	if sErr != nil {
		slog.Warn("[activity-migrate] secondary WipeAllActivity failed", "err", sErr)
	}
	if m.readSecondary.Load() {
		return sN, sErr
	}
	return pN, pErr
}

// ── reads: active only ──────────────────────────────────────────────────────

func (m *MigratingActivityStore) Query(ctx context.Context, f ActivityFilter) ([]ActivityEntry, int, error) {
	return m.active().Query(ctx, f)
}

func (m *MigratingActivityStore) GetDistinctSources(ctx context.Context, f ActivityFilter) ([]SourceCount, error) {
	return m.active().GetDistinctSources(ctx, f)
}

// ── maintenance: both backends ──────────────────────────────────────────────

// runOnBothBackends is the one shape every storage-rewriting maintenance
// method on this wrapper takes. It runs op against the primary, then the
// secondary, and folds the two outcomes with fold.
//
// Both backends are always attempted: a primary failure does not skip the
// secondary, because the two are independent stores and skipping one silently
// is how a "maintenance ran" log line ends up describing half the data. A
// canceled context is the ONE reason not to try the secondary — the work was
// asked to stop, not merely to fail over — and ctx.Err() is returned in that
// case even if the primary itself failed for another reason, because the
// caller asked to stop and that is the fact it is waiting on. A method whose
// interface has no context (Prune) passes context.Background(), so both
// backends always run.
//
// The returned error is the first one encountered (primary before secondary)
// and wraps the other when both fail, so neither is lost. Every backend's
// result is handed to fold whether or not it failed, so counters reflect
// what each backend actually did before failing.
//
// onDone, when non-nil, receives each backend's name, result and error as it
// finishes — CompactByDay uses it to feed the WithCompactProgress hook.
//
// It is a function taking m rather than a method on *MigratingActivityStore
// because Go does not allow type parameters on methods.
func runOnBothBackends[R any](
	m *MigratingActivityStore,
	ctx context.Context,
	what string,
	op func(s ActivityStorer) (R, error),
	fold func(total *R, part R),
	onDone func(backend string, res R, err error),
) (R, error) {
	var total R

	primaryRes, primaryErr := op(m.primary)
	fold(&total, primaryRes)
	if primaryErr != nil {
		slog.Warn("[activity-migrate] primary "+what+" failed", "err", primaryErr, "result", primaryRes)
	}
	if onDone != nil {
		onDone(activityBackendName(m.primary), primaryRes, primaryErr)
	}

	if ctxErr := ctx.Err(); ctxErr != nil {
		return total, ctxErr
	}

	secondaryRes, secondaryErr := op(m.secondary)
	fold(&total, secondaryRes)
	if secondaryErr != nil {
		slog.Warn("[activity-migrate] secondary "+what+" failed", "err", secondaryErr, "result", secondaryRes)
	}
	if onDone != nil {
		onDone(activityBackendName(m.secondary), secondaryRes, secondaryErr)
	}

	switch {
	case primaryErr != nil && secondaryErr != nil:
		return total, fmt.Errorf("activity-migrate %s: primary: %w (secondary also failed: %v)", what, primaryErr, secondaryErr)
	case primaryErr != nil:
		return total, fmt.Errorf("activity-migrate %s: primary: %w", what, primaryErr)
	case secondaryErr != nil:
		return total, fmt.Errorf("activity-migrate %s: secondary: %w", what, secondaryErr)
	}
	return total, nil
}

// sumRowCounts is the fold for the methods that return a plain row count.
func sumRowCounts(total *int, part int) { *total += part }

// Summarize folds old rows into summary rows on BOTH backends and returns the
// summed count of originals deleted. See the routing table in the package
// comment for why this was active-only until 2026-09-10. Per-backend
// completion goes to any WithMaintenanceProgress hook on ctx.
func (m *MigratingActivityStore) Summarize(ctx context.Context, olderThan time.Time, tier string) (int, error) {
	return runOnBothBackends(m, ctx, "summarize",
		func(s ActivityStorer) (int, error) { return s.Summarize(ctx, olderThan, tier) },
		sumRowCounts,
		func(backend string, n int, err error) {
			reportMaintenanceDone(ctx, MaintenancePhaseSummarize, backend, int64(n), err)
		})
}

// Prune hard-deletes old rows of tier on BOTH backends and returns the summed
// count. Per-backend completion goes to any WithMaintenanceProgress hook on
// ctx (each backend reports its own batches from inside), and
// runOnBothBackends stops before the secondary once ctx is cancelled, so a
// cancelled cleanup does not prune the second store to completion.
func (m *MigratingActivityStore) Prune(ctx context.Context, olderThan time.Time, tier string) (int, error) {
	return runOnBothBackends(m, ctx, "prune",
		func(s ActivityStorer) (int, error) { return s.Prune(ctx, olderThan, tier) },
		sumRowCounts,
		func(backend string, n int, err error) {
			reportMaintenanceDone(ctx, MaintenancePhasePrune, backend, int64(n), err)
		})
}

// CompactByDay compacts BOTH backends and returns the summed counters.
//
// Both backends receive every write (Record above), so both accumulate the
// compactable rows; compacting only the active one would leave the rollback
// mirror growing unbounded. See the routing table in the package comment for
// why this was active-only until 2026-09-10.
//
// Per-backend completion events go to any WithCompactProgress hook on ctx
// (Done=true, with that backend's own counters and error), so a caller that
// wants per-store numbers reads them from the hook rather than from the sum.
func (m *MigratingActivityStore) CompactByDay(ctx context.Context, olderThan time.Time) (CompactResult, error) {
	return runOnBothBackends(m, ctx, "compact",
		func(s ActivityStorer) (CompactResult, error) { return s.CompactByDay(ctx, olderThan) },
		func(total *CompactResult, part CompactResult) {
			total.DaysCompacted += part.DaysCompacted
			total.EntriesDeleted += part.EntriesDeleted
		},
		func(backend string, res CompactResult, err error) { reportCompactDone(ctx, backend, res, err) })
}

// activityBackendName labels a backend for CompactProgressEvent.Backend using
// the same vocabulary the stores use for their own events. Wrappers
// (instrumentation) are unwrapped by type switch; an unknown type is named by
// its Go type so a log line is never blank.
func activityBackendName(s ActivityStorer) string {
	switch v := s.(type) {
	case *SQLActivityStore:
		return "sqlite"
	case *PebbleActivityStore:
		return "pebble"
	case *NutsActivityStore:
		return "nuts"
	case *InstrumentedActivityStorer:
		return activityBackendName(v.store)
	default:
		return fmt.Sprintf("%T", s)
	}
}

// RecompactDigests re-derives legacy digest items on BOTH backends and returns
// the summed counters. Both backends build their own digests (CompactByDay
// above fans out), so both carry the legacy items this repairs.
func (m *MigratingActivityStore) RecompactDigests(ctx context.Context) (RecompactResult, error) {
	return runOnBothBackends(m, ctx, "recompact",
		func(s ActivityStorer) (RecompactResult, error) { return s.RecompactDigests(ctx) },
		func(total *RecompactResult, part RecompactResult) {
			total.Touched += part.Touched
			total.Skipped += part.Skipped
		}, nil)
}

// RepairActivityIndexes repairs orphaned secondary-index entries on BOTH
// backends and returns the summed counters. Only Pebble keeps such indexes
// (the SQLite implementation is a documented no-op), so routing this to the
// active backend meant that after the flip to SQLite the Pebble mirror's index
// leak was never repaired again.
func (m *MigratingActivityStore) RepairActivityIndexes(ctx context.Context) (ActivityIndexRepairResult, error) {
	return runOnBothBackends(m, ctx, "repair-indexes",
		func(s ActivityStorer) (ActivityIndexRepairResult, error) { return s.RepairActivityIndexes(ctx) },
		func(total *ActivityIndexRepairResult, part ActivityIndexRepairResult) {
			total.Scanned += part.Scanned
			total.Orphaned += part.Orphaned
			total.Malformed += part.Malformed
			total.Deleted += part.Deleted
		},
		func(backend string, res ActivityIndexRepairResult, err error) {
			reportMaintenanceDone(ctx, MaintenancePhaseRepairIndexes, backend, res.Deleted, err)
		})
}

// OptimizeStatistics refreshes planner statistics on BOTH backends, not just the
// active one, and returns the active one's result.
//
// It was the first maintenance method to run on both (2026-09-09), when the
// storage-rewriting ones above were still active-only: statistics are read-only
// bookkeeping that makes a backend's own query plans correct, and the inactive
// backend is still receiving every write (Record goes to both, always).
// Refreshing only the active one would leave the rollback mirror's planner
// blind, so a rollback would land on a backend that has never been analyzed —
// which is exactly the state production's SQLite database was found in on
// 2026-09-09. It returns the ACTIVE backend's result rather than a sum because
// Supported/Bootstrapped are not additive.
//
// The Pebble implementation is a cheap Supported=false no-op, so "both" costs
// nothing while Pebble is one of the two.
func (m *MigratingActivityStore) OptimizeStatistics(ctx context.Context) (ActivityOptimizeResult, error) {
	primaryRes, primaryErr := m.primary.OptimizeStatistics(ctx)
	secondaryRes, secondaryErr := m.secondary.OptimizeStatistics(ctx)
	if primaryErr != nil {
		slog.Warn("[activity] primary OptimizeStatistics failed", "err", primaryErr)
	}
	if secondaryErr != nil {
		slog.Warn("[activity] secondary OptimizeStatistics failed", "err", secondaryErr)
	}
	if m.readSecondary.Load() {
		return secondaryRes, secondaryErr
	}
	return primaryRes, primaryErr
}

// MigrateSystemActivityLogs is the one storage-touching method that stays
// active-only: it is a one-time legacy migration and a documented no-op on the
// Pebble backend.
func (m *MigratingActivityStore) MigrateSystemActivityLogs() (int, error) {
	return m.active().MigrateSystemActivityLogs()
}

// ── lifecycle ───────────────────────────────────────────────────────────────

// Close closes both backends and returns the first error.
func (m *MigratingActivityStore) Close() error {
	sErr := m.secondary.Close()
	pErr := m.primary.Close()
	if pErr != nil {
		return fmt.Errorf("activity-migrate close primary: %w", pErr)
	}
	return sErr
}

var _ ActivityStorer = (*MigratingActivityStore)(nil)
