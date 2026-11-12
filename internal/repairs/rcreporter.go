// file: internal/repairs/rcreporter.go
// version: 1.0.0
// guid: c5cf37f0-6079-49e8-b167-39fb7cbaf6d9
// last-edited: 2026-11-12

package repairs

import (
	"context"
	"errors"
	"log/slog"

	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// ErrNoRunEvents is what Trigger answers on a native run: its events are the
// executor's, not a fixer's to fire.
var ErrNoRunEvents = errors.New("repairs: a native run's events are the executor's; a fixer cannot trigger one")

// rcReporter is the registry.Reporter that Fixer.Plan and Fixer.Replan take
// until F5-18 re-signs them without it, over a native run. It is books code
// that disappears with the v2 reporter: progress and the log go to the run,
// IsCanceled reads the run's context, and Checkpoint is the run's own
// checkpoint.
//
// Over an item (Replan runs inside one) there is no run-level progress or
// checkpoint to write, so those are refused or ignored: Replan reports
// nothing but log lines.
type rcReporter struct {
	ctx        context.Context
	log        *slog.Logger
	progress   func(done, total int64, known bool)
	note       func(string)
	touch      func()
	phase      func(name string) (end func())
	checkpoint func(state any) error
}

var (
	_ registry.Reporter        = (*rcReporter)(nil)
	_ registry.LivenessToucher = (*rcReporter)(nil)
)

// errItemCheckpoint is what Checkpoint answers over an item.
var errItemCheckpoint = errors.New("repairs: an item of a native run has no checkpoint of its own")

// noPhase is the phase of a reporter that has no run to name one on.
func noPhase(string) func() { return func() {} }

// newRCReporter is the reporter of a run's own body (a plan's Candidates).
func newRCReporter(rc *tasks.RC) *rcReporter {
	p := rc.Progress()
	return &rcReporter{
		ctx: rc.Context(),
		log: rc.Log(),
		progress: func(done, total int64, known bool) {
			if known {
				p.Set(done, tasks.Known(total))
			} else {
				p.Set(done, tasks.Unknown())
			}
		},
		note:       p.Note,
		touch:      p.Tick,
		phase:      rc.Phase,
		checkpoint: rc.Checkpoint,
	}
}

// newItemReporter is the reporter of one item of a run (a row's Replan).
func newItemReporter(ic *tasks.ItemRC) *rcReporter {
	return &rcReporter{
		ctx:        ic.Context(),
		log:        ic.Log(),
		progress:   func(int64, int64, bool) {},
		note:       func(string) {},
		touch:      func() {},
		phase:      noPhase,
		checkpoint: func(any) error { return errItemCheckpoint },
	}
}

// UpdateProgress reports current of total (total <= 0 is unknown) and, when
// message is not empty, the run's status line.
func (r *rcReporter) UpdateProgress(current, total int, message string) error {
	r.progress(int64(current), int64(total), total > 0)
	if message != "" {
		r.note(message)
	}
	return nil
}

// Log writes to the run's log at level.
func (r *rcReporter) Log(level slog.Level, message string, attrs ...slog.Attr) error {
	r.log.LogAttrs(r.ctx, level, message, attrs...)
	return nil
}

// Logger is the run's logger.
func (r *rcReporter) Logger() *slog.Logger { return r.log }

// Checkpoint saves state as the run's checkpoint.
func (r *rcReporter) Checkpoint(state any) error { return r.checkpoint(state) }

// IsCanceled reports whether the run's context has ended (a cancel, a
// timeout, a quiesce or a shutdown).
func (r *rcReporter) IsCanceled() bool { return r.ctx.Err() != nil }

// RunPhase runs fn as a named phase of the run.
func (r *rcReporter) RunPhase(ctx context.Context, name string, fn func(context.Context, registry.Reporter) error) error {
	end := r.phase(name)
	defer end()
	return fn(ctx, r)
}

// Trigger is refused: see ErrNoRunEvents.
func (r *rcReporter) Trigger(context.Context, string, any) error { return ErrNoRunEvents }

// SetCurrentItem stamps the run's liveness; the label itself is the
// executor's (it labels each item by its row).
func (r *rcReporter) SetCurrentItem(string) { r.touch() }

// TouchLiveness stamps the run's liveness without reporting progress.
func (r *rcReporter) TouchLiveness() { r.touch() }
