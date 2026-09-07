// file: internal/database/sql_activity_backfill.go
// version: 1.0.0
// guid: 5b2e9d73-1c46-4f8a-b0d1-7e3a2c6f9048
// last-edited: 2026-09-07

// Package database — Pebble → SQLite activity backfill for the backend cutover.
//
// WHY: during the dual-write window the SQLite store receives every NEW write,
// but historic entries live only in Pebble. This copies them, keyed by the
// Pebble primary key ("act:<tier>:<nano>:<ulid>") stored in SQLite's src_key
// column, so the copy is idempotent (ON CONFLICT DO NOTHING) and a resumed or
// re-run backfill never duplicates. Live dual-written rows keep src_key NULL and
// are never touched here.
//
// THE FLIP IS GATED ON PARITY, NOT ON "no error". After copying, it verifies
// per tier that the number of backfilled SQLite rows (src_key IS NOT NULL)
// equals the number of Pebble rows scanned. Only if every tier matches does it
// write the sentinel and let the caller flip reads to SQLite. This repo has a
// backfill-false-success incident on record; a returned nil error is not
// evidence the data arrived.
package database

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// ActivitySQLBackfillKey is the Pebble sentinel written when the Pebble→SQLite
// backfill has completed AND parity has been verified. The activity wiring reads
// it at boot to decide whether to start already flipped to SQLite.
const ActivitySQLBackfillKey = "system:backfill:activity_sql_v1_done"

// sqlBackfillBatch is how many rows are copied per SQLite transaction.
const sqlBackfillBatch = 500

// SQLActivityBackfillResult holds the outcome of a Pebble → SQLite backfill run.
type SQLActivityBackfillResult struct {
	TiersProcessed int            `json:"tiers_processed"`
	EntriesScanned int            `json:"entries_scanned"` // rows read from Pebble
	EntriesCopied  int            `json:"entries_copied"`  // rows actually inserted (excludes idempotent skips)
	PerTierScanned map[string]int `json:"per_tier_scanned"`
	ParityOK       bool           `json:"parity_ok"`
	DryRun         bool           `json:"dry_run"`
	AlreadyDone    bool           `json:"already_done"`
}

// backfilledCount returns how many rows of tier were copied by a backfill
// (src_key IS NOT NULL), i.e. excluding live dual-written rows.
func (s *SQLActivityStore) backfilledCount(ctx context.Context, tier string) (int, error) {
	var n int
	err := s.reader.QueryRowContext(ctx, s.dialect.rebind(
		`SELECT COUNT(*) FROM activity WHERE tier = ? AND src_key IS NOT NULL`), tier).Scan(&n)
	return n, err
}

// BackfillPebbleActivityToSQL copies Pebble activity history into sqlStore,
// verifies per-tier parity, and (unless dryRun) writes the sentinel on success.
// It does NOT flip reads — the caller does that only when ParityOK is true.
//
// It copies ONLY rows OLDER than `until` (the migration cutoff): rows at or
// after the cutoff are already in sqlStore via dual-write, and copying them
// would duplicate the event. Pass the wrapper's MigrationCutoff().
func BackfillPebbleActivityToSQL(
	ctx context.Context,
	pebbleStore *PebbleActivityStore,
	sqlStore *SQLActivityStore,
	until time.Time,
	dryRun bool,
) (SQLActivityBackfillResult, error) {
	res := SQLActivityBackfillResult{DryRun: dryRun, PerTierScanned: make(map[string]int)}

	if !dryRun {
		if _, closer, err := pebbleStore.db.Get([]byte(ActivitySQLBackfillKey)); err == nil {
			closer.Close()
			slog.Info("[activity-sql-backfill] already done — skipping", "flag", ActivitySQLBackfillKey)
			res.AlreadyDone = true
			res.ParityOK = true
			return res, nil
		}
	}

	slog.Info("[activity-sql-backfill] starting Pebble → SQLite backfill",
		"dry_run", dryRun, "tiers", len(actTiers))

	for _, tier := range actTiers {
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		default:
		}

		kvs, err := pebbleStore.scanTierKVs(ctx, tier, nil, &until)
		if err != nil {
			return res, fmt.Errorf("activity-sql-backfill: scan pebble tier=%s: %w", tier, err)
		}
		res.PerTierScanned[tier] = len(kvs)
		res.EntriesScanned += len(kvs)
		if len(kvs) == 0 {
			res.TiersProcessed++
			continue
		}

		slog.Info("[activity-sql-backfill] processing tier",
			"tier", tier, "entries", len(kvs), "dry_run", dryRun)

		if !dryRun {
			for i := 0; i < len(kvs); i += sqlBackfillBatch {
				select {
				case <-ctx.Done():
					return res, ctx.Err()
				default:
				}
				end := min(i+sqlBackfillBatch, len(kvs))
				chunk := kvs[i:end]
				entries := make([]ActivityEntry, len(chunk))
				keys := make([]string, len(chunk))
				for j, kv := range chunk {
					entries[j] = kv.entry
					keys[j] = string(kv.key)
				}
				copied, cerr := sqlStore.recordBatchWithKeys(ctx, entries, keys)
				if cerr != nil {
					return res, fmt.Errorf("activity-sql-backfill: copy tier=%s: %w", tier, cerr)
				}
				res.EntriesCopied += copied
			}
		}
		res.TiersProcessed++
	}

	if dryRun {
		slog.Info("[activity-sql-backfill] dry-run complete",
			"entries_would_copy", res.EntriesScanned, "tiers", res.TiersProcessed)
		return res, nil
	}

	// Parity gate: every Pebble row must be present in SQLite as a backfilled row.
	res.ParityOK = true
	for _, tier := range actTiers {
		want := res.PerTierScanned[tier]
		got, err := sqlStore.backfilledCount(ctx, tier)
		if err != nil {
			return res, fmt.Errorf("activity-sql-backfill: parity count tier=%s: %w", tier, err)
		}
		if got != want {
			res.ParityOK = false
			slog.Error("[activity-sql-backfill] PARITY MISMATCH — not flipping",
				"tier", tier, "pebble", want, "sqlite_backfilled", got)
		}
	}
	if !res.ParityOK {
		return res, fmt.Errorf("activity-sql-backfill: parity check failed; sentinel NOT written, reads stay on Pebble")
	}

	// Sentinel: only after verified parity.
	if err := pebbleStore.db.Set([]byte(ActivitySQLBackfillKey),
		fmt.Appendf(nil, "copied=%d scanned=%d", res.EntriesCopied, res.EntriesScanned),
		pebble.Sync); err != nil {
		return res, fmt.Errorf("activity-sql-backfill: write sentinel: %w", err)
	}
	slog.Info("[activity-sql-backfill] complete — parity verified, sentinel written",
		"flag", ActivitySQLBackfillKey,
		"scanned", res.EntriesScanned, "copied", res.EntriesCopied, "tiers", res.TiersProcessed)
	return res, nil
}

// SQLBackfillDone reports whether this Pebble store carries the Pebble→SQLite
// migration sentinel — i.e. a prior run copied history and verified parity, so
// the activity wiring may start already flipped to SQLite.
func (s *PebbleActivityStore) SQLBackfillDone() bool { return IsActivitySQLBackfillDone(s.db) }

// IsActivitySQLBackfillDone reports whether the Pebble→SQLite sentinel exists.
func IsActivitySQLBackfillDone(db *pebble.DB) bool {
	_, closer, err := db.Get([]byte(ActivitySQLBackfillKey))
	if err == nil {
		closer.Close()
		return true
	}
	return false
}
