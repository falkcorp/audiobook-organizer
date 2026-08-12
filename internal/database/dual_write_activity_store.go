// file: internal/database/dual_write_activity_store.go
// version: 1.2.0
// guid: f6a7b8c9-d0e1-0006-f012-000000000006
// last-edited: 2026-08-11

// Package database — dual-write wrapper for the activity migration window.
//
// UNUSED as of 2026-07-03 (TASK-22): the activitystore service in
// internal/activity/register.go now returns *PebbleActivityStore directly.
// This wrapper is retained, unwired, for one release cycle as a rollback
// reference; it will be deleted (along with NutsActivityStore/
// NutsMetricsStore and the nutsdb dependency) in the follow-up NutsDB-removal
// PR once the soak period has passed and an owner has greenlit removal.
//
// WHY a dual-write wrapper (historical):
//   - The NutsDB → Pebble migration requires a hot-deploy cutover window during
//     which BOTH backends receive every write. This prevents data loss if the
//     Pebble flag is not yet set (e.g., prod hasn't backfilled yet).
//   - Once the backfill op sets "activity_pebble_v1_done" in Pebble, reads
//     switch from NutsDB to Pebble. Until then, reads come from NutsDB.
//   - Once the NutsDB dep is removed (follow-up task), this wrapper can be
//     collapsed to a simple pass-through to PebbleActivityStore.
//
// Behaviour:
//   - Writes: always go to both primary (Nuts or Pebble) and secondary.
//   - Reads: routed to whichever backend the ReadFrom flag selects.
//   - Close: closes both backends.
package database

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// DualWriteActivityStore writes to two ActivityStorers simultaneously and reads
// from the one indicated by ReadFromPebble.
//
// If ReadFromPebble is false, reads come from nuts (pre-backfill state).
// If ReadFromPebble is true, reads come from pebble (post-backfill state).
//
// Write errors from the secondary backend are logged but not returned to the
// caller — the primary backend's result is authoritative.
type DualWriteActivityStore struct {
	nuts          ActivityStorer
	pebble        ActivityStorer
	ReadFromPebble bool
}

// NewDualWriteActivityStore creates a dual-write wrapper.
// nuts is the legacy NutsDB backend; pebble is the new Pebble backend.
// ReadFromPebble controls which backend serves reads.
func NewDualWriteActivityStore(nuts, pebble ActivityStorer, readFromPebble bool) *DualWriteActivityStore {
	return &DualWriteActivityStore{
		nuts:           nuts,
		pebble:         pebble,
		ReadFromPebble: readFromPebble,
	}
}

// ── ActivityStorer writes — both backends ────────────────────────────────────

// Record inserts into both backends. The nuts result is returned when
// ReadFromPebble is false; the pebble result otherwise.
func (d *DualWriteActivityStore) Record(e ActivityEntry) (int64, error) {
	nutsID, nutsErr := d.nuts.Record(e)
	pebbleID, pebbleErr := d.pebble.Record(e)

	if pebbleErr != nil {
		slog.Warn("[dual-write] pebble Record failed (secondary write error)",
			"err", pebbleErr)
	}
	if nutsErr != nil {
		slog.Warn("[dual-write] nuts Record failed (secondary write error)",
			"err", nutsErr)
	}

	if d.ReadFromPebble {
		return pebbleID, pebbleErr
	}
	return nutsID, nutsErr
}

// Summarize runs on both; returns primary backend's result.
func (d *DualWriteActivityStore) Summarize(ctx context.Context, olderThan time.Time, tier string) (int, error) {
	nutsCnt, nutsErr := d.nuts.Summarize(ctx, olderThan, tier)
	pebbleCnt, pebbleErr := d.pebble.Summarize(ctx, olderThan, tier)

	if pebbleErr != nil {
		slog.Warn("[dual-write] pebble Summarize failed", "err", pebbleErr)
	}
	if nutsErr != nil {
		slog.Warn("[dual-write] nuts Summarize failed", "err", nutsErr)
	}

	if d.ReadFromPebble {
		return pebbleCnt, pebbleErr
	}
	return nutsCnt, nutsErr
}

// Prune runs on both; returns primary backend's result.
func (d *DualWriteActivityStore) Prune(olderThan time.Time, tier string) (int, error) {
	nutsCnt, nutsErr := d.nuts.Prune(olderThan, tier)
	pebbleCnt, pebbleErr := d.pebble.Prune(olderThan, tier)

	if pebbleErr != nil {
		slog.Warn("[dual-write] pebble Prune failed", "err", pebbleErr)
	}
	if nutsErr != nil {
		slog.Warn("[dual-write] nuts Prune failed", "err", nutsErr)
	}

	if d.ReadFromPebble {
		return pebbleCnt, pebbleErr
	}
	return nutsCnt, nutsErr
}

// WipeAllActivity runs on both; returns primary backend's result.
func (d *DualWriteActivityStore) WipeAllActivity() (int64, error) {
	nutsCnt, nutsErr := d.nuts.WipeAllActivity()
	pebbleCnt, pebbleErr := d.pebble.WipeAllActivity()

	if pebbleErr != nil {
		slog.Warn("[dual-write] pebble WipeAllActivity failed", "err", pebbleErr)
	}
	if nutsErr != nil {
		slog.Warn("[dual-write] nuts WipeAllActivity failed", "err", nutsErr)
	}

	if d.ReadFromPebble {
		return pebbleCnt, pebbleErr
	}
	return nutsCnt, nutsErr
}

// CompactByDay runs on both; returns primary backend's result.
func (d *DualWriteActivityStore) CompactByDay(ctx context.Context, olderThan time.Time) (CompactResult, error) {
	nutsRes, nutsErr := d.nuts.CompactByDay(ctx, olderThan)
	pebbleRes, pebbleErr := d.pebble.CompactByDay(ctx, olderThan)

	if pebbleErr != nil {
		slog.Warn("[dual-write] pebble CompactByDay failed", "err", pebbleErr)
	}
	if nutsErr != nil {
		slog.Warn("[dual-write] nuts CompactByDay failed", "err", nutsErr)
	}

	if d.ReadFromPebble {
		return pebbleRes, pebbleErr
	}
	return nutsRes, nutsErr
}

// MigrateSystemActivityLogs runs only on the primary read backend.
// This is a one-shot SQLite→nuts migration; the Pebble store has no SQLite data.
func (d *DualWriteActivityStore) MigrateSystemActivityLogs() (int, error) {
	if d.ReadFromPebble {
		return d.pebble.MigrateSystemActivityLogs()
	}
	return d.nuts.MigrateSystemActivityLogs()
}

// RecompactDigests runs on both; returns primary backend's result.
func (d *DualWriteActivityStore) RecompactDigests(ctx context.Context) (RecompactResult, error) {
	nutsRes, nutsErr := d.nuts.RecompactDigests(ctx)
	pebbleRes, pebbleErr := d.pebble.RecompactDigests(ctx)

	if pebbleErr != nil {
		slog.Warn("[dual-write] pebble RecompactDigests failed", "err", pebbleErr)
	}
	if nutsErr != nil {
		slog.Warn("[dual-write] nuts RecompactDigests failed", "err", nutsErr)
	}

	if d.ReadFromPebble {
		return pebbleRes, pebbleErr
	}
	return nutsRes, nutsErr
}

// ── ActivityStorer reads — routed to active backend ──────────────────────────

// Query reads from the active backend only.
func (d *DualWriteActivityStore) Query(ctx context.Context, f ActivityFilter) ([]ActivityEntry, int, error) {
	if d.ReadFromPebble {
		return d.pebble.Query(ctx, f)
	}
	return d.nuts.Query(ctx, f)
}

// GetDistinctSources reads from the active backend only.
func (d *DualWriteActivityStore) GetDistinctSources(ctx context.Context, f ActivityFilter) ([]SourceCount, error) {
	if d.ReadFromPebble {
		return d.pebble.GetDistinctSources(ctx, f)
	}
	return d.nuts.GetDistinctSources(ctx, f)
}

// Close closes both backends and returns the first error encountered.
func (d *DualWriteActivityStore) Close() error {
	nutsErr := d.nuts.Close()
	pebbleErr := d.pebble.Close()
	if nutsErr != nil {
		return fmt.Errorf("dual-write close nuts: %w", nutsErr)
	}
	return pebbleErr
}

// ── interface assertion ───────────────────────────────────────────────────────

var _ ActivityStorer = (*DualWriteActivityStore)(nil)
