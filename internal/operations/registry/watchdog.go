// file: internal/operations/registry/watchdog.go
// version: 1.11.0
// guid: 2b3c4d5e-6f7a-8901-bcde-f01234567890
// last-edited: 2026-10-11

package registry

import (
	"context"
	"fmt"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

const (
	defaultWatchdogInterval     = 30 * time.Second
	defaultProgressTimeout      = 5 * time.Minute
	defaultMinCheckpointTimeout = 5 * time.Minute // window before uncheckpointed strike
)

// effectiveProgressTimeout is the silence budget the watchdog allows one run
// of def: def.ProgressTimeout, or defaultProgressTimeout when the def leaves it
// 0. The ops.progress_timeout gauge (inflightByDef) reports the same value, so
// the stall alert and the watchdog cannot disagree about a def's budget.
func effectiveProgressTimeout(def OperationDef) time.Duration {
	if def.ProgressTimeout == 0 {
		return defaultProgressTimeout
	}
	return def.ProgressTimeout
}

// runWatchdog runs every watchdogInterval and inspects all in-flight ops.
// It writes strikes for:
//
//   - uncheckpointed: ResumeRestart ops that haven't checkpointed in
//     ≥5 consecutive minutes (and whose def sets MinCheckpointInterval).
//   - stuck: ops that reported progress at least once and then went quiet for
//     longer than def.ProgressTimeout. The run's context is canceled; the
//     worker will set terminal status.
//   - never_reported: ops that have not reported progress even once since
//     StartedAt. Same cancellation, different diagnosis: this usually means the
//     op is not wired to its reporter, not that its work is wedged.
//
// Infinite-restart detection happens in worker.go at run start time, not
// here, because it needs to be enforced before the run begins.
func (r *Registry) runWatchdog(ctx context.Context) {
	interval := r.watchdogInterval
	if interval == 0 {
		interval = defaultWatchdogInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	r.logger.Info("registry: watchdog started", "interval", interval)

	for {
		select {
		case <-ctx.Done():
			r.logger.Info("registry: watchdog stopping")
			return
		case <-ticker.C:
			r.watchdogCycle()
		}
	}
}

// Zombie warning cadence (owner decision D6): a run whose goroutine outlived
// the abandon grace is logged at WARN once it is zombieWarnAfter old, then
// again every zombieWarnEvery while it lives.
const (
	zombieWarnAfter = 10 * time.Minute
	zombieWarnEvery = time.Minute
)

// zombieSweep logs the long-lived zombies. It runs on the watchdog goroutine
// (no goroutine of its own) and measures against the injected liveness clock.
func (r *Registry) zombieSweep(now time.Time) {
	r.mu.RLock()
	var zombies []*runHandle
	for _, h := range r.running {
		if h != nil && h.zombie.Load() {
			zombies = append(zombies, h)
		}
	}
	r.mu.RUnlock()
	for _, h := range zombies {
		since := h.zombieSince.Load()
		if since == 0 {
			continue
		}
		age := now.Sub(time.Unix(0, since))
		if age < zombieWarnAfter {
			continue
		}
		if last := h.zombieWarnedAt.Load(); last != 0 && now.Sub(time.Unix(0, last)) < zombieWarnEvery {
			continue
		}
		h.zombieWarnedAt.Store(now.UnixNano())
		r.logger.Warn("registry: zombie op goroutine still alive; its concurrency key stays held until it exits",
			"op_id", logger.SanitizeLogValue(h.id),
			"def_id", logger.SanitizeLogValue(h.defID),
			"key", logger.SanitizeLogValue(h.concurrencyKey),
			"age", age.Round(time.Second))
	}
}

// watchdogCycle inspects all running ops once.
func (r *Registry) watchdogCycle() {
	r.zombieSweep(r.livenessClock())
	// Collect a snapshot of running handles to avoid holding the lock during DB
	// calls.
	r.mu.RLock()
	handles := make([]*runHandle, 0, len(r.running))
	for _, h := range r.running {
		handles = append(handles, h)
	}
	r.mu.RUnlock()

	now := r.livenessClock().UTC()

	for _, h := range handles {
		// A zombie was already stopped (fence revoked, context canceled) and
		// its row reads "running" until its goroutine returns (v3 stopping,
		// 05-PR4). Striking or canceling it again does nothing useful;
		// zombieSweep above is what watches it.
		if h.zombie.Load() {
			continue
		}
		def, defOK := r.lookupDef(h.defID)
		if !defOK {
			continue
		}

		// Fetch the DB row to get last_progress_at and last_checkpoint_at.
		row, err := r.store.GetOperationV2(h.id)
		if err != nil || row == nil {
			continue
		}

		// Skip rows that are not actually running yet. A handle can exist for a
		// row still marked "queued": the dispatcher inserts a stub handle the
		// instant an op is claimed, but the worker only marks the row "running"
		// on pickup from the nextRun channel. While an op sits in that channel
		// (e.g. saturated workers), its row may carry stale StartedAt /
		// LastProgressAt values from a previous resumed run — inspecting those
		// here would write spurious stuck strikes.
		//
		if row.Status != "running" {
			continue
		}

		// --- Strike: stuck ---
		// Cancel the op's context. The worker detects cancellation and sets
		// terminal status when Run returns; we do NOT set status here.
		progressTimeout := effectiveProgressTimeout(def)
		// Prefer the in-memory atomic clock over the DB row: if the reporter has
		// stamped lastProgressAt (non-zero), use it — this avoids false-positive
		// cancellations when UpdateOpProgressV2 is queued behind PebbleDB L0
		// compaction. Fall back to the DB row only when the atomic is unset (zero).
		var lastProgress time.Time
		// everReported separates two states that produced an identical strike
		// until 2026-08-16, and which call for opposite responses. An op that
		// reported and then went quiet is stuck: go and see what it is blocked
		// on. An op that has never reported once is, far more often, not wired
		// to its reporter at all — the work may be perfectly healthy while the
		// only channel that could say so is missing. Raising ProgressTimeout
		// "fixes" the first and merely hides the second, which is precisely what
		// three separate workarounds did before the LoggerFromReporter stub was
		// found (see internal/operations/progress.go).
		everReported := false
		if ts := h.lastProgressAt.Load(); ts != 0 {
			lastProgress = time.Unix(0, ts).UTC()
			everReported = true
		} else if ts := h.attemptStartedAt.Load(); ts != 0 {
			// A persisted LastProgressAt can describe a PREVIOUS process attempt
			// after ResumeRestart. It is useful history but not current liveness:
			// reading it here killed a newly resumed scan on the first watchdog
			// tick as "idle for hours". This attempt has not reported yet, so keep
			// everReported false and count from its worker-start baseline.
			lastProgress = time.Unix(0, ts).UTC()
		} else if row.LastProgressAt != nil {
			lastProgress = *row.LastProgressAt
			everReported = true
		} else if row.StartedAt != nil {
			// R-2: no progress has EVER been reported (atomic unset, DB row nil).
			// Marking an op running stamps StartedAt but never LastProgressAt, so
			// without this fallback an op that hangs BEFORE its first
			// UpdateProgress call was undetectable — the exact "silent for hours"
			// incident class. Fall back to StartedAt so a hang-from-birth op
			// accrues stuck time from the moment it started.
			lastProgress = *row.StartedAt
		}
		if !lastProgress.IsZero() && now.Sub(lastProgress) > progressTimeout {
			idle := now.Sub(lastProgress).Round(time.Second)
			if everReported {
				r.writeStrike(h.id, def.ID, def.Plugin, "stuck",
					fmt.Sprintf("no progress for %s (timeout=%s)", idle, progressTimeout))
				r.logger.Warn("registry: canceling stuck op", "op_id", h.id, "def_id", def.ID,
					"idle_since", lastProgress)
			} else {
				// ERROR, not WARN: a healthy op being killed because nothing
				// can hear it is a defect in the operation's plumbing, and it
				// will recur on every single run until someone changes code.
				r.writeStrike(h.id, def.ID, def.Plugin, "never_reported",
					fmt.Sprintf("never reported progress in the %s since it started (timeout=%s); "+
						"the op is most likely not wired to its reporter", idle, progressTimeout))
				r.logger.Error("registry: canceling op that never reported progress",
					"op_id", h.id, "def_id", def.ID, "started_at", lastProgress, "ran_for", idle,
					"hint", "Run never called reporter.UpdateProgress — run the work through "+
						"registry.RunItems, or report directly; raising ProgressTimeout only hides this")
			}
			// A watchdog kill is a deliberate decision, not an interruption, and
			// must not be resumed: the op is stuck or unwired, so resuming it
			// reproduces the same stall on the next boot -- a stuck -> cancel ->
			// resume -> stuck loop that never converges and never surfaces.
			h.userCanceled.Store(true)
			h.cancelIfActive()
			continue // don't also check uncheckpointed for the same op
		}

		// --- Strike: uncheckpointed ---
		// Gate on the DECLARATION, not on ResumePolicy. This implements the rule
		// the comment here has always stated and the code never did: it said the
		// strike applies to ops "that have MinCheckpointInterval set", then
		// supplied a default whenever it was unset, so the condition could never
		// be false and every ResumeRestart op was eligible.
		//
		// A strike is a declaration-vs-behaviour check, so it is only meaningful
		// against a def that promised a checkpoint cadence and missed it.
		// ResumePolicy is the wrong proxy, because ResumeRestart also covers ops
		// whose resume anchor is their OPERATION ID rather than a checkpoint blob
		// — they resume correctly by re-entering Run and filtering against
		// GetOperationResults(id). Those cannot checkpoint even in principle:
		// maintenance.ProgressReporter (internal/maintenance/job.go) declares only
		// SetTotal/Increment/Log, with no Checkpoint method to call. So the strike
		// fired once per threshold on every run of all nine maintenance
		// ResumeRestart jobs and of metadata.candidate-fetch, forever, into a table
		// that has an InsertOpStrikeV2 and no reader anywhere.
		//
		// This SHARPENS the strike rather than silencing it: a def that DOES
		// declare MinCheckpointInterval and then never checkpoints still strikes,
		// which is precisely the defect the strike exists to report.
		if def.ResumePolicy != ResumeRestart || def.MinCheckpointInterval == 0 {
			continue
		}
		minInterval := def.MinCheckpointInterval
		// C-4: the strike threshold honors the def's own MinCheckpointInterval,
		// with defaultMinCheckpointTimeout as the floor. The old code compared
		// against the 5m constant alone, striking long-interval defs spuriously.
		threshold := max(minInterval, defaultMinCheckpointTimeout)

		// Reference time: last_checkpoint_at if set, else started_at.
		var refTime *time.Time
		if row.LastCheckpointAt != nil {
			refTime = row.LastCheckpointAt
		} else if row.StartedAt != nil {
			refTime = row.StartedAt
		}
		if refTime == nil {
			continue
		}

		elapsed := now.Sub(*refTime)
		if elapsed < threshold {
			continue
		}
		// C-4: dedupe. The watchdog cycles every ~30s; without this gate a
		// persistently uncheckpointed op re-inserted a strike row every cycle.
		// Write at most one strike per threshold interval per op.
		if last := h.lastUncheckpointedStrike.Load(); last != 0 && now.Sub(time.Unix(0, last).UTC()) < threshold {
			continue
		}
		h.lastUncheckpointedStrike.Store(now.UnixNano())
		r.writeStrike(h.id, def.ID, def.Plugin, "uncheckpointed",
			fmt.Sprintf("no checkpoint for %s (threshold=%s, min_interval=%s)", elapsed.Round(time.Second), threshold, minInterval))
	}
}
