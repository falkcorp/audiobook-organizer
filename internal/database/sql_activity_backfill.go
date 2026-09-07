// file: internal/database/sql_activity_backfill.go
// version: 2.1.0
// guid: 5b2e9d73-1c46-4f8a-b0d1-7e3a2c6f9048
// last-edited: 2026-09-07

// Package database — Pebble → SQLite activity backfill for the backend cutover.
//
// WHY: during the dual-write window the SQLite store receives every NEW write,
// but historic entries live only in Pebble. This copies them. Every insert is
// keyed by the entry's deterministic content hash (activitySrcKey) and issued as
// INSERT … ON CONFLICT(src_key) DO NOTHING, so the copy is idempotent against
// BOTH a resumed/re-run backfill AND the live dual-write of the same event — no
// duplicate can arise, and there is no timestamp cutoff to get wrong (activity
// timestamps are caller-supplied and non-monotonic, so a cutoff is unsound).
//
// THE FLIP IS GATED ON PARITY, NOT ON "no error". After copying a tier it
// re-presents every scanned entry; the second pass MUST insert zero rows, which
// proves every scanned entry is already present. A plain count-equality gate is
// WRONG here: content-dedup means the number of Pebble rows scanned is ≥ the
// number of distinct SQLite rows, so equal counts cannot be required. This repo
// has a backfill-false-success incident on record; a returned nil error is not
// evidence the data arrived, so the gate must be able to actually fail.
package database

import (
	"context"
	"fmt"
	"log/slog"

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

// BackfillPebbleActivityToSQL copies ALL Pebble activity history into sqlStore,
// verifies per-tier parity by re-presentation, and (unless dryRun) writes the
// sentinel on success. It does NOT flip reads — the caller does that only when
// ParityOK is true.
//
// There is no timestamp bound: every insert is idempotent by content key, so
// copying a row the live dual-write already stored is a no-op rather than a
// duplicate.
func BackfillPebbleActivityToSQL(
	ctx context.Context,
	pebbleStore *PebbleActivityStore,
	sqlStore *SQLActivityStore,
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

	res.ParityOK = true
	for _, tier := range actTiers {
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		default:
		}

		if !dryRun {
			slog.Info("[activity-sql-backfill] processing tier", "tier", tier)
		}

		// Stream the tier in bounded batches instead of materialising it. Each
		// batch is copied into SQLite and then re-presented for parity WITHIN the
		// same fn call, reusing the same in-memory slice — so at most
		// sqlBackfillBatch rows are ever live, and the parity re-present can never
		// pick up a concurrent live dual-write (it only ever re-presents the batch
		// it just copied, never re-reads Pebble). See streamTierEntries for why a
		// materialising scan here OOM-killed prod on the `change` tier.
		//
		// Per-batch copy+parity is equivalent to the old whole-tier copy-then-parity:
		// content-key dedup makes a row that repeats within or across batches insert
		// exactly once on copy and zero times on every re-present, so EntriesCopied
		// and the parity verdict are identical either way.
		tierReinserted := 0
		scanned, serr := pebbleStore.streamTierEntries(ctx, tier, sqlBackfillBatch, func(batch []ActivityEntry) error {
			if dryRun {
				return nil // count only; no writes
			}
			// Copy pass for this batch.
			copied, cerr := sqlStore.recordBatch(ctx, batch)
			if cerr != nil {
				return fmt.Errorf("copy: %w", cerr)
			}
			res.EntriesCopied += copied
			// Parity pass: re-present the same batch. A correct copy inserts ZERO
			// here (every row conflicts on src_key). Any insert means a copied row
			// did not land — record it so the tier fails parity below.
			n, verr := sqlStore.recordBatch(ctx, batch)
			if verr != nil {
				return fmt.Errorf("parity re-present: %w", verr)
			}
			tierReinserted += n
			return nil
		})
		if serr != nil {
			return res, fmt.Errorf("activity-sql-backfill: stream tier=%s: %w", tier, serr)
		}
		res.PerTierScanned[tier] = scanned
		res.EntriesScanned += scanned
		if tierReinserted != 0 {
			res.ParityOK = false
			slog.Error("[activity-sql-backfill] PARITY FAIL — re-presentation inserted rows; not flipping",
				"tier", tier, "scanned", scanned, "reinserted_on_second_pass", tierReinserted)
		}
		res.TiersProcessed++
	}

	if dryRun {
		slog.Info("[activity-sql-backfill] dry-run complete",
			"entries_would_copy", res.EntriesScanned, "tiers", res.TiersProcessed)
		return res, nil
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
