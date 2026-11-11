// file: core/internal/executor/writer.go
// version: 1.0.0
// guid: b36bdb50-f432-4165-bf9a-e3cd86723c0a
// last-edited: 2026-11-10

package executor

import (
	"context"
	"fmt"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// writer is a run's tasks.Writer (5T.3.5): the one gated write path.
type writer struct{ a *attempt }

// Do refuses with ErrFenced once the fence is revoked (grace expired,
// shutdown stamped the run, or a stand-down hold was lost), with
// ErrOutsideEffects for a resource the definition did not declare; in
// Preview it records the intent and returns ErrPreview without calling fn;
// in Live it records a write-ahead intent, calls fn, and hands the changes
// to the module's ChangeSink.
func (w *writer) Do(ctx context.Context, wr tasks.Write, fn func(ctx context.Context) ([]tasks.Change, error)) error {
	a := w.a
	if a.fenced.Load() {
		return tasks.ErrFenced
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
