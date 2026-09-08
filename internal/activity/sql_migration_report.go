// file: internal/activity/sql_migration_report.go
// version: 1.0.0
// guid: 6d19f2b4-3c58-4e07-9a1d-8f4b0c73e2a6
// last-edited: 2026-09-08

// Package activity — user-visible status for the Pebble → SQLite activity cutover.
//
// The migration runs for HOURS (production spent 4h10m on tier 1 of 7 before a
// restart) inside sqlMigrationStarter's own goroutine, and until this file existed
// it reported only to slog. From the UI it was indistinguishable from nothing
// happening, and "why have reads not flipped to SQLite yet?" had no answer short of
// journalctl.
//
// WHY THIS WRITES AN OPERATION ROW INSTEAD OF BECOMING AN OPERATION. The obvious
// design — register an OperationDef and run the backfill through the registry —
// is unsafe here. Registry.Shutdown ends with a goroutineWG.Wait() behind a
// hardcoded 2s escape; past it the caller closes Pebble under whatever is still
// reading, and pebble_activity_store.go has no recoverPebbleClosed guard, so an
// overrun is a process PANIC rather than an error. The backfill cannot honour a 2s
// deadline: per sqlBackfillProgressEvery's own note a batch of 500 iTunes rows
// carries ~9.6 MB of details EACH — gigabytes to compress and insert, twice (copy
// then parity) — and a single row's insert is not interruptible at all. That is why
// sqlMigrationStarter.Stop joins UNBOUNDED, and why that must not be "fixed".
//
// So the work stays where it is and only the REPORTING is registry-shaped: these
// are plain store writes with no worker and no goroutineWG enrollment. The
// migration keeps its unbounded join; the user gets a row in the operations UI.
//
// NO OperationDef IS REGISTERED, deliberately. Registry.ActiveDefs() has no
// allowlist and feeds GET /api/v1/op-defs, so registering one would put a Run
// button on the migration — a way to launch a SECOND concurrent backfill over the
// same keyspace. The cost is that resumeAfterStartup finds no def for a row left by
// a killed process and drops it ("unknown def at startup"), which is the correct
// terminal state: that run WAS interrupted and the registry must not resume it —
// the starter starts a fresh one.
package activity

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/oklog/ulid/v2"
)

// migrationOpDefID / migrationOpPlugin label the row in the operations UI. The
// def id is intentionally NOT registered with the operations registry (see the
// package comment); it exists so the row has a stable, greppable identity.
const (
	migrationOpDefID  = "activity.sql-migration"
	migrationOpPlugin = "activity"
)

// migrationOpsRecorder is the slice of the ops-v2 store this reporter needs.
//
// Narrow on purpose: database.Store is ~398 methods and this needs three. See the
// interface-narrowing note in CLAUDE.md — a wide interface here would couple the
// activity package to the entire store for the sake of status reporting.
type migrationOpsRecorder interface {
	InsertOperationV2(row database.OperationV2Row) error
	UpdateOpProgressV2(id string, current, total int, message string) error
	UpdateOperationV2Status(id, status string, startedAt, completedAt *time.Time, errMsg *string) error
}

// migrationOpReporter mirrors backfill progress onto an operations_v2 row.
//
// NOT SAFE FOR CONCURRENT USE, and it does not need to be: begin, observe and
// finish are all called from the single backfill goroutine, and observe runs
// inline on it as the progress callback.
//
// ops may be nil (no ops store wired), in which case every method is a no-op and
// the migration behaves exactly as it did before this file.
type migrationOpReporter struct {
	ops  migrationOpsRecorder
	opID string

	// reasserted guards the one-shot status re-assert in observe. See the comment
	// there — it exists to survive a startup ordering race, not to retry.
	reasserted bool

	// finishedCopied is the sum of copied counts for tiers that have COMPLETED,
	// and lastCopied is that plus the current tier's running count.
	//
	// The accumulation is the point. ActivityBackfillProgressUpdate.Copied is
	// per-TIER: it restarts at each tier's checkpoint, so reporting it directly
	// would make the count jump backwards every time a tier finished — 7 times
	// over a multi-hour run, each looking like the migration had lost work.
	// lastCopied is also what finish writes back, because UpdateOpProgressV2
	// writes ProgressCurrent unconditionally and a 0 there would erase the total
	// the run just achieved.
	finishedCopied int
	lastCopied     int
}

// bestEffort is the ONLY way this file writes to the store.
//
// A status write must never be able to kill a migration that is hours deep, so
// every error is logged and swallowed. Stated as a rule in a doc comment this
// would erode — some later edit adds `if err != nil { return err }` to the
// callback path and one transient Pebble error aborts the copy. Routed through a
// helper that has no error return, it cannot.
func bestEffort(what, opID string, err error) {
	if err == nil {
		return
	}
	slog.Warn("[activity] could not update migration status row (migration continues)",
		"step", what, "op_id", opID, "err", err)
}

// begin inserts the operation row. It is called when work actually starts — after
// the settle delay — so a shutdown during the delay leaves no phantom operation.
func (r *migrationOpReporter) begin(now time.Time) {
	if r == nil || r.ops == nil {
		return
	}
	r.opID = ulid.Make().String()
	bestEffort("insert", r.opID, r.ops.InsertOperationV2(database.OperationV2Row{
		ID:              r.opID,
		DefID:           migrationOpDefID,
		Plugin:          migrationOpPlugin,
		Status:          "running",
		Params:          "{}",
		ProgressMessage: "starting activity log migration",
		QueuedAt:        now,
		StartedAt:       &now,
	}))
}

// observe records one progress update. It is the backfill's onProgress callback,
// so it runs inline on the copy goroutine and must stay cheap.
//
// ProgressTotal is deliberately left at 0 — there is no denominator. Counting one
// would cost a full key walk per tier, and the count moves under itself anyway
// because the live dual-write keeps adding rows while the scan runs. The
// operations UI already renders total==0 as an indeterminate bar (OperationsIndicator
// .tsx) rather than a percentage, which is the truthful rendering here.
func (r *migrationOpReporter) observe(u database.ActivityBackfillProgressUpdate) {
	if r == nil || r.ops == nil || r.opID == "" {
		return
	}

	// One-shot re-assert of `running`. Container.Start (which starts this) runs at
	// server_lifecycle.go:82 and Registry.Start → resumeAfterStartup at line 144,
	// so the resume sweep normally happens seconds into boot — long before the 60s
	// settle delay inserts this row. That ordering is timing, not a guarantee: on a
	// slow boot the sweep could land AFTER the insert, find a row whose def is not
	// registered, and flip a genuinely-running migration to interrupted_dropped.
	// Re-asserting once on the first progress update makes the outcome independent
	// of that ordering instead of resting on the delay being long enough.
	if !r.reasserted {
		r.reasserted = true
		started := time.Now().UTC()
		bestEffort("reassert-running", r.opID,
			r.ops.UpdateOperationV2Status(r.opID, "running", &started, nil, nil))
	}

	msg := fmt.Sprintf("tier %d/%d %q: %s scanned, %s copied",
		u.TierIndex, u.TiersTotal, u.Tier,
		humanCount(u.Scanned), humanCount(u.Copied))
	if u.Done {
		msg = fmt.Sprintf("tier %d/%d %q complete (%s): %s scanned, %s copied",
			u.TierIndex, u.TiersTotal, u.Tier, u.Verdict,
			humanCount(u.Scanned), humanCount(u.Copied))
	}
	r.lastCopied = r.finishedCopied + u.Copied
	if u.Done {
		r.finishedCopied += u.Copied
	}
	bestEffort("progress", r.opID, r.ops.UpdateOpProgressV2(r.opID, r.lastCopied, 0, msg))
}

// finish closes the row out in a terminal state.
//
// The status vocabulary is the one pebble_store_ops_v2.go's terminal-status switch
// recognises: completed, failed, canceled, interrupted_dropped.
//
// This is also the durable record of a FAILED migration, which nothing else keeps:
// loadActivityBackfillProgress rewrites a `failed` tier verdict back to in_progress
// on the next boot (correctly — a failed tier must be re-verified in full), so after
// a restart the checkpoint blob can no longer answer "why didn't it flip?". This row
// is not touched by that loader, so the failure and its message survive.
func (r *migrationOpReporter) finish(status, message string, cause error) {
	if r == nil || r.ops == nil || r.opID == "" {
		return
	}
	done := time.Now().UTC()
	var errMsg *string
	if cause != nil {
		s := cause.Error()
		errMsg = &s
	} else if status != "completed" {
		s := message
		errMsg = &s
	}
	// Keep the count: UpdateOpProgressV2 writes ProgressCurrent unconditionally,
	// so a 0 here would erase the total the run just achieved.
	bestEffort("progress-final", r.opID, r.ops.UpdateOpProgressV2(r.opID, r.lastCopied, 0, message))
	bestEffort("finish", r.opID,
		r.ops.UpdateOperationV2Status(r.opID, status, nil, &done, errMsg))
}

// humanCount renders large row counts readably — these run to millions, and
// "7581500" in a status line is harder to read at a glance than "7.58M".
func humanCount(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.2fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}
