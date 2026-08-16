// file: internal/plugins/dedup/bookfile_seg_sweep.go
// version: 1.0.0
// guid: 7a3b5c8e-d1f2-4e9a-b6c0-3d7f1a2e5b8c
// last-edited: 2026-06-10

// Package dedup — op dedup.bookfile-seg-drop (T020, SPEC 3 §6 item 2).
//
// Why this op exists: before the whole-file fingerprint migration (T019)
// BookFile rows in Pebble stored up to 7 per-segment AcoustID hashes
// (AcoustIDSeg0..6). Those fields are now deprecated — LSH replaced the
// last consumer — and removing them from stored values saves ~200–400 MB of
// Pebble disk space and reduces deserialization cost on every file read.
//
// This op iterates every primary book_file: row in Pebble, identifies rows
// that still carry any AcoustIDSeg0..6 value using byte-needle fast-skip,
// rewrites those rows without the segment fields, and removes the
// corresponding book_file_acoustid: secondary index entries.
//
// The op defaults to dry-run (reports counts without writing). Pass
// {"apply":true} to commit rewrites. A versioned flag
// `bookfile_seg_drop_v1_done` is written only when apply=true completes
// successfully, preventing redundant re-runs.
//
// Resumability: re-running after a partial apply is safe — rewritten rows
// carry no seg needles and are fast-skipped. The flag is advisory; omitting
// it (or bumping to v2) forces a full re-scan that no-ops on clean rows.

package dedup

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// bookfileSegDropDoneFlag is the versioned key written to Settings when the
// sweep completes with apply=true. Bump to v2 if the sweep criteria change
// and a forced re-run is needed.
const bookfileSegDropDoneFlag = "bookfile_seg_drop_v1_done"

// bookfileSegDropBatchSize is the default number of row rewrites committed per
// PebbleDB batch (passed to SweepBookFileSegDrop).
const bookfileSegDropBatchSize = 1000

// BookfileSegDropStore is the narrow store interface required by the
// bookfile-seg-drop op. Using a narrow interface keeps the op decoupled from
// the concrete *PebbleStore while remaining testable with a mock.
type BookfileSegDropStore interface {
	// SweepBookFileSegDrop scans all primary book_file: rows and rewrites
	// those carrying AcoustIDSeg0..6 values, removing those fields and their
	// book_file_acoustid: secondary index entries.
	// dryRun=true counts rows without writing.
	SweepBookFileSegDrop(
		ctx context.Context,
		dryRun bool,
		batchSize int,
		progress func(rewrite, total int),
	) (database.SweepBookFileSegDropResult, error)

	// GetSetting / SetSetting manage the versioned completion flag.
	GetSetting(key string) (*database.Setting, error)
	SetSetting(key, value, dataType string, internal bool) error
}

// bookfileSegDropParams are the JSON parameters accepted by the op.
type bookfileSegDropParams struct {
	// Apply, if true, rewrites rows and sets the completion flag.
	// Default false (dry-run) — the op only counts and reports.
	Apply bool `json:"apply"`
}

// bookfileSegDropDef returns the OperationDef for dedup.bookfile-seg-drop.
func (p *Plugin) bookfileSegDropDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "dedup.bookfile-seg-drop",
		Liveness:    sdk.LivenessManual,
		Plugin:      "dedup",
		DisplayName: "Drop AcoustID segment fields from BookFile values",
		Description: "Rewrites Pebble book_file: rows that still carry AcoustIDSeg0..6 values, " +
			"removing those deprecated fields and their secondary index entries. " +
			"Saves ~200–400 MB disk. Dry-run by default (pass apply=true to execute). " +
			"Idempotent: a versioned flag prevents re-running after completion.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "dedup.bookfile-seg-drop",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         60 * time.Minute,
		Capabilities: []sdk.Capability{
			sdk.CapLibraryRead,
			sdk.CapLibraryWrite,
		},
		Run: p.runBookfileSegDrop,
	}
}

// runBookfileSegDrop implements the bookfile-seg-drop op.
func (p *Plugin) runBookfileSegDrop(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	// Type-assert to the narrow interface — only *PebbleStore satisfies it.
	sweepStore, ok := p.store.(BookfileSegDropStore)
	if !ok {
		return fmt.Errorf("store does not implement BookfileSegDropStore (PebbleDB required)")
	}

	// Parse params.
	var params bookfileSegDropParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("parse params: %w", err)
		}
	}

	reporter.Logger().Info("bookfile-seg-drop: start", "apply", params.Apply)

	// Guard: skip if already completed with apply=true.
	if params.Apply {
		if done, err := isFlagSetStore(sweepStore, bookfileSegDropDoneFlag); err != nil {
			reporter.Logger().Warn("bookfile-seg-drop: flag check error (proceeding)", "error", err)
		} else if done {
			reporter.Logger().Info("bookfile-seg-drop: already completed; skipping",
				"flag", bookfileSegDropDoneFlag)
			_ = reporter.UpdateProgress(1, 1, "Already completed (flag set); nothing to do.")
			return nil
		}
	}

	mode := "dry-run"
	if params.Apply {
		mode = "apply"
	}
	_ = reporter.UpdateProgress(0, 2, fmt.Sprintf("Scanning book_file: rows (%s)…", mode))

	progressFn := func(rewrite, total int) {
		_ = reporter.UpdateProgress(1, 2, fmt.Sprintf(
			"Sweeping: %d / %d rows rewritten so far…", rewrite, total))
		reporter.Logger().Info("bookfile-seg-drop: progress",
			"rewrite", rewrite, "total", total, "apply", params.Apply)
	}

	res, err := sweepStore.SweepBookFileSegDrop(ctx, !params.Apply, bookfileSegDropBatchSize, progressFn)
	if err != nil {
		return fmt.Errorf("bookfile-seg-drop: sweep: %w", err)
	}

	reporter.Logger().Info("bookfile-seg-drop: sweep complete",
		"total", res.Total,
		"rewrite", res.Rewrite,
		"skipped", res.Skipped,
		"errors", res.Errors,
		"apply", params.Apply)

	if !params.Apply {
		summary := fmt.Sprintf(
			"Dry-run complete — %d of %d rows would be rewritten (%d already clean, %d errors). "+
				"Pass apply=true to execute.",
			res.Rewrite, res.Total, res.Skipped, res.Errors)
		_ = reporter.UpdateProgress(2, 2, summary)
		return nil
	}

	// Set versioned completion flag.
	if err := sweepStore.SetSetting(bookfileSegDropDoneFlag, "true", "bool", false); err != nil {
		reporter.Logger().Warn("bookfile-seg-drop: could not set done flag",
			"flag", bookfileSegDropDoneFlag, "error", err)
	} else {
		reporter.Logger().Info("bookfile-seg-drop: set done flag", "flag", bookfileSegDropDoneFlag)
	}

	summary := fmt.Sprintf(
		"Complete — %d rows rewritten, %d already clean, %d errors (of %d total rows).",
		res.Rewrite, res.Skipped, res.Errors, res.Total)
	_ = reporter.UpdateProgress(2, 2, summary)
	reporter.Logger().Info("bookfile-seg-drop: complete",
		"rewrite", res.Rewrite, "skipped", res.Skipped, "errors", res.Errors, "total", res.Total)
	return nil
}

// isFlagSetStore checks whether the named Settings key holds "true"
// using a BookfileSegDropStore (which provides GetSetting).
func isFlagSetStore(store BookfileSegDropStore, key string) (bool, error) {
	setting, err := store.GetSetting(key)
	if err != nil {
		return false, err
	}
	if setting == nil {
		return false, nil
	}
	return setting.Value == "true", nil
}
