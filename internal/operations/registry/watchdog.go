// file: internal/operations/registry/watchdog.go
// version: 1.2.0
// guid: 2b3c4d5e-6f7a-8901-bcde-f01234567890
// last-edited: 2026-06-22

package registry

import (
	"context"
	"fmt"
	"time"
)

const (
	defaultWatchdogInterval      = 30 * time.Second
	defaultProgressTimeout       = 5 * time.Minute
	defaultMinCheckpointTimeout  = 5 * time.Minute // window before uncheckpointed strike
	defaultMinCheckpointInterval = 60 * time.Second
)

// runWatchdog runs every watchdogInterval and inspects all in-flight ops.
// It writes strikes for:
//
//   - uncheckpointed: ResumeRestart ops that haven't checkpointed in
//     ≥5 consecutive minutes (and whose def sets MinCheckpointInterval).
//   - stuck: ops whose last_progress_at is older than def.ProgressTimeout.
//     The run's context is canceled; the worker will set terminal status.
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

// watchdogCycle inspects all running ops once.
func (r *Registry) watchdogCycle() {
	// Collect a snapshot of running handles to avoid holding the lock during DB
	// calls.
	r.mu.RLock()
	handles := make([]*runHandle, 0, len(r.running))
	for _, h := range r.running {
		handles = append(handles, h)
	}
	r.mu.RUnlock()

	now := time.Now().UTC()

	for _, h := range handles {
		r.mu.RLock()
		def, defOK := r.defs[h.defID]
		r.mu.RUnlock()
		if !defOK {
			continue
		}

		// Fetch the DB row to get last_progress_at and last_checkpoint_at.
		row, err := r.store.GetOperationV2(h.id)
		if err != nil || row == nil {
			continue
		}

		// --- Strike: stuck ---
		// Cancel the op's context. The worker detects cancellation and sets
		// terminal status when Run returns; we do NOT set status here.
		progressTimeout := def.ProgressTimeout
		if progressTimeout == 0 {
			progressTimeout = defaultProgressTimeout
		}
		// Prefer the in-memory atomic clock over the DB row: if the reporter has
		// stamped lastProgressAt (non-zero), use it — this avoids false-positive
		// cancellations when UpdateOpProgressV2 is queued behind PebbleDB L0
		// compaction. Fall back to the DB row only when the atomic is unset (zero).
		var lastProgress time.Time
		if ts := h.lastProgressAt.Load(); ts != 0 {
			lastProgress = time.Unix(0, ts).UTC()
		} else if row.LastProgressAt != nil {
			lastProgress = *row.LastProgressAt
		}
		if !lastProgress.IsZero() && now.Sub(lastProgress) > progressTimeout {
			r.writeStrike(h.id, def.ID, def.Plugin, "stuck",
				fmt.Sprintf("no progress for %s (timeout=%s)", now.Sub(lastProgress).Round(time.Second), progressTimeout))
			r.logger.Warn("registry: canceling stuck op", "op_id", h.id, "def_id", def.ID,
				"idle_since", lastProgress)
			h.cancelIfActive()
			continue // don't also check uncheckpointed for the same op
		}

		// --- Strike: uncheckpointed ---
		// Only applies to ResumeRestart ops that have MinCheckpointInterval set
		// (non-zero after applying the default).
		if def.ResumePolicy != ResumeRestart {
			continue
		}
		minInterval := def.MinCheckpointInterval
		if minInterval == 0 {
			minInterval = defaultMinCheckpointInterval
		}

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
		if elapsed >= defaultMinCheckpointTimeout {
			r.writeStrike(h.id, def.ID, def.Plugin, "uncheckpointed",
				fmt.Sprintf("no checkpoint for %s (min_interval=%s)", elapsed.Round(time.Second), minInterval))
		}
	}
}
