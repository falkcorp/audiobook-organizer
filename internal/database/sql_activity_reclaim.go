// file: internal/database/sql_activity_reclaim.go
// version: 1.0.0
// guid: 3f8c1a56-90d4-4e27-b6a1-7c05e2d84b93
// last-edited: 2026-09-08

package database

import (
	"context"
	"fmt"
	"time"
)

// Reclaiming the Pebble copy of the activity log after the SQLite cutover.
//
// WHY THIS EXISTS. NewMigratingActivityStore dual-writes every activity row to
// both backends, and the Pebble→SQLite backfill only ever COPIES. Nothing has
// ever deleted the Pebble side, so once the cutover flips reads to SQLite the
// entire Pebble activity keyspace — ~1.3 GiB on production, of which ~0.78 GiB
// was act:op:/act:bk: index entries — sits in the main database being read by
// nobody. This is the delete half that was never written.

// activityReclaimMinRetain is the floor on how much recent history a reclaim
// must leave in Pebble, however small a retain the caller asks for.
//
// It is the anti-race guard, and it is why this prunes behind a cutoff instead
// of calling WipeAllActivity on the primary. Record fans out to BOTH backends
// on every write and there is no post-cutover stop, so the Pebble store is a
// LIVE store, not a frozen one: a total wipe would range-delete rows that
// landed between the parity check and the delete. Rows older than the cutoff
// cannot be in that window, so a cutoff-bounded prune cannot race a dual-write
// no matter how long it runs.
const activityReclaimMinRetain = 24 * time.Hour

// ActivityReclaimTier is the per-tier outcome of a reclaim.
type ActivityReclaimTier struct {
	// Eligible is how many primary rows sit behind the cutoff. It is counted
	// on both dry and applied runs, so a dry run reports exactly what an
	// applied one would delete.
	Eligible int `json:"eligible"`
	Deleted  int `json:"deleted"`
}

// ActivityReclaimResult is the full census plus whatever was deleted.
//
// The census is populated even when the run is refused. A reclaim that can only
// ever say "no" is not a usable maintenance job: the operator needs to see WHY
// it refused and how much is waiting behind the block.
type ActivityReclaimResult struct {
	ReadSecondary bool   `json:"read_secondary"`
	Refused       bool   `json:"refused"`
	RefusedReason string `json:"refused_reason,omitempty"`
	DryRun        bool   `json:"dry_run"`

	Cutoff        time.Time `json:"cutoff"`
	PrimaryRows   int       `json:"primary_rows"`
	SecondaryRows int       `json:"secondary_rows"`
	EligibleRows  int       `json:"eligible_rows"`
	RowsDeleted   int       `json:"rows_deleted"`

	PerTier map[string]ActivityReclaimTier `json:"per_tier"`
}

// ActivityReclaimProgress is called once per tier so a long reclaim can report
// liveness. Prune is one blocking call per tier with no callback of its own, so
// between-tier is the finest granularity available; the caller sizes its
// ProgressTimeout accordingly.
type ActivityReclaimProgress func(tier string, index, total int, t ActivityReclaimTier)

// ReclaimMigratedActivity deletes Pebble-side activity rows that SQLite already
// holds, freeing space in the main database.
//
// It refuses unless reads have been flipped to SQLite. That flip is the only
// signal that means "the copy is verified": sqlMigrationStarter sets it solely
// after BackfillPebbleActivityToSQL reports per-tier ParityOK. While it is
// false, Pebble is what the activity UI is reading, and deleting from it would
// destroy live data rather than reclaim dead data.
//
// On the rows SQLite may be missing: once reads are on SQLite, anything present
// in Pebble but absent from SQLite (a secondary Record that failed and was
// logged rather than returned) is ALREADY unreachable — no query path reads
// Pebble any more. Deleting it removes nothing an operator can currently see.
// The retained tail is what preserves the ability to flip back for recent
// history, which is why retain has a floor rather than being honoured as given.
//
// A refusal is not an error: the result carries the census and Refused is set,
// so the caller reports what is blocking. An error return means the census
// itself could not be taken.
func ReclaimMigratedActivity(
	ctx context.Context,
	mig *MigratingActivityStore,
	retain time.Duration,
	dryRun bool,
	onTier ActivityReclaimProgress,
) (ActivityReclaimResult, error) {
	res := ActivityReclaimResult{DryRun: dryRun, PerTier: map[string]ActivityReclaimTier{}}

	if mig == nil {
		res.Refused = true
		res.RefusedReason = "activity store is not the SQLite migration wrapper " +
			"(Pebble-only escape hatch, or SQLite failed to open) — there is no duplicate copy to reclaim"
		return res, nil
	}

	if retain < activityReclaimMinRetain {
		retain = activityReclaimMinRetain
	}
	res.Cutoff = time.Now().UTC().Add(-retain)
	res.ReadSecondary = mig.ReadSecondary()

	// Both backends must be able to count EXACTLY, or this refuses. Falling back
	// to Query's total would be worse than having no census at all: on both
	// backends that number is a pagination probe (Pebble stops at
	// Offset+Limit+1, SQLite caps at sqlActCountCap), so a five-million-row
	// keyspace reports as a single digit — and the parity guard below, which
	// decides whether it is safe to delete, would be comparing two of those.
	primary, secondary := mig.Primary(), mig.Secondary()
	primaryCounter, pok := primary.(ActivityCounter)
	secondaryCounter, sok := secondary.(ActivityCounter)
	if !pok || !sok {
		res.Refused = true
		res.RefusedReason = "an activity backend cannot report an exact row count " +
			"(ActivityCounter unimplemented) — refusing to delete on the strength of an estimate"
		return res, nil
	}

	// Census first, and unconditionally — including on the refusal paths below,
	// which is the whole point of taking it here.
	for _, tier := range actTiers {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		total, err := primaryCounter.CountActivity(ctx, tier, nil)
		if err != nil {
			return res, fmt.Errorf("count primary activity rows (tier=%s): %w", tier, err)
		}
		eligible, err := primaryCounter.CountActivity(ctx, tier, &res.Cutoff)
		if err != nil {
			return res, fmt.Errorf("count eligible activity rows (tier=%s): %w", tier, err)
		}
		secondaryTotal, err := secondaryCounter.CountActivity(ctx, tier, nil)
		if err != nil {
			return res, fmt.Errorf("count secondary activity rows (tier=%s): %w", tier, err)
		}
		res.PrimaryRows += total
		res.EligibleRows += eligible
		res.SecondaryRows += secondaryTotal
		res.PerTier[tier] = ActivityReclaimTier{Eligible: eligible}
	}

	if !res.ReadSecondary {
		res.Refused = true
		res.RefusedReason = fmt.Sprintf(
			"reads are still served from Pebble — the SQLite cutover has not completed "+
				"(Pebble holds %d rows, SQLite %d). Deleting now would destroy live data, not reclaim dead data",
			res.PrimaryRows, res.SecondaryRows)
		return res, nil
	}

	// Defence in depth, not the primary guard: the cutover flip already required
	// verified per-tier parity. A secondary that has since fallen behind the
	// primary means something is wrong with an assumption this rests on, so stop
	// rather than delete on top of it.
	if res.SecondaryRows < res.PrimaryRows {
		res.Refused = true
		res.RefusedReason = fmt.Sprintf(
			"SQLite holds fewer rows (%d) than Pebble (%d) despite reads being flipped — "+
				"refusing to delete the Pebble copy while that is unexplained",
			res.SecondaryRows, res.PrimaryRows)
		return res, nil
	}

	if dryRun {
		return res, nil
	}

	for i, tier := range actTiers {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		deleted, err := primary.Prune(res.Cutoff, tier)
		// Prune reports what it actually deleted even on error, so record the
		// count before deciding what to do with the error.
		t := res.PerTier[tier]
		t.Deleted = deleted
		res.PerTier[tier] = t
		res.RowsDeleted += deleted
		if err != nil {
			return res, fmt.Errorf("prune activity tier %s: %w", tier, err)
		}
		if onTier != nil {
			onTier(tier, i+1, len(actTiers), t)
		}
	}

	return res, nil
}
