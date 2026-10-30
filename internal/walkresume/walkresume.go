// file: internal/walkresume/walkresume.go
// version: 1.0.0
// guid: f932086b-4dc9-4b79-8a0e-a733e43f195c
// last-edited: 2026-10-30

// Package walkresume is the resume plumbing of the four boot-enqueued
// full-library ops (maintenance.external-id-backfill, movement-atom-cleanup,
// malformed-m4b-remux, malformed-m4b-transcode; TODO 04-P6).
//
// It exists because the walks live in layer-3 packages (remux, itunes) that may
// not import the operations registry (layer 4), yet the registry owns the two
// mechanisms the walks must use: the checkpoint blob (Reporter.Checkpoint,
// merged back into params on ResumeRestart) and RunItems (a worker pool whose
// CheckpointStateFn hands out a contiguous-completion watermark). This package
// is the seam: the walk is written against State, Run and Executor, and the
// maintenance plugin supplies a Run whose Executor is registry.RunItems and
// whose Checkpoint is Reporter.Checkpoint. There is no second checkpoint
// format: State is just the JSON the registry already persists.
package walkresume

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"sort"
)

// ErrYield is returned by a walk that stopped on purpose because Run.YieldDue
// said its time budget is nearly spent. The state it checkpointed is durable;
// the maintenance plugin turns it into registry.ErrContinueInPlace so the SAME
// operation is re-queued with a fresh timeout and continues from the cursor.
var ErrYield = errors.New("walkresume: yielded with a durable checkpoint")

// errStop is what an item reports when a yield was decided before it started.
var errStop = errors.New("walkresume: item not started: run is yielding")

// CheckpointEvery is how many completed items pass between checkpoints.
const CheckpointEvery = 10

// State is a resume point. It is the checkpoint the registry persists and
// merges back into the op's params on a restart, so every field must be safe to
// feed back in: a counter carried here is added to by the resumed attempt.
type State struct {
	// Cursor is the last item (a file path, or a book ID) such that every item
	// at or before it, in the walk's sorted order, is done. Resume starts after
	// it. Empty means "from the start".
	Cursor string `json:"cursor,omitempty"`
	// Phase names the stage a multi-stage walk had reached (external-id only).
	Phase string `json:"phase,omitempty"`
	// Index is a stream position inside Phase (external-id XML pass: tracks to
	// skip because they were already registered).
	Index int `json:"index,omitempty"`

	// Counters of the attempts before this one. They are carried because some
	// gate the done flag: a protected file skipped by attempt 1 must still stop
	// attempt 2 from marking the pass done, or the file is never considered.
	Processed        int `json:"processed,omitempty"`
	Changed          int `json:"changed,omitempty"`
	Clean            int `json:"clean,omitempty"`
	Failed           int `json:"failed,omitempty"`
	Skipped          int `json:"skipped,omitempty"`
	SkippedProtected int `json:"skipped_protected,omitempty"`
	Mappings         int `json:"mappings,omitempty"`
}

// Executor runs fn for every index in [resumeFrom, n) and calls onWatermark
// with the absolute contiguous-completion watermark (the number of leading
// items all done) as items finish. The maintenance plugin implements it with
// registry.RunItems. fn may run concurrently; onWatermark calls are serialized.
type Executor func(ctx context.Context, n, resumeFrom int, fn func(ctx context.Context, i int) error, onWatermark func(ctx context.Context, watermark int)) error

// Run is everything a walk needs to be resumable. The zero value is a plain
// sequential, non-checkpointing, non-yielding run, which is what callers that
// know nothing about resume (and the operator's direct callers) get.
type Run struct {
	Resume State
	// Checkpoint persists a resume point. nil disables checkpointing. An error
	// is logged by the caller's checkpoint func, never fatal to the walk.
	Checkpoint func(State)
	// YieldDue reports that the run should stop at its next checkpoint and hand
	// itself back. nil never yields.
	YieldDue func() bool
	// Exec runs the items. nil means sequential in-process.
	Exec Executor
	// Progress reports (processed, total, message) for walks that take no
	// progress argument of their own (movement-atom cleanup). nil is silent.
	Progress func(processed, total int, msg string)
}

// Option configures a walk.
type Option func(*Run)

// WithRun installs r.
func WithRun(r Run) Option { return func(dst *Run) { *dst = r } }

// Resolve folds options into a Run.
func Resolve(opts []Option) Run {
	var r Run
	for _, o := range opts {
		o(&r)
	}
	return r
}

// ResumeIndex returns the index of the first item after cursor in the sorted
// slice. The cursor is a value, not a position, so files added or removed
// between attempts cannot shift the resume point.
func ResumeIndex(sorted []string, cursor string) int {
	if cursor == "" {
		return 0
	}
	i := sort.SearchStrings(sorted, cursor)
	if i < len(sorted) && sorted[i] == cursor {
		i++
	}
	return i
}

// Drive runs process over items (which it sorts) from r.Resume.Cursor.
// snapshot returns the counters at the moment of a checkpoint; Drive sets the
// cursor. It returns nil when every item was processed, ctx.Err() when the
// context ended first, ErrYield when r.YieldDue stopped it (after a durable
// checkpoint), or the first real error process returned.
//
// Items before the watermark are never re-run; up to CheckpointEvery plus the
// pool size items after it are, on resume, so process must be idempotent (all
// four ops are: a rewritten file reads clean on the second look).
func Drive(ctx context.Context, r Run, items []string, process func(ctx context.Context, path string) error, snapshot func() State) error {
	sort.Strings(items)
	start := ResumeIndex(items, r.Resume.Cursor)
	if start >= len(items) {
		return ctx.Err()
	}
	exec := r.Exec
	if exec == nil {
		exec = sequential
	}
	var (
		yielded   = newFlag()
		lastSaved = start
	)
	onWM := func(ctx context.Context, wm int) {
		// The final watermark is not checkpointed: a run that reached the end has
		// nothing to resume, and a checkpoint naming the last item would only
		// outlive the completion it describes.
		if wm <= lastSaved || wm < 1 || wm >= len(items) {
			return
		}
		lastSaved = wm
		if r.Checkpoint != nil {
			st := snapshot()
			st.Cursor = items[wm-1]
			r.Checkpoint(st)
			if r.YieldDue != nil && r.YieldDue() {
				yielded.set()
			}
		}
	}
	err := exec(ctx, len(items), start, func(ctx context.Context, i int) error {
		if yielded.isSet() {
			return errStop
		}
		return process(ctx, items[i])
	}, onWM)
	if yielded.isSet() {
		return ErrYield
	}
	return err
}

func sequential(ctx context.Context, n, resumeFrom int, fn func(ctx context.Context, i int) error, onWatermark func(ctx context.Context, watermark int)) error {
	for i := resumeFrom; i < n; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := fn(ctx, i); err != nil {
			return err
		}
		if wm := i + 1; wm%CheckpointEvery == 0 || wm == n {
			onWatermark(ctx, wm)
		}
	}
	return nil
}

// CollectFiles walks root and returns, sorted, every file accept admits.
// skipDir names directories not to descend into; onWalkErr (may be nil) sees an
// entry the walk could not read, which is skipped. The walk stops with
// ctx.Err() when ctx ends.
//
// Collecting first and processing second is what gives a walk a cursor: a
// filepath.WalkDir order is not the order of the full path strings (it visits
// "a/b" before "a-c"), so a path cursor over a live walk would skip or repeat
// files, while a sorted slice searched by value does neither.
func CollectFiles(ctx context.Context, root string, skipDir func(path string) bool,
	accept func(path string, d fs.DirEntry) bool, onWalkErr func(path string, err error)) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			if onWalkErr != nil {
				onWalkErr(path, walkErr)
			}
			return nil
		}
		if d.IsDir() {
			if skipDir != nil && skipDir(path) {
				return fs.SkipDir
			}
			return nil
		}
		if accept(path, d) {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}
