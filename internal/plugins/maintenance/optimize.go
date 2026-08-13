// file: internal/plugins/maintenance/optimize.go
// version: 1.1.0
// guid: d4e5f6a7-b8c9-0123-4567-890123456789
// last-edited: 2026-08-12

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/logging"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// optimizeDef returns the OperationDef for library.optimize.
func (p *Plugin) optimizeDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "library.optimize",
		Plugin:          "maintenance",
		DisplayName:     "Library optimize sweep",
		Description:     "Chains cleanup-stale → fingerprint-rescan(missing) → dedup-acoustid-scan → backfill into one user-triggered maintenance pass.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityNormal,
		ConcurrencyKey:  "library.optimize",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         36 * time.Hour,
		Capabilities: []sdk.Capability{
			sdk.CapLibraryRead,
			sdk.CapLibraryWrite,
			sdk.CapFilesRead,
			sdk.CapFilesWrite,
			sdk.CapFilesExecute,
			sdk.CapSubprocessSpawn,
		},
		Run: p.runOptimize,
	}
}

// childOp describes a single child operation in the optimize sweep.
type childOp struct {
	name   string
	defID  string
	params any
}

func (p *Plugin) runOptimize(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	opID := ctxOpID(ctx)
	start := time.Now()

	logging.Info(ctx, "library.optimize: sweep started",
		"operation_id", opID,
	)
	_ = reporter.Log(slog.LevelInfo, "Library optimize sweep started")

	children := []childOp{
		{
			name:   "temp-file-cleanup",
			defID:  "maintenance.temp-file-cleanup",
			params: nil,
		},
		{
			name:  "fingerprint-rescan-missing",
			defID: "acoustid.fingerprint-rescan",
			params: map[string]any{
				"scope": "missing",
			},
		},
		{
			name:   "acoustid-scan",
			defID:  "acoustid.scan",
			params: nil,
		},
		{
			name:   "acoustid-backfill",
			defID:  "acoustid.backfill",
			params: nil,
		},
	}

	// maintenance.acoustid_backfill (default OFF as of 2026-08-11): the backfill's load
	// phase pulls the whole book table into memory before it can start — ~862 MB of live
	// heap in production, implicated in three OOM kills in one night. optimize is a broad
	// "tidy everything" sweep rather than a request for this specific op, so it is an
	// automatic trigger for the purposes of the flag and respects it. A direct
	// EnqueueOp("acoustid.backfill") via the ops API stays ungated — that is the
	// deliberate opt-in path. Filtering here rather than skipping mid-loop keeps `total`
	// honest, so progress does not report a child it never intended to run.
	if !config.AppConfig.Maintenance.AcoustIDBackfill {
		kept := make([]childOp, 0, len(children))
		for _, ch := range children {
			if ch.defID == "acoustid.backfill" {
				logging.Info(ctx, "library.optimize: acoustid-backfill excluded",
					"operation_id", opID,
					"reason", "maintenance.acoustid_backfill=false",
				)
				_ = reporter.Log(slog.LevelInfo,
					"Skipping acoustid-backfill: disabled by maintenance.acoustid_backfill")
				continue
			}
			kept = append(kept, ch)
		}
		children = kept
	}

	total := len(children)
	completed := 0
	failed := 0

	prog := sdk.NewProgress(reporter, total)
	prog.Start("Starting optimize sweep...")

	for i, ch := range children {
		if reporter.IsCanceled() {
			logging.Info(ctx, "library.optimize: sweep canceled",
				"operation_id", opID,
				"completed", completed,
				"failed", failed,
			)
			_ = reporter.Log(slog.LevelInfo,
				fmt.Sprintf("Optimize sweep canceled after %d/%d children", i, total))
			return fmt.Errorf("canceled")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		prog.StepN(i, fmt.Sprintf("Running child op %d/%d: %s", i+1, total, ch.name))

		childStart := time.Now()
		logging.Info(ctx, "library.optimize: child started",
			"operation_id", opID,
			"child", ch.name,
			"def_id", ch.defID,
			"child_index", i+1,
			"child_total", total,
		)
		_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("Starting child %d/%d: %s", i+1, total, ch.name))

		childID, err := p.deps.EnqueueOp(ctx, ch.defID, ch.params)
		if err != nil {
			elapsed := time.Since(childStart)
			logging.Warn(ctx, "library.optimize: child enqueue failed",
				"operation_id", opID,
				"child", ch.name,
				"def_id", ch.defID,
				"elapsed_ms", elapsed.Milliseconds(),
				"error", err,
			)
			_ = reporter.Log(slog.LevelWarn,
				fmt.Sprintf("Child %s enqueue failed (skipping): %v", ch.name, err))
			failed++
			continue
		}

		logging.Info(ctx, "library.optimize: child enqueued",
			"operation_id", opID,
			"child", ch.name,
			"child_id", childID,
		)

		// Wait for the child to reach a terminal state.
		if waitErr := p.deps.WaitForOp(ctx, childID); waitErr != nil {
			elapsed := time.Since(childStart)
			logging.Warn(ctx, "library.optimize: child failed or timed out",
				"operation_id", opID,
				"child", ch.name,
				"child_id", childID,
				"elapsed_ms", elapsed.Milliseconds(),
				"error", waitErr,
			)
			_ = reporter.Log(slog.LevelWarn,
				fmt.Sprintf("Child %s failed (continuing): %v", ch.name, waitErr))
			failed++
			continue
		}

		elapsed := time.Since(childStart)
		logging.Info(ctx, "library.optimize: child completed",
			"operation_id", opID,
			"child", ch.name,
			"child_id", childID,
			"elapsed_ms", elapsed.Milliseconds(),
		)
		_ = reporter.Log(slog.LevelInfo,
			fmt.Sprintf("Child %s completed in %s", ch.name, elapsed.Round(time.Second)))
		completed++
	}

	totalElapsed := time.Since(start)
	logging.Info(ctx, "library.optimize: sweep complete",
		"operation_id", opID,
		"children_completed", completed,
		"children_failed", failed,
		"children_total", total,
		"elapsed_ms", totalElapsed.Milliseconds(),
	)
	prog.Done(fmt.Sprintf("Optimize sweep complete in %s — %d/%d children succeeded",
		totalElapsed.Round(time.Second), completed, total))
	_ = reporter.Log(slog.LevelInfo,
		fmt.Sprintf("Library optimize sweep complete: %d/%d children succeeded, %d failed (elapsed: %s)",
			completed, total, failed, totalElapsed.Round(time.Second)))

	return nil
}
