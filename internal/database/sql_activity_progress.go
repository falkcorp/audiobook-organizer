// file: internal/database/sql_activity_progress.go
// version: 1.0.0
// guid: a7db9843-e6c9-42ed-bd67-c747b5b5c670
// last-edited: 2026-09-08

// Package database — durable progress for the Pebble → SQLite activity backfill.
//
// WHY THIS EXISTS: the backfill's only persisted state used to be the terminal
// sentinel (ActivitySQLBackfillKey), written after a FULL successful run. A
// restart therefore discarded every hour of work. Production restarted the
// migration from row zero three times on 2026-09-08 (12:07, 12:50, 13:28),
// throwing away ~81 minutes, and the run that survived was ~3 hours deep into a
// single tier when it was measured. Because every insert is idempotent by
// content key, that cost is re-READING ~6.5M JSON rows, not re-writing them.
//
// ⚠️ THE PART THAT IS NOT JUST AN OPTIMISATION — READ BEFORE CHANGING.
//
// A resume cursor on its own would introduce a data-integrity bug, because the
// parity verdict was never durable either. BackfillPebbleActivityToSQL sets
// res.ParityOK = true at the top of EVERY run, flips it to false in memory when
// a tier fails, and gates the sentinel on it. That is fail-closed only BY
// ACCIDENT: a crash means the next boot re-scans and re-verifies every tier from
// row zero, so a failure cannot be forgotten — merely rediscovered.
//
// Add a naive cursor and that accident disappears:
//
//  1. tier `change` fails parity in its first million rows → ParityOK=false (memory only)
//  2. process killed → cursor persisted
//  3. next boot resumes mid-tier with a FRESH ParityOK=true
//  4. the tail verifies clean, remaining tiers verify clean
//  5. sentinel written → SetReadSecondary(true)
//
// …and reads flip to a SQLite copy that was never fully verified. `read_secondary`
// is the ONLY signal meaning "the copy is verified"; that must not be weakened.
//
// So this file persists a per-tier VERDICT, not just a cursor, and two rules keep
// the gate fail-closed:
//
//   - Reinserted is carried across a resume. Fail 5 rows in, get killed, resume,
//     verify a clean tail — the tier still ends `failed`, because the 5 survived.
//   - A `failed` tier is reset to a FULL re-scan on load (cursor and counters
//     cleared). A failed verdict can therefore only ever be cleared by a complete
//     re-scan of that tier, never by a clean tail. It still self-heals on a
//     genuine retry, which a permanently sticky flag would not.
//
// The sentinel gate then reads "every tier carries a clean verdict" — a durable
// census — instead of "this process happened to observe no failure".
package database

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// ActivitySQLBackfillProgressKey holds resumable progress for the Pebble→SQLite
// activity backfill. It sits beside ActivitySQLBackfillKey (the terminal
// sentinel) and is additive: older builds ignore it and simply restart from zero.
const ActivitySQLBackfillProgressKey = "system:backfill:activity_sql_v1_progress"

// activityProgressVersion is the blob's schema version. An unrecognised version
// is treated as "no progress" — a full, fully-verified re-scan, which is always
// the safe reading of state this code cannot interpret.
const activityProgressVersion = 1

// Per-tier lifecycle states.
const (
	// activityTierInProgress: partially scanned (or never started). Cursor may
	// be set; counters accumulate across resumes.
	activityTierInProgress = "in_progress"
	// activityTierClean: fully scanned AND re-presented with zero reinserts.
	// This is the ONLY state that contributes to the sentinel gate.
	activityTierClean = "clean"
	// activityTierFailed: fully scanned, but re-presentation inserted rows, so
	// some copied row did not land. Reset to a full re-scan on the next load.
	activityTierFailed = "failed"
)

// ActivityBackfillTierProgress is one tier's durable progress and verdict.
type ActivityBackfillTierProgress struct {
	State string `json:"state"`
	// Cursor is the last Pebble primary key successfully handed to the batch
	// callback ("act:<tier>:<20-digit nanos>:<ulid>"). Empty means "from the
	// start of the tier".
	Cursor string `json:"cursor,omitempty"`
	// Scanned/Copied accumulate ACROSS resumes, so they describe the tier, not
	// the current attempt.
	Scanned int `json:"scanned"`
	Copied  int `json:"copied"`
	// Reinserted is the parity counter. Carrying it across a resume is what
	// keeps an interrupted failure fail-closed — see the package comment.
	Reinserted int `json:"reinserted"`
}

// ActivityBackfillProgress is the whole durable blob.
type ActivityBackfillProgress struct {
	Version   int                                      `json:"version"`
	Tiers     map[string]*ActivityBackfillTierProgress `json:"tiers"`
	UpdatedAt time.Time                                `json:"updated_at"`
}

// tier returns tier's progress, creating a fresh in_progress entry if absent.
func (p *ActivityBackfillProgress) tier(name string) *ActivityBackfillTierProgress {
	if p.Tiers == nil {
		p.Tiers = make(map[string]*ActivityBackfillTierProgress, len(actTiers))
	}
	st, ok := p.Tiers[name]
	if !ok || st == nil {
		st = &ActivityBackfillTierProgress{State: activityTierInProgress}
		p.Tiers[name] = st
	}
	return st
}

// AllTiersClean reports whether every tier in actTiers carries a clean verdict.
// This is the sentinel gate: a durable census rather than an in-memory flag that
// a resumed process would start over from `true`.
func (p *ActivityBackfillProgress) AllTiersClean() bool {
	for _, tier := range actTiers {
		st, ok := p.Tiers[tier]
		if !ok || st == nil || st.State != activityTierClean {
			return false
		}
	}
	return true
}

// PendingTiers names the tiers that are not yet clean, for logging.
func (p *ActivityBackfillProgress) PendingTiers() []string {
	var pending []string
	for _, tier := range actTiers {
		st, ok := p.Tiers[tier]
		if !ok || st == nil || st.State != activityTierClean {
			pending = append(pending, tier)
		}
	}
	return pending
}

// loadActivityBackfillProgress reads the durable blob and normalises it for a
// new run. It NEVER returns nil and never returns an error: every failure mode
// (missing key, corrupt JSON, unknown version) degrades to "no progress", which
// means a full re-scan and full re-verification. That is the fail-safe reading —
// the expensive answer, never the unverified one — but it is logged loudly
// because silently discarding a recorded parity failure is exactly the outcome
// this file exists to prevent.
//
// Normalisation applies the sticky-but-self-healing rule: a `failed` tier comes
// back as in_progress with its cursor and counters CLEARED, so it is re-scanned
// in full. A clean tail can never clear a failure; only a complete re-scan can.
func loadActivityBackfillProgress(db *pebble.DB) *ActivityBackfillProgress {
	fresh := &ActivityBackfillProgress{
		Version: activityProgressVersion,
		Tiers:   make(map[string]*ActivityBackfillTierProgress, len(actTiers)),
	}

	val, closer, err := db.Get([]byte(ActivitySQLBackfillProgressKey))
	if err != nil {
		return fresh // includes pebble.ErrNotFound: the ordinary first run
	}
	raw := append([]byte(nil), val...)
	closer.Close()

	var p ActivityBackfillProgress
	if err := json.Unmarshal(raw, &p); err != nil {
		slog.Error("[activity-sql-backfill] progress blob is unreadable — restarting the copy from scratch",
			"key", ActivitySQLBackfillProgressKey, "err", err)
		return fresh
	}
	if p.Version != activityProgressVersion {
		slog.Warn("[activity-sql-backfill] progress blob has an unknown version — restarting the copy from scratch",
			"found", p.Version, "expected", activityProgressVersion)
		return fresh
	}
	if p.Tiers == nil {
		p.Tiers = make(map[string]*ActivityBackfillTierProgress, len(actTiers))
	}

	for tier, st := range p.Tiers {
		if st == nil {
			delete(p.Tiers, tier)
			continue
		}
		if st.State == activityTierFailed {
			slog.Warn("[activity-sql-backfill] tier previously FAILED parity — re-scanning it in full",
				"tier", tier, "prior_reinserted", st.Reinserted, "prior_scanned", st.Scanned)
			st.State = activityTierInProgress
			st.Cursor = ""
			st.Scanned, st.Copied, st.Reinserted = 0, 0, 0
		}
	}
	return &p
}

// saveActivityBackfillProgress persists the blob.
//
// sync=false (cursor advance) vs sync=true (verdict transition) is a deliberate
// cost/safety split: losing a cursor write costs one re-read of an idempotent
// batch, whereas losing a verdict is a correctness bug. Cursors are written every
// batch — roughly every two seconds of work at production throughput — because a
// coarser throttle discards minutes of scanning to save a single small write.
func saveActivityBackfillProgress(db *pebble.DB, p *ActivityBackfillProgress, sync bool) error {
	p.Version = activityProgressVersion
	p.UpdatedAt = time.Now().UTC()
	blob, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("activity-sql-backfill: marshal progress: %w", err)
	}
	opts := pebble.NoSync
	if sync {
		opts = pebble.Sync
	}
	if err := db.Set([]byte(ActivitySQLBackfillProgressKey), blob, opts); err != nil {
		return fmt.Errorf("activity-sql-backfill: write progress: %w", err)
	}
	return nil
}

// clearActivityBackfillProgress removes the blob. Called once the sentinel is
// written: the migration is done, so the resume state is dead weight and leaving
// it behind would only confuse a future reader.
func clearActivityBackfillProgress(db *pebble.DB) {
	if err := db.Delete([]byte(ActivitySQLBackfillProgressKey), pebble.Sync); err != nil {
		slog.Warn("[activity-sql-backfill] could not delete progress blob after completion",
			"key", ActivitySQLBackfillProgressKey, "err", err)
	}
}

// resumeCursorFor validates a stored cursor against the tier it claims to belong
// to and returns the key to resume strictly after, or nil to start from the top.
//
// The validation is load-bearing, not defensive noise. An out-of-range cursor
// would make streamTierEntries yield ZERO rows, the tier would finish with
// reinserted == 0, and it would be marked CLEAN without a single row having been
// verified — a corrupt cursor would fabricate the exact verdict that flips
// production reads. Requiring the tier's own key prefix makes that unrepresentable.
func resumeCursorFor(tier, cursor string) []byte {
	if cursor == "" {
		return nil
	}
	key := []byte(cursor)
	prefix := pactPrimaryPrefix(tier)
	if !bytes.HasPrefix(key, prefix) {
		slog.Warn("[activity-sql-backfill] stored cursor does not belong to this tier — restarting the tier",
			"tier", tier, "cursor", cursor)
		return nil
	}
	return key
}
