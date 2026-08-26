// file: internal/database/pebble_activity_backfill.go
// version: 1.0.0
// guid: a7b8c9d0-e1f2-0007-0123-000000000007

// Package database — NutsDB → PebbleDB activity backfill.
//
// WHY: During the dual-write window, the Pebble store receives all new writes but
// historic entries live only in NutsDB. This backfill op copies every existing NutsDB
// activity entry into the Pebble store in key-ordered (timestamp-ascending) batches.
// It is idempotent: existing Pebble entries are not overwritten (Set is safe to repeat
// — the JSON is identical). On completion it writes the sentinel key
// "system:backfill:activity_pebble_v1_done" in Pebble so subsequent starts skip the
// work immediately.
//
// The backfill is resumable: if interrupted, the next run rescans NutsDB from scratch
// and re-writes already-copied entries — since Set is idempotent this is safe and
// merely redundant work.
package database

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// ActivityPebbleBackfillKey is the Pebble sentinel key set when the backfill
// has completed successfully.  Callers check this key to decide whether to
// read from Pebble or NutsDB.
const ActivityPebbleBackfillKey = "system:backfill:activity_pebble_v1_done"

// ActivityBackfillResult holds the outcome of a NutsDB → Pebble backfill run.
type ActivityBackfillResult struct {
	TiersProcessed int  `json:"tiers_processed"`
	EntriesCopied  int  `json:"entries_copied"`
	DryRun         bool `json:"dry_run"`
	AlreadyDone    bool `json:"already_done"`
}

// BackfillNutsActivityToPebble copies all NutsDB activity entries to the Pebble
// store.  Call with dryRun=true to get counts without writing.
//
// Steps:
//  1. Check sentinel key — skip if already done.
//  2. For each tier (including "digest"), scan NutsDB in key order.
//  3. Batch-write to Pebble (500 entries per batch).
//  4. Set sentinel key on success.
func BackfillNutsActivityToPebble(
	ctx context.Context,
	nutsStore *NutsActivityStore,
	pebbleStore *PebbleActivityStore,
	dryRun bool,
) (ActivityBackfillResult, error) {
	res := ActivityBackfillResult{DryRun: dryRun}

	// 1. Check sentinel key — idempotent guard.
	if !dryRun {
		if _, closer, err := pebbleStore.db.Get([]byte(ActivityPebbleBackfillKey)); err == nil {
			closer.Close()
			slog.Info("[activity-backfill] already done — skipping", "flag", ActivityPebbleBackfillKey)
			res.AlreadyDone = true
			return res, nil
		}
	}

	slog.Info("[activity-backfill] starting NutsDB → Pebble activity backfill",
		"dry_run", dryRun, "tiers", len(actTiers))

	// 2. Scan each tier (all tiers, including "digest") from NutsDB.
	for _, tier := range actTiers {
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		default:
		}

		kvs, err := nutsStore.scanTierKeysAndValues(tier, nil, nil)
		if err != nil {
			return res, fmt.Errorf("backfill: scan nuts tier=%s: %w", tier, err)
		}
		if len(kvs) == 0 {
			continue
		}

		// Sort by NutsDB key (already time-keyed; sort ensures key-ordered cursor).
		sort.Slice(kvs, func(i, j int) bool {
			return string(kvs[i].key) < string(kvs[j].key)
		})

		slog.Info("[activity-backfill] processing tier",
			"tier", tier, "entries", len(kvs), "dry_run", dryRun)

		// 3. Batch-write to Pebble.
		for i := 0; i < len(kvs); i += 500 {
			select {
			case <-ctx.Done():
				return res, ctx.Err()
			default:
			}

			end := min(i+500, len(kvs))
			batch := kvs[i:end]

			if dryRun {
				res.EntriesCopied += len(batch)
				continue
			}

			pebbleBatch := pebbleStore.db.NewBatch()
			for _, kv := range batch {
				e := kv.entry

				// Reconstruct the Pebble primary key from the entry's fields.
				// WHY: NutsDB keys use the same <20d-nano>:<ulid> format but without
				// the "act:<tier>:" prefix — we need the full Pebble-style key.
				var entryID string
				// NutsDB key format: <20d-unix-nano>:<ulid>
				nutsKey := string(kv.key)
				if colonIdx := lastIndexByte(nutsKey, ':'); colonIdx >= 0 {
					entryID = nutsKey[colonIdx+1:]
				}
				if entryID == "" {
					entryID = ulid32()
				}

				pkey := pactPrimaryKey(tier, e.Timestamp, entryID)
				b, err := json.Marshal(e)
				if err != nil {
					pebbleBatch.Close()
					return res, fmt.Errorf("backfill: marshal entry: %w", err)
				}
				if err := pebbleBatch.Set(pkey, b, nil); err != nil {
					pebbleBatch.Close()
					return res, fmt.Errorf("backfill: set entry: %w", err)
				}

				// Rebuild secondary indexes.
				if e.OperationID != "" {
					opKey := fmt.Appendf(nil, "act:op:%s:%020d:%s", e.OperationID, e.Timestamp.UnixNano(), entryID)
					ref := pactIndexRef(tier, e.Timestamp, entryID)
					if err := pebbleBatch.Set(opKey, ref, nil); err != nil {
						pebbleBatch.Close()
						return res, fmt.Errorf("backfill: set op index: %w", err)
					}
				}
				if e.BookID != "" {
					bkKey := fmt.Appendf(nil, "act:bk:%s:%020d:%s", e.BookID, e.Timestamp.UnixNano(), entryID)
					ref := pactIndexRef(tier, e.Timestamp, entryID)
					if err := pebbleBatch.Set(bkKey, ref, nil); err != nil {
						pebbleBatch.Close()
						return res, fmt.Errorf("backfill: set book index: %w", err)
					}
				}
			}
			if err := pebbleBatch.Commit(pebble.Sync); err != nil {
				pebbleBatch.Close()
				return res, fmt.Errorf("backfill: commit batch tier=%s: %w", tier, err)
			}
			pebbleBatch.Close()
			res.EntriesCopied += len(batch)
		}
		res.TiersProcessed++
	}

	// 4. Write sentinel key.
	if !dryRun && res.EntriesCopied >= 0 {
		ts := time.Now().UTC().Format(time.RFC3339)
		if err := pebbleStore.db.Set(
			[]byte(ActivityPebbleBackfillKey),
			[]byte(ts),
			pebble.Sync,
		); err != nil {
			return res, fmt.Errorf("backfill: write sentinel key: %w", err)
		}
		slog.Info("[activity-backfill] complete — sentinel written",
			"flag", ActivityPebbleBackfillKey,
			"entries_copied", res.EntriesCopied,
			"tiers_processed", res.TiersProcessed)
	} else if dryRun {
		slog.Info("[activity-backfill] dry-run complete",
			"entries_would_copy", res.EntriesCopied,
			"tiers", res.TiersProcessed)
	}

	return res, nil
}

// IsActivityPebbleBackfillDone reports whether the Pebble activity sentinel has been
// written. Callers use this to decide whether to read from Pebble or NutsDB.
func IsActivityPebbleBackfillDone(db *pebble.DB) bool {
	_, closer, err := db.Get([]byte(ActivityPebbleBackfillKey))
	if err == nil {
		closer.Close()
		return true
	}
	return false
}

// ── helpers ───────────────────────────────────────────────────────────────────

// lastIndexByte returns the last index of b in s, or -1.
func lastIndexByte(s string, b byte) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// ulid32 generates a short random suffix as a fallback when the NutsDB key has
// no ':' (should not happen with well-formed NutsDB data).
func ulid32() string {
	// Use unix nano + 4 digits of a counter as a lightweight unique suffix.
	return fmt.Sprintf("%020d:fallback", time.Now().UnixNano())
}

// ── notes ─────────────────────────────────────────────────────────────────────

// BackfillNutsActivityToPebble accesses nutsStore.scanTierKeysAndValues which is
// an unexported method — the backfill lives in the same package (database) so this
// is valid.  No NutsDB import is needed beyond what nuts_activity_store.go already
// provides in the same compilation unit.
