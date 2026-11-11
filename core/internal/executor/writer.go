// file: core/internal/executor/writer.go
// version: 1.1.0
// guid: b36bdb50-f432-4165-bf9a-e3cd86723c0a
// last-edited: 2026-11-11

package executor

import (
	"context"
	"fmt"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// writer is a run's tasks.Writer (5T.3.5): the one gated write path.
type writer struct{ a *attempt }

// writable is the attempt's write permit (HEAD's opswriter Fence.Check):
// ErrFenced once the attempt was abandoned (a.fenced) or its run context is
// done, whatever stopped it: a cancel or the watchdog, a quiesce, Shutdown
// or the attempt's Timeout. Every stop path ends a.ctx first, and a
// context's Done channel is closed before its cancel returns, so a body
// that has seen ctx.Done() can no longer pass this check: the permit is
// revoked by the cancel itself, never after it (HEAD revoked the fence
// before cancelling for the same reason). The run's ledger writes
// (checkpoints, marks, progress) do not go through it: a stopping run still
// records where it got to (5T.7).
func (a *attempt) writable() error {
	if a.fenced.Load() {
		return tasks.ErrFenced
	}
	select {
	case <-a.ctx.Done():
		return fmt.Errorf("%w: the run was stopped (%v)", tasks.ErrFenced, context.Cause(a.ctx))
	default:
		return nil
	}
}

// Do refuses with ErrFenced once the run no longer holds its permit
// (5T.3.5: canceled, abandoned, timed out or a stand-down hold lost; see
// writable), with ErrOutsideEffects for a resource the definition did not
// declare; in Preview it records the intent and returns ErrPreview without
// calling fn; in Live it records a write-ahead intent, calls fn, and hands
// the changes to the module's ChangeSink. The permit is checked again after
// the intent, just before fn, so a stop that lands while the intent is
// written still keeps fn from running.
func (w *writer) Do(ctx context.Context, wr tasks.Write, fn func(ctx context.Context) ([]tasks.Change, error)) error {
	a := w.a
	if err := a.writable(); err != nil {
		return err
	}
	if !declared(a.d.writes, wr.Resource) {
		return fmt.Errorf("%w: %s writes %s", tasks.ErrOutsideEffects, a.d.kind, tasks.FormatRef(wr.Resource))
	}
	if err := a.e.renewHolds(ctx, a); err != nil {
		return fmt.Errorf("%w: %v", tasks.ErrFenced, err)
	}
	a.Touch()
	intent := func(changes []tasks.Change) error {
		return a.ledger(ctx, func(ctx context.Context, b *storage.Batch) error {
			return a.e.runs.AppendIntent(ctx, b, a.id, a.nextSeq(), wr, changes)
		})
	}
	if a.rec.Mode == tasks.ModePreview {
		if err := intent(nil); err != nil {
			return err
		}
		return tasks.ErrPreview
	}
	if err := intent(nil); err != nil {
		return err
	}
	if err := a.writable(); err != nil {
		return err
	}
	changes, err := fn(ctx)
	if err != nil {
		return err
	}
	if sink := a.e.opts.ChangeSinks[a.d.module]; sink != nil && len(changes) > 0 {
		if err := sink.Record(ctx, a.id, changes); err != nil {
			return fmt.Errorf("executor: change sink: %w", err)
		}
	}
	return nil
}

// MarkRowApplied records a Fixer row's evidence mark through the per-run
// writer, under the closed-ledger rule (a stopping run still takes it).
func (w *writer) MarkRowApplied(ctx context.Context, rowID string) {
	a := w.a
	if err := a.ledger(ctx, func(ctx context.Context, b *storage.Batch) error {
		return a.e.runs.MarkRow(ctx, b, a.id, rowID)
	}); err != nil {
		a.log.Warn("row mark not recorded; the row is re-evaluated on resume", "row", rowID, "err", err)
	}
}

// RowApplied reports whether a row's changes still stand: the module's
// ChangeSink decides when it has one, else the run's marks.
func (w *writer) RowApplied(ctx context.Context, rowID string) (bool, error) {
	a := w.a
	if sink := a.e.opts.ChangeSinks[a.d.module]; sink != nil {
		return sink.Stands(ctx, a.id, rowID)
	}
	for m, err := range a.e.runs.Marks(ctx, a.id) {
		if err != nil {
			return false, err
		}
		if m == rowID {
			return true, nil
		}
	}
	return false, nil
}
