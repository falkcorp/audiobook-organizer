// file: internal/plugins/dedup/bookfile_seg_sweep.go
// version: 1.1.0
// guid: 7a3b5c8e-d1f2-4e9a-b6c0-3d7f1a2e5b8c
// last-edited: 2026-11-18

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

	"github.com/falkcorp/mediacore/tasks"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
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
		ID:              "dedup.bookfile-seg-drop",
		Preview:         sdk.PreviewSpec{Params: bookfileSegDropParams{}},
		Liveness:        sdk.LivenessManual,
		Plugin:          "dedup",
		DisplayName:     "Drop AcoustID segment fields from BookFile values",
		Description:     bookfileSegDropHelp,
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

// bookfileSegDropKind is rule R3's native kind for dedup.bookfile-seg-drop.
const bookfileSegDropKind = tasks.Kind("books.dedup.bookfile-seg-drop")

// bookfileSegDropHelp is HEAD's Description and the native Help (rule R5).
const bookfileSegDropHelp = "Rewrites Pebble book_file: rows that still carry AcoustIDSeg0..6 values, " +
	"removing those deprecated fields and their secondary index entries. " +
	"Saves ~200–400 MB disk. Dry-run by default (pass apply=true to execute). " +
	"Idempotent: a versioned flag prevents re-running after completion."

// bookfileSegDropStore is the reads the core takes. The sweep is scan and
// write in one store call, so a dry run reaches it through SweepBookFileSegDropDry
// (HEAD: SweepBookFileSegDrop with dryRun=true), which writes nothing.
type bookfileSegDropStore interface {
	SweepBookFileSegDropDry(ctx context.Context, batchSize int, progress func(rewrite, total int)) (database.SweepBookFileSegDropResult, error)
	GetSetting(key string) (*database.Setting, error)
}

// bookfileSegDropWriter is the writes the core makes: the live sweep and the
// completion flag.
type bookfileSegDropWriter interface {
	SweepBookFileSegDrop(ctx context.Context, batchSize int, progress func(rewrite, total int)) (database.SweepBookFileSegDropResult, error)
	SetSetting(key, value, dataType string, internal bool) error
}

// bookfileSegDropEnv is what the core takes from its shell. Live is HEAD's
// apply=true; a dry run never calls Writer, which may then be nil.
type bookfileSegDropEnv struct {
	kindEnv
	Store  bookfileSegDropStore
	Writer bookfileSegDropWriter
}

// bookfileSegDropResult is the native run's result. HEAD stored none.
type bookfileSegDropResult struct {
	Total       int  `json:"total"`
	Rewrite     int  `json:"rewrite"`
	Skipped     int  `json:"skipped"`
	Errors      int  `json:"errors"`
	Conflicts   int  `json:"conflicts"`
	AlreadyDone bool `json:"already_done,omitempty"`
}

// headBookfileSegDrop is HEAD's store for the core: the capability the sweep
// lives on, as both the read store (dry) and the writer (live).
type headBookfileSegDrop struct{ s BookfileSegDropStore }

func (h headBookfileSegDrop) SweepBookFileSegDropDry(ctx context.Context, batchSize int, progress func(rewrite, total int)) (database.SweepBookFileSegDropResult, error) {
	return h.s.SweepBookFileSegDrop(ctx, true, batchSize, progress)
}
func (h headBookfileSegDrop) SweepBookFileSegDrop(ctx context.Context, batchSize int, progress func(rewrite, total int)) (database.SweepBookFileSegDropResult, error) {
	return h.s.SweepBookFileSegDrop(ctx, false, batchSize, progress)
}
func (h headBookfileSegDrop) GetSetting(key string) (*database.Setting, error) {
	return h.s.GetSetting(key)
}
func (h headBookfileSegDrop) SetSetting(key, value, dataType string, internal bool) error {
	return h.s.SetSetting(key, value, dataType, internal)
}

// bookfileSegDropCore is the body of runBookfileSegDrop, shared by both shells.
func bookfileSegDropCore(ctx context.Context, env bookfileSegDropEnv) (bookfileSegDropResult, error) {
	env.Logger.Info("bookfile-seg-drop: start", "apply", env.Live)

	// Guard: skip if already completed with apply=true.
	if env.Live {
		if done, err := isFlagSetStore(env.Store, bookfileSegDropDoneFlag); err != nil {
			env.Logger.Warn("bookfile-seg-drop: flag check error (proceeding)", "error", err)
		} else if done {
			env.Logger.Info("bookfile-seg-drop: already completed; skipping",
				"flag", bookfileSegDropDoneFlag)
			env.Progress(1, 1, "Already completed (flag set); nothing to do.")
			return bookfileSegDropResult{AlreadyDone: true}, nil
		}
	}

	mode := "dry-run"
	if env.Live {
		mode = "apply"
	}
	env.Progress(0, 2, fmt.Sprintf("Scanning book_file: rows (%s)…", mode))

	progressFn := func(rewrite, total int) {
		env.Progress(1, 2, fmt.Sprintf(
			"Sweeping: %d / %d rows rewritten so far…", rewrite, total))
		env.Logger.Info("bookfile-seg-drop: progress",
			"rewrite", rewrite, "total", total, "apply", env.Live)
	}

	var (
		res database.SweepBookFileSegDropResult
		err error
	)
	if env.Live {
		res, err = env.Writer.SweepBookFileSegDrop(ctx, bookfileSegDropBatchSize, progressFn)
	} else {
		res, err = env.Store.SweepBookFileSegDropDry(ctx, bookfileSegDropBatchSize, progressFn)
	}
	if err != nil {
		return bookfileSegDropResult{}, fmt.Errorf("bookfile-seg-drop: sweep: %w", err)
	}
	out := bookfileSegDropResult{Total: res.Total, Rewrite: res.Rewrite, Skipped: res.Skipped, Errors: res.Errors, Conflicts: res.Conflicts}

	env.Logger.Info("bookfile-seg-drop: sweep complete",
		"total", res.Total,
		"rewrite", res.Rewrite,
		"skipped", res.Skipped,
		"errors", res.Errors,
		"conflicts", res.Conflicts,
		"apply", env.Live)

	if !env.Live {
		summary := fmt.Sprintf(
			"Dry-run complete — %d of %d rows would be rewritten (%d already clean, %d errors, %d busy). "+
				"Pass apply=true to execute.",
			res.Rewrite, res.Total, res.Skipped, res.Errors, res.Conflicts)
		env.Progress(2, 2, summary)
		return out, nil
	}

	// A row the sweep gave up on because other writers kept changing it was left
	// exactly as stored, with its legacy segment columns. Marking the sweep done
	// would hide it from every later run, so the flag stays unset and the next
	// run (which skips already-clean rows) picks those rows up.
	if res.Conflicts > 0 {
		env.Logger.Warn("bookfile-seg-drop: not setting done flag: rows kept changing under the sweep",
			"conflicts", res.Conflicts, "flag", bookfileSegDropDoneFlag)
	} else if err := env.Writer.SetSetting(bookfileSegDropDoneFlag, "true", "bool", false); err != nil {
		env.Logger.Warn("bookfile-seg-drop: could not set done flag",
			"flag", bookfileSegDropDoneFlag, "error", err)
	} else {
		env.Logger.Info("bookfile-seg-drop: set done flag", "flag", bookfileSegDropDoneFlag)
	}

	summary := fmt.Sprintf(
		"Complete — %d rows rewritten, %d already clean, %d errors, %d left as stored because other writers kept changing them (of %d total rows).",
		res.Rewrite, res.Skipped, res.Errors, res.Conflicts, res.Total)
	env.Progress(2, 2, summary)
	env.Logger.Info("bookfile-seg-drop: complete",
		"rewrite", res.Rewrite, "skipped", res.Skipped, "errors", res.Errors, "conflicts", res.Conflicts, "total", res.Total)
	return out, nil
}

// runBookfileSegDrop is the v2 shell: HEAD's Run (capability resolution, params)
// over bookfileSegDropCore.
func (p *Plugin) runBookfileSegDrop(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	// Resolve the narrow interface through any decorator chain — only
	// *PebbleStore satisfies it, and the registry hands this plugin the server's
	// search-indexing decorator, which embeds database.Store and so cannot
	// satisfy a capability that lives outside it. A bare p.store.(T) assertion
	// fails through that decorator.
	sweepStore, ok := database.AsCapability[BookfileSegDropStore](p.store)
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

	h := headBookfileSegDrop{s: sweepStore}
	_, err := bookfileSegDropCore(ctx, bookfileSegDropEnv{kindEnv: headEnv(reporter, params.Apply), Store: h, Writer: h})
	return err
}

// isFlagSetStore checks whether the named Settings key holds "true"
// using any store that provides GetSetting.
func isFlagSetStore(store interface {
	GetSetting(key string) (*database.Setting, error)
}, key string) (bool, error) {
	setting, err := store.GetSetting(key)
	if err != nil {
		return false, err
	}
	if setting == nil {
		return false, nil
	}
	return setting.Value == "true", nil
}
