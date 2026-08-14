// file: internal/maintenance/jobs/retention_and_hygiene.go
// version: 1.3.0
// guid: e7c9d4a2-f1b3-49a8-8c4f-7d2e5a1f3c9e
// last-edited: 2026-08-14

package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
)

func init() { maintenance.Register(&retentionAndHygieneJob{}) }

type retentionAndHygieneJob struct{}

func (j *retentionAndHygieneJob) ID() string       { return "retention-and-hygiene" }
func (j *retentionAndHygieneJob) Name() string     { return "Retention & Dead-Prefix Hygiene" }
func (j *retentionAndHygieneJob) Category() string { return "maintenance" }
func (j *retentionAndHygieneJob) DefaultParams() any {
	return struct {
		DryRun bool `json:"dry_run"`
	}{DryRun: true}
}
func (j *retentionAndHygieneJob) Description() string {
	return "Delete stale operation logs, purge old operation records, and clean dead prefixes (one-off book:series:, book:author:)"
}
func (j *retentionAndHygieneJob) CanResume() bool { return true }

func (j *retentionAndHygieneJob) Run(ctx context.Context, store database.Store, reporter maintenance.ProgressReporter, dryRun bool) error {
	slog.Info("retention-and-hygiene job starting", "dry_run", dryRun)

	// (1) Operation/OperationLog retention sweep: delete older than N days (default 90).
	operationRetentionDays := config.AppConfig.OperationLogRetentionDays
	if operationRetentionDays <= 0 {
		operationRetentionDays = 90
	}
	cutoffTime := time.Now().AddDate(0, 0, -operationRetentionDays)

	slog.Info("retention-and-hygiene: operation log retention",
		"retention_days", operationRetentionDays,
		"cutoff_time", cutoffTime)

	operationsCut, err := deleteOldOperations(ctx, store, cutoffTime, dryRun)
	if err != nil {
		slog.Error("retention-and-hygiene: operation deletion failed", "error", err)
		return fmt.Errorf("operation retention sweep: %w", err)
	}
	slog.Info("retention-and-hygiene: operations processed",
		"count", operationsCut, "dry_run", dryRun)

	// (2) Dead-prefix sweep: one-off cleanup of residual book:series: and book:author: keys.
	// These prefix indexes were removed in Task 3.4 (replaced by memdb queries) but may
	// still exist in production databases that pre-date the removal.
	// Guard with versioned flag to prevent re-running on every maintenance cycle.
	flagName := "dead_prefix_sweep_v1_done"
	done, err := isDeadPrefixSweepDone(store, flagName)
	if err != nil {
		slog.Warn("retention-and-hygiene: dead-prefix flag check failed", "error", err)
		// Continue anyway — don't fail the whole job for a flag check.
	} else if done {
		slog.Info("retention-and-hygiene: dead-prefix sweep already completed (flag set)")
	} else {
		prefixCount, sweepErr := deleteDeadPrefixes(ctx, store, dryRun)
		if sweepErr != nil {
			slog.Error("retention-and-hygiene: dead-prefix deletion failed", "error", sweepErr)
			return fmt.Errorf("dead-prefix sweep: %w", sweepErr)
		}
		slog.Info("retention-and-hygiene: dead prefixes deleted",
			"count", prefixCount, "dry_run", dryRun)

		// Mark the sweep as done only after a real (non-dry-run) execution that
		// succeeded. Setting the flag on a dry-run would suppress the actual
		// deletion on the next real run — defeating the purpose of the guard.
		if !dryRun {
			if err := setDeadPrefixSweepDone(store, flagName); err != nil {
				slog.Warn("retention-and-hygiene: failed to set completion flag", "error", err)
				// Don't fail the job — the flag is just a guard to avoid redundant runs.
			}
		}
	}

	// (3) opstate sweep: clear opstate:<id> / opstate:<id>:params blobs whose
	// owning operation is finished or gone. Only 2 of the 34 maintenance jobs
	// call operations.ClearState on completion, so these keys otherwise
	// accumulate forever. Runs AFTER phase (1) so state orphaned by this very
	// run's operation-retention pass is caught in the same run.
	stateCut, err := deleteStaleOperationState(ctx, store, dryRun)
	if err != nil {
		slog.Error("retention-and-hygiene: opstate sweep failed", "error", err)
		return fmt.Errorf("opstate sweep: %w", err)
	}
	slog.Info("retention-and-hygiene: stale operation state cleared",
		"count", stateCut, "dry_run", dryRun)

	slog.Info("retention-and-hygiene job complete",
		"operations_deleted", operationsCut, "opstate_cleared", stateCut, "dry_run", dryRun)
	return nil
}

// deleteOldOperations deletes operation records (and their associated log lines) whose
// CreatedAt is strictly before cutoffTime.
//
// In dry-run mode it counts matching records without modifying the store so callers can
// preview the impact. In non-dry-run mode it calls DeleteOperationWithLogs for each
// eligible operation, which atomically removes the operation key and all operationlog:*
// entries in a single Pebble batch — avoiding orphaned log lines.
//
// The scan is done in two phases: first collect all eligible IDs, then delete them.
// This avoids pagination skew caused by row deletions shifting the sorted index
// mid-scan (PebbleStore.ListOperations reads the entire prefix into memory and slices
// by offset, so deleting during iteration would cause the same offset to advance past
// fewer rows, silently skipping records).
//
// Phase 1 takes the whole listing in ONE call, which is what "a single pass" has
// always claimed to mean here. It used to walk the listing in 500-row pages, and
// because ListOperations reads and sorts the entire "operation:" prefix on every
// call, each page paid for a full scan: N/500 scans of N rows. At the 10,163
// operations production held when this was written that was 21 full scans, and it
// grows quadratically.
//
// Paging also had a correctness cost that the two-phase design does not cover.
// The listing is newest-first, so an operation created by anything else while
// phase 1 was running shifted every existing row to a HIGHER index; reading a
// fixed, increasing offset sequence over a right-shifting slice re-read rows and
// put the same ID in toDelete twice. Deleting an already-deleted key is a no-op,
// so nothing was lost, but the count returned below counted it twice and the job
// then reported more deletions than it made. One call takes one snapshot, so
// neither the amplification nor the double-count is reachable.
func deleteOldOperations(ctx context.Context, store database.Store, cutoffTime time.Time, dryRun bool) (int, error) {
	slog.Info("deleteOldOperations: scanning operations", "cutoff_time", cutoffTime, "dry_run", dryRun)

	if ctx.Err() != nil {
		return 0, ctx.Err()
	}

	// Phase 1: collect all eligible operation IDs in a single pass. limit <= 0
	// means "no limit" — see PebbleStore.ListOperations.
	ops, _, err := store.ListOperations(0, 0)
	if err != nil {
		return 0, fmt.Errorf("list operations: %w", err)
	}
	var toDelete []string
	for _, op := range ops {
		if op.CreatedAt.Before(cutoffTime) {
			toDelete = append(toDelete, op.ID)
		}
	}

	slog.Info("deleteOldOperations: scan complete",
		"eligible", len(toDelete), "dry_run", dryRun)

	if dryRun {
		// Dry-run: report count only, touch nothing.
		for _, id := range toDelete {
			slog.Debug("dry-run: would delete operation", "op_id", id)
		}
		return len(toDelete), nil
	}

	// Phase 2: delete eligible operations and their associated log lines.
	count := 0
	for _, id := range toDelete {
		if ctx.Err() != nil {
			return count, ctx.Err()
		}
		slog.Debug("deleting operation with logs", "op_id", id)
		if err := store.DeleteOperationWithLogs(id); err != nil {
			return count, fmt.Errorf("delete operation %s: %w", id, err)
		}
		count++
	}
	return count, nil
}

// deleteDeadPrefixes deletes residual book:series: and book:author: keys that were
// written by the old secondary-index layer (removed in Task 3.4).
//
// Zero live code reads these prefixes — a grep audit confirmed no reader outside this
// file references them (see PR description). Removing them reclaims disk space and
// eliminates confusion for future readers of the key schema.
//
// Returns the total number of keys deleted across both prefixes.
func deleteDeadPrefixes(ctx context.Context, store database.Store, dryRun bool) (int, error) {
	deadPrefixes := []string{"book:series:", "book:author:"}
	slog.Info("deleteDeadPrefixes: starting sweep",
		"prefixes", deadPrefixes, "dry_run", dryRun)

	total := 0
	for _, prefix := range deadPrefixes {
		if ctx.Err() != nil {
			return total, ctx.Err()
		}

		pairs, err := store.ScanPrefix(prefix)
		if err != nil {
			return total, fmt.Errorf("scan prefix %q: %w", prefix, err)
		}

		slog.Info("deleteDeadPrefixes: scanned prefix",
			"prefix", prefix, "key_count", len(pairs), "dry_run", dryRun)

		if dryRun {
			total += len(pairs)
			continue
		}

		for _, pair := range pairs {
			if ctx.Err() != nil {
				return total, ctx.Err()
			}
			if err := store.DeleteRaw(pair.Key); err != nil {
				return total, fmt.Errorf("delete key %q: %w", pair.Key, err)
			}
			total++
		}
	}

	return total, nil
}

// terminalOpStatuses are the operation statuses from which the resume path can
// never pick an operation back up: GetInterruptedOperations selects only
// "running", "queued", and "interrupted", so state for anything here is dead.
// "canceled" and "cancelled" are both listed because both spellings exist in
// the codebase's status literals.
var terminalOpStatuses = map[string]bool{
	"completed": true,
	"failed":    true,
	"canceled":  true,
	"cancelled": true,
}

// deleteStaleOperationState removes opstate:<id> and opstate:<id>:params blobs
// whose owning operation is gone or terminal. runMaintenanceJob persists a
// params blob per run so a restart can resume faithfully, but only 2 of the 34
// jobs clear it on completion — the rest leak one pair of keys per run,
// forever (small but unbounded growth).
//
// The decision is per-operation, and it deliberately fails toward KEEPING:
//   - op record missing            -> state is orphaned (phase (1) may have just
//     deleted the op)              -> delete
//   - status in terminalOpStatuses -> resume can never fire -> delete
//   - anything else — running, queued, interrupted, or a status this map does
//     not know — -> KEEP. Deleting on "status not recognized" would silently
//     break resume for any status value added later; an unknown status keeps
//     its state until the operation itself ages out of retention.
//
// Returns the number of operations whose state was (or in dry-run, would be)
// cleared — counting operations, not raw keys, so the dry-run count matches
// the subsequent real run.
func deleteStaleOperationState(ctx context.Context, store database.Store, dryRun bool) (int, error) {
	pairs, err := store.ScanPrefix("opstate:")
	if err != nil {
		return 0, fmt.Errorf("scan opstate prefix: %w", err)
	}

	// Collapse opstate:<id> and opstate:<id>:params to one entry per op.
	// Op IDs are ULIDs (no colons), so trimming the suffix is unambiguous.
	ids := make(map[string]struct{}, len(pairs))
	for _, pair := range pairs {
		id := strings.TrimPrefix(pair.Key, "opstate:")
		id = strings.TrimSuffix(id, ":params")
		ids[id] = struct{}{}
	}

	slog.Info("deleteStaleOperationState: scanned opstate prefix",
		"keys", len(pairs), "operations", len(ids), "dry_run", dryRun)

	count := 0
	for id := range ids {
		if ctx.Err() != nil {
			return count, ctx.Err()
		}
		op, err := store.GetOperationByID(id)
		if err != nil {
			return count, fmt.Errorf("get operation %s: %w", id, err)
		}
		if op != nil && !terminalOpStatuses[op.Status] {
			continue // live or unknown status — resume may still need this state
		}
		if dryRun {
			count++
			continue
		}
		// DeleteOperationState removes both opstate:<id> and opstate:<id>:params
		// in one batch.
		if err := store.DeleteOperationState(id); err != nil {
			return count, fmt.Errorf("delete operation state %s: %w", id, err)
		}
		count++
	}
	return count, nil
}

// isDeadPrefixSweepDone checks if the dead-prefix sweep completion flag is set.
// A missing setting (ErrSettingNotFound) is treated as "not done" (returns false, nil)
// because the flag is created only after the first successful real run.
func isDeadPrefixSweepDone(store database.Store, flagName string) (bool, error) {
	setting, err := store.GetSetting(flagName)
	if err != nil {
		if errors.Is(err, database.ErrSettingNotFound) {
			return false, nil // Flag absent → sweep has not run yet.
		}
		return false, err
	}
	if setting == nil {
		return false, nil
	}
	return setting.Value == "true", nil
}

// setDeadPrefixSweepDone sets the completion flag.
func setDeadPrefixSweepDone(store database.Store, flagName string) error {
	return store.SetSetting(flagName, "true", "boolean", false)
}
