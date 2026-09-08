// file: internal/database/sql_activity_backfill.go
// version: 2.6.0
// guid: 5b2e9d73-1c46-4f8a-b0d1-7e3a2c6f9048
// last-edited: 2026-09-08

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
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// ActivitySQLBackfillKey is the Pebble sentinel written when the Pebble→SQLite
// backfill has completed AND parity has been verified. The activity wiring reads
// it at boot to decide whether to start already flipped to SQLite.
const ActivitySQLBackfillKey = "system:backfill:activity_sql_v1_done"

// sqlBackfillBatch is how many rows are copied per SQLite transaction.
const sqlBackfillBatch = 500

// sqlBackfillProgressEvery bounds how often a long-running tier emits a progress
// line.
//
// The trigger is wall-clock rather than every-N-batches on purpose. Batch cost
// here varies by orders of magnitude: a batch of 500 iTunes `ApplyITLOperations`
// rows carries ~9.6 MB of `details` EACH, while a batch of ordinary events
// carries a few hundred bytes each. Any fixed N therefore produces either an
// hour of silence or a log flood depending only on which rows a tier happens to
// hold. A time interval gives the same heartbeat cadence either way, which is
// the whole point — the reader needs to distinguish "working" from "wedged".
//
// Sized against observed production throughput (~600 rows/s on the `change`
// tier): ~18k rows per line, a few hundred lines for a multi-hour tier.
const sqlBackfillProgressEvery = 30 * time.Second

// SQLActivityBackfillResult holds the outcome of a Pebble → SQLite backfill run.
type SQLActivityBackfillResult struct {
	TiersProcessed int            `json:"tiers_processed"`
	EntriesScanned int            `json:"entries_scanned"` // rows read from Pebble
	EntriesCopied  int            `json:"entries_copied"`  // rows actually inserted (excludes idempotent skips)
	PerTierScanned map[string]int `json:"per_tier_scanned"`
	// PerTierCopied breaks EntriesCopied down the same way PerTierScanned does.
	// The two differ by exactly the idempotent skips.
	//
	// ⚠️ scanned≫copied MEANS NOTHING ON ITS OWN. Do not diagnose from it.
	//
	// This guide has now been wrong twice, in opposite directions, and the second
	// version was written in this file and disproved by production four hours
	// later — so the rule below is stated as a non-inference rather than as a
	// better list of causes.
	//
	//  v1 (pre-checkpoint): "scanned≫copied means a resumed run re-streaming
	//      already-copied history." Checkpointing broke that: a resume now skips
	//      the history instead of re-reading it.
	//  v2 (2026-09-08): "a genuine resume shows scanned≈copied for its tail, and
	//      never the old shape; scanned≫copied therefore narrows to a `failed`
	//      verdict, a tier in activityNonResumableTiers, or a lost progress blob
	//      — none of them a resume." DISPROVED the same day on prod.
	//
	// What prod actually showed on 2026-09-08, across two processes and ~2.5h of
	// tier `change`: one run reached scanned=965,500 with copied=0 over 1h49m; it
	// was interrupted, checkpointed at row 972,000, and the next process logged
	// `resumed=true resuming_after_row=972000` and ran on to scanned=1,419,500
	// still at copied=0. That is a genuine resume showing copied=0 — v2's
	// "never" — and it is none of v2's three causes: the blob was present and its
	// cursor was used, the tier is `change` and not `digest`, and a `failed`
	// verdict would have reset the cursor to 0 and reported resumed=false.
	//
	// The reason v2 missed it: copied EXCLUDES idempotent skips, and a row is
	// skipped whenever SQLite already holds it — which is true of everything an
	// EARLIER attempt copied, and separately of everything the live dual-write
	// stored (MigratingActivityStore.Record writes both backends under the same
	// content key). A tier that a previous run already copied therefore reports
	// copied=0 forever after, no matter how it is entered. Resumed or not is
	// simply not what this pair measures.
	//
	// So: copied=0 with scanned climbing is the NORMAL, healthy shape for a
	// re-verification pass, and on its own is not evidence of a stall, a lost
	// checkpoint, or a failed verdict. To tell those apart, read the
	// `processing tier` line's `resumed` / `resuming_after_row` fields, which say
	// directly what this pair only ever implied.
	PerTierCopied map[string]int `json:"per_tier_copied"`
	ParityOK      bool           `json:"parity_ok"`
	DryRun        bool           `json:"dry_run"`
	AlreadyDone   bool           `json:"already_done"`
}

// ActivityBackfillProgressUpdate is one observation of a running backfill, handed
// to the optional callback so a caller can surface progress to users without
// re-deriving it or re-reading Pebble. Every field is already computed for the
// log lines this mirrors, so reporting costs no additional I/O.
//
// Scanned and Copied are cumulative FOR THE TIER — they start at the checkpoint,
// not at zero — while Elapsed covers only the current attempt. Dividing one by
// the other on a resumed tier yields a throughput far above anything the scan
// achieved; see the note on the "tier complete" log line below.
type ActivityBackfillProgressUpdate struct {
	Tier       string
	TierIndex  int // 1-based, over TiersTotal
	TiersTotal int
	Scanned    int
	Copied     int
	Elapsed    time.Duration
	// Done marks the final update for this tier, carrying its verdict.
	Done    bool
	Verdict string
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
	return BackfillPebbleActivityToSQLWithProgress(ctx, pebbleStore, sqlStore, dryRun, nil)
}

// BackfillPebbleActivityToSQLWithProgress is BackfillPebbleActivityToSQL with an
// observer. onProgress may be nil; when set it is called at each tier boundary and
// on the same schedule as the progress log line (sqlBackfillProgressEvery).
//
// ⚠️ The callback runs INLINE on the backfill goroutine. It must not block and it
// must not return work to the caller: this migration is the only writer of the
// SQLite activity history and a callback that stalls stalls the copy. Callers that
// persist these updates are expected to swallow their own errors — a reporting
// failure must never abort a run that is hours deep.
func BackfillPebbleActivityToSQLWithProgress(
	ctx context.Context,
	pebbleStore *PebbleActivityStore,
	sqlStore *SQLActivityStore,
	dryRun bool,
	onProgress func(ActivityBackfillProgressUpdate),
) (SQLActivityBackfillResult, error) {
	res := SQLActivityBackfillResult{
		DryRun:         dryRun,
		PerTierScanned: make(map[string]int),
		PerTierCopied:  make(map[string]int),
	}

	if !dryRun {
		if _, closer, err := pebbleStore.db.Get([]byte(ActivitySQLBackfillKey)); err == nil {
			closer.Close()
			slog.Info("[activity-sql-backfill] already done — skipping", "flag", ActivitySQLBackfillKey)
			res.AlreadyDone = true
			res.ParityOK = true
			return res, nil
		}
	}

	// Durable progress. A dry run neither reads nor writes it: it must report
	// what a full run WOULD do (so it cannot skip tiers), and it must not mutate
	// migration state.
	prog := &ActivityBackfillProgress{
		Version: activityProgressVersion,
		Tiers:   make(map[string]*ActivityBackfillTierProgress, len(actTiers)),
	}
	if !dryRun {
		prog = loadActivityBackfillProgress(pebbleStore.db)
		if pending := prog.PendingTiers(); len(pending) < len(actTiers) {
			slog.Info("[activity-sql-backfill] resuming — some tiers are already verified",
				"verified", len(actTiers)-len(pending), "pending", pending)
		}
	}

	slog.Info("[activity-sql-backfill] starting Pebble → SQLite backfill",
		"dry_run", dryRun, "tiers", len(actTiers))

	// report is the single gate for the observer: it fills in the constant field
	// and no-ops when nobody is watching, so the call sites below stay one line
	// and can never dereference a nil callback.
	report := func(u ActivityBackfillProgressUpdate) {
		if onProgress == nil {
			return
		}
		u.TiersTotal = len(actTiers)
		onProgress(u)
	}

	for _, tier := range actTiers {
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		default:
		}

		st := prog.tier(tier)

		// A tier verified by an EARLIER process is not re-scanned. This is the
		// whole point of the checkpoint: production was re-reading ~6.5M rows
		// after every restart.
		if !dryRun && st.State == activityTierClean {
			res.PerTierScanned[tier] = st.Scanned
			res.PerTierCopied[tier] = st.Copied
			res.EntriesScanned += st.Scanned
			res.EntriesCopied += st.Copied
			res.TiersProcessed++
			slog.Info("[activity-sql-backfill] tier already verified by an earlier run — skipping",
				"tier", tier, "tier_index", res.TiersProcessed, "tiers_total", len(actTiers),
				"scanned", st.Scanned, "copied", st.Copied)
			continue
		}

		var resumeFrom []byte
		if !dryRun {
			resumeFrom = resumeCursorFor(tier, st.Cursor)
			// Not resuming means re-scanning the tier from row zero, so the
			// PROGRESS counters must go with the cursor that produced them —
			// carrying them into a full re-scan double-counts every row.
			//
			// ⚠️ Reinserted is NOT reset here, and that asymmetry is the whole
			// point. It is the integrity signal, not a progress counter: an
			// interrupted attempt that observed parity failures records them with
			// no cursor written yet, and clearing them because "there is nothing
			// to resume from" is precisely the laundering this package exists to
			// prevent — the tier would re-scan, find a clean tail, and pass.
			// loadActivityBackfillProgress is the ONLY place allowed to clear a
			// reinserted count, and it does so only for a tier already recorded
			// `failed`, which is what turns a failed verdict into a genuine
			// full re-verification rather than a forgotten one.
			if resumeFrom == nil && (st.Scanned != 0 || st.Copied != 0) {
				st.Cursor = ""
				st.Scanned, st.Copied = 0, 0
			}
		}

		slog.Info("[activity-sql-backfill] processing tier",
			"tier", tier, "tier_index", res.TiersProcessed+1, "tiers_total", len(actTiers),
			"resuming_after_row", st.Scanned, "resumed", resumeFrom != nil, "dry_run", dryRun)

		// Report the tier the moment it starts, before any row is read. A tier can
		// run for hours; without this the surface would show the PREVIOUS tier's
		// name until the first heartbeat 30s later.
		report(ActivityBackfillProgressUpdate{
			Tier: tier, TierIndex: res.TiersProcessed + 1,
			Scanned: st.Scanned, Copied: st.Copied,
		})

		// Progress bookkeeping. A tier can run for hours (production's `change`
		// tier holds ~5.7M rows at ~600 rows/s), and before this the function
		// logged nothing between "processing tier" and the final summary — so a
		// working run and a wedged one were indistinguishable from the log.
		// These three counters START FROM THE CHECKPOINT, not from zero, so they
		// describe the TIER rather than this attempt.
		//
		// tierReinserted carrying forward is the load-bearing one: it is what
		// makes an interrupted parity failure stay failed. Fail 5 rows into a
		// tier, get killed, resume, verify a spotless tail — the tier still ends
		// `failed`, because those 5 are still counted. Reset it to 0 here and a
		// restart silently launders an unverified copy into a clean verdict.
		tierStart := time.Now()
		lastLog := tierStart
		lastScanned := st.Scanned
		tierScanned := st.Scanned

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
		tierReinserted := st.Reinserted
		tierCopied := st.Copied
		_, serr := pebbleStore.streamTierEntries(ctx, tier, sqlBackfillBatch, resumeFrom, func(batch []ActivityEntry, lastKey []byte) error {
			tierScanned += len(batch)
			if !dryRun {
				// Copy pass for this batch.
				copied, cerr := sqlStore.recordBatch(ctx, batch)
				if cerr != nil {
					return fmt.Errorf("copy: %w", cerr)
				}
				tierCopied += copied
				// Parity pass: re-present the same batch. A correct copy inserts ZERO
				// here (every row conflicts on src_key). Any insert means a copied row
				// did not land — record it so the tier fails parity below.
				n, verr := sqlStore.recordBatch(ctx, batch)
				if verr != nil {
					return fmt.Errorf("parity re-present: %w", verr)
				}
				tierReinserted += n

				// Checkpoint AFTER the batch is durably in SQLite, so the cursor
				// can only ever lag the copy, never lead it. Written every batch
				// (~2s of work at production throughput): a coarser throttle
				// would discard minutes of scanning to save one small write.
				//
				// NoSync — losing this costs a re-read of an idempotent batch.
				// The verdict write after the tier uses Sync, because losing THAT
				// is a correctness bug.
				st.State = activityTierInProgress
				st.Cursor = string(lastKey)
				st.Scanned, st.Copied, st.Reinserted = tierScanned, tierCopied, tierReinserted
				if perr := saveActivityBackfillProgress(pebbleStore.db, prog, false); perr != nil {
					// Not fatal: the copy itself is fine, a restart just redoes
					// work from the last cursor that did land. Never silent.
					slog.Warn("[activity-sql-backfill] checkpoint write failed; a restart will redo more of this tier",
						"tier", tier, "scanned", tierScanned, "err", perr)
				}
			}

			// Heartbeat. Rate is computed over the window since the last line, not
			// since tier start, so a slowdown shows up immediately instead of being
			// averaged away by a fast first hour.
			if now := time.Now(); now.Sub(lastLog) >= sqlBackfillProgressEvery {
				window := now.Sub(lastLog).Seconds()
				slog.Info("[activity-sql-backfill] tier progress",
					"tier", tier,
					"scanned", tierScanned,
					"copied", tierCopied,
					"rows_per_sec", int(float64(tierScanned-lastScanned)/window),
					"elapsed", now.Sub(tierStart).Round(time.Second).String(),
					"dry_run", dryRun)
				report(ActivityBackfillProgressUpdate{
					Tier: tier, TierIndex: res.TiersProcessed + 1,
					Scanned: tierScanned, Copied: tierCopied,
					Elapsed: now.Sub(tierStart),
				})
				lastLog, lastScanned = now, tierScanned
			}
			return nil
		})
		if serr != nil {
			// An interrupted tier keeps its cursor but earns NO verdict. Flush it
			// with Sync so a graceful shutdown resumes exactly where it stopped
			// rather than at the last NoSync batch that happened to reach disk.
			if !dryRun {
				st.Scanned, st.Copied, st.Reinserted = tierScanned, tierCopied, tierReinserted
				if perr := saveActivityBackfillProgress(pebbleStore.db, prog, true); perr != nil {
					slog.Warn("[activity-sql-backfill] could not flush progress on interruption",
						"tier", tier, "err", perr)
				}
			}
			return res, fmt.Errorf("activity-sql-backfill: stream tier=%s: %w", tier, serr)
		}

		// The tier is fully scanned, so it now earns a durable verdict. The
		// cursor is cleared either way: a clean tier is never revisited, and a
		// failed one must be re-scanned IN FULL (see loadActivityBackfillProgress)
		// so that a clean tail can never launder an earlier failure.
		st.Scanned, st.Copied, st.Reinserted = tierScanned, tierCopied, tierReinserted
		st.Cursor = ""
		if tierReinserted != 0 {
			st.State = activityTierFailed
		} else {
			st.State = activityTierClean
		}

		res.PerTierScanned[tier] = tierScanned
		res.PerTierCopied[tier] = tierCopied
		res.EntriesScanned += tierScanned
		res.EntriesCopied += tierCopied
		// ⚠️ Do NOT derive a rate from this line. `scanned`/`copied` are
		// cumulative for the TIER (they start at the checkpoint), while `elapsed`
		// covers THIS ATTEMPT only. On a resumed tier the pair divides out to a
		// throughput far above anything the scan achieved.
		slog.Info("[activity-sql-backfill] tier complete",
			"tier", tier,
			"tier_index", res.TiersProcessed+1, "tiers_total", len(actTiers),
			"scanned", tierScanned, "copied", tierCopied,
			"skipped_already_present", tierScanned-tierCopied,
			"verdict", st.State,
			"elapsed", time.Since(tierStart).Round(time.Second).String(),
			"dry_run", dryRun)
		if tierReinserted != 0 {
			slog.Error("[activity-sql-backfill] PARITY FAIL — re-presentation inserted rows; not flipping",
				"tier", tier, "scanned", tierScanned, "reinserted_on_second_pass", tierReinserted)
		}
		report(ActivityBackfillProgressUpdate{
			Tier: tier, TierIndex: res.TiersProcessed + 1,
			Scanned: tierScanned, Copied: tierCopied,
			Elapsed: time.Since(tierStart),
			Done:    true, Verdict: st.State,
		})

		// Sync: losing a verdict is the one failure that could let unverified
		// data go live, so a write error here fails the run rather than leaving
		// the census guessing.
		if !dryRun {
			if perr := saveActivityBackfillProgress(pebbleStore.db, prog, true); perr != nil {
				return res, fmt.Errorf("activity-sql-backfill: persist verdict for tier=%s: %w", tier, perr)
			}
		}
		res.TiersProcessed++
	}

	if dryRun {
		// A dry run copies nothing and therefore verifies nothing; it reports
		// what a real run would read, and makes no parity claim.
		res.ParityOK = true
		slog.Info("[activity-sql-backfill] dry-run complete",
			"entries_would_copy", res.EntriesScanned, "tiers", res.TiersProcessed)
		return res, nil
	}

	// THE GATE. This asks the durable census "does every tier carry a clean
	// verdict?" — NOT "did this process happen to see a failure?". The
	// difference is the whole point of the progress blob: a resumed run starts
	// with no memory of an earlier tier's failure, so an in-memory flag would
	// answer "no failures seen" and flip reads onto an unverified copy.
	res.ParityOK = prog.AllTiersClean()
	if !res.ParityOK {
		return res, fmt.Errorf(
			"activity-sql-backfill: parity check failed (tiers not verified: %v); sentinel NOT written, reads stay on Pebble",
			prog.PendingTiers())
	}

	// Sentinel: only after verified parity.
	if err := pebbleStore.db.Set([]byte(ActivitySQLBackfillKey),
		fmt.Appendf(nil, "copied=%d scanned=%d", res.EntriesCopied, res.EntriesScanned),
		pebble.Sync); err != nil {
		return res, fmt.Errorf("activity-sql-backfill: write sentinel: %w", err)
	}
	// The sentinel now answers everything the progress blob was tracking, so
	// drop it rather than leave stale resume state for a future reader to
	// misinterpret. Ordered strictly after the sentinel write: if the process
	// dies between the two, the sentinel already says "done" and the leftover
	// blob is merely ignored — the reverse order could lose both.
	clearActivityBackfillProgress(pebbleStore.db)
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
