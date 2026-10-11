// file: internal/logger/retention.go
// version: 1.1.0
// guid: 7c9e2f14-3a5b-4d8c-b1e6-9f2047a85c30
// last-edited: 2026-10-11

package logger

import (
	"context"
	"fmt"
	"time"
)

// RetentionStore is the subset of the store needed for log pruning. Each method
// deletes in bounded chunks, stops between chunks when ctx is cancelled
// (returning ctx.Err() and the rows already deleted), and calls onChunk, if
// non-nil, with the row count of each committed chunk. The callback is spelled
// func(deleted int) rather than database.PruneChunkFunc because database
// imports this package; the latter is an alias of the former, so the two are
// the same type.
type RetentionStore interface {
	PruneOperationLogs(ctx context.Context, olderThan time.Time, onChunk func(deleted int)) (int, error)
	PruneOperationChanges(ctx context.Context, olderThan time.Time, onChunk func(deleted int)) (int, error)
	PruneSystemActivityLogs(ctx context.Context, olderThan time.Time, onChunk func(deleted int)) (int, error)
}

// PruneProgressFunc reports pruning progress after each committed chunk: which
// kind of record ("operation logs", "operation changes", "system activity
// logs") just lost a chunk, and the running total across all three kinds so far.
// A nil PruneProgressFunc is allowed.
type PruneProgressFunc func(kind string, totalPruned int)

// PruneOldLogs deletes logs, changes, and activity entries older than
// retentionDays, in bounded chunks, and returns the total records pruned.
//
// A cancelled ctx stops it between chunks: what earlier chunks committed stays
// deleted, the count returned is exactly that, and the error wraps ctx.Err() so
// a caller cannot mistake a partial run for a finished one. The next run picks
// up the remainder. onProgress, if non-nil, runs after every committed chunk.
func PruneOldLogs(ctx context.Context, store RetentionStore, retentionDays int, log Logger, onProgress PruneProgressFunc) (int, error) {
	if retentionDays <= 0 {
		log.Info("log retention disabled (0 days), skipping prune")
		return 0, nil
	}

	cutoff := time.Now().AddDate(0, 0, -retentionDays)
	total := 0

	steps := []struct {
		kind  string
		prune func(context.Context, time.Time, func(int)) (int, error)
	}{
		{"operation logs", store.PruneOperationLogs},
		{"operation changes", store.PruneOperationChanges},
		{"system activity logs", store.PruneSystemActivityLogs},
	}
	for _, step := range steps {
		// chunkTotal is what this step's committed chunks have reported; it
		// feeds the running total so progress stays right when a step fails
		// partway (the store still returns its partial count with the error).
		chunkTotal := 0
		n, err := step.prune(ctx, cutoff, func(deleted int) {
			chunkTotal += deleted
			if onProgress != nil {
				onProgress(step.kind, total+chunkTotal)
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
