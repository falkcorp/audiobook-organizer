// file: internal/logger/retention.go
// version: 1.2.0
// guid: 7c9e2f14-3a5b-4d8c-b1e6-9f2047a85c30
// last-edited: 2026-10-11

package logger

import (
	"context"
	"fmt"
	"time"
)

// RetentionStore is the subset of the store needed for log pruning. Each method
// deletes in bounded chunks, stops between chunks or mid-scan when ctx is
// cancelled (returning ctx.Err() and the rows already deleted), and calls
// onProgress, if non-nil, with running (scanned, deleted) totals after each
// committed chunk and every few thousand scanned rows. The callback is spelled
// func(scanned, deleted int) rather than database.PruneProgressFunc because
// database imports this package; the latter is an alias of the former, so the
// two are the same type.
//
// The undo journal (opchange: rows, PruneOperationChanges) is deliberately NOT
// here. The log purge never touches it: the approved storage design
// (docs/design/2026-10-03-storage-efficiency-design.md section 7) gives undo
// its own window so no log setting can shorten undo. See todo.d
// UNDO-WINDOW-PRUNE.
type RetentionStore interface {
	PruneOperationLogs(ctx context.Context, olderThan time.Time, onProgress func(scanned, deleted int)) (int, error)
	PruneSystemActivityLogs(ctx context.Context, olderThan time.Time, onProgress func(scanned, deleted int)) (int, error)
}

// PruneProgressFunc reports pruning progress: which kind of record is being
// pruned ("operation logs" or "system logs"), how many rows of that kind have
// been scanned so far, and the running total pruned across both kinds. It runs
// after every committed chunk and every few thousand scanned rows, so a scan
// with nothing to delete still reports. A nil PruneProgressFunc is allowed.
type PruneProgressFunc func(kind string, scanned, totalPruned int)

// PruneOldLogs deletes legacy operation log rows (operationlog:) and system
// log rows (syslog:) older than retentionDays, in bounded chunks, and returns
// the total records pruned. It never touches undo history (opchange: rows).
//
// A cancelled ctx stops it between chunks or mid-scan: what earlier chunks
// committed stays deleted, the count returned is exactly that, and the error
// wraps ctx.Err() so a caller cannot mistake a partial run for a finished one.
// The next run picks up the remainder. onProgress, if non-nil, runs after
// every committed chunk and every few thousand scanned rows.
func PruneOldLogs(ctx context.Context, store RetentionStore, retentionDays int, log Logger, onProgress PruneProgressFunc) (int, error) {
	if retentionDays <= 0 {
		log.Info("log retention disabled (keep forever), skipping prune")
		return 0, nil
	}

	cutoff := time.Now().AddDate(0, 0, -retentionDays)
	total := 0

	steps := []struct {
		kind  string
		prune func(context.Context, time.Time, func(int, int)) (int, error)
	}{
		{"operation logs", store.PruneOperationLogs},
		{"system logs", store.PruneSystemActivityLogs},
	}
	for _, step := range steps {
		// The store reports this step's running deleted count; it feeds the
		// running total so progress stays right when a step fails partway (the
		// store still returns its partial count with the error).
		n, err := step.prune(ctx, cutoff, func(scanned, deleted int) {
			if onProgress != nil {
				onProgress(step.kind, scanned, total+deleted)
			}
		})
		total += n
		if n > 0 {
			log.Info("pruned %d %s", n, step.kind)
		}
		if err != nil {
			return total, fmt.Errorf("prune %s: %w", step.kind, err)
		}
	}

	return total, nil
}
