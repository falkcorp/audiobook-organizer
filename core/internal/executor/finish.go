// file: core/internal/executor/finish.go
// version: 0.1.0
// guid: 5b0e7c3a-91d4-4f62-8a17-c3e6d2f40b95
// last-edited: 2026-11-22

package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/falkcorp/mediacore/tasks"
)

// runFinishOnly is the attempt of a run Decide(DecideFinish) re-queued,
// "finish without resuming" (05-tasks 5T.3.10; raw/08-prior-art section 7;
// HEAD: Registry.FinishWithoutResuming, registry/finish.go, and the runner's
// finishOnly mode, registry/batch_runner.go). The attempt is dispatched like
// any other, with a fresh lease and fence, but it leases no work: no source
// is read, no item or row is run, and nothing is written to the ledger. It
// runs only the definition's Finish (a Batch) or AfterApply (a Fixer) over
// the ledger and marks the earlier attempts left, and ends succeeded, or
// failed when that returns an error. The record's FinishOnly marker stays
// while the run is not terminal, so a restart in the middle resumes it as
// finish-only again (resume.go), and is cleared when the run ends (move).
//
// A stop that is already ending this attempt (a shutdown, a stand-down, a
// cancel) skips the Finish: the run then ends or resumes by that stop and
// Finish runs once, in the attempt that ends the run (HEAD: batch_runner.go
// finishOnly branch, "Finish is still skipped when a shutdown or stand-down
// is stopping this very attempt").
func (a *attempt) runFinishOnly(rc *tasks.RC, p any) (json.RawMessage, error) {
	a.Touch()
	switch {
	case a.d.batch != nil:
		return a.batchFinishOnly(rc, p)
	case a.d.fixer != nil:
		return a.fixerFinishOnly(rc, p)
	}
	// Decide refuses such a run, so only a record written some other way (an
	// import, a hand-edit) gets here; HEAD fails it the same way
	// (registry/v3_adapter.go:261-263).
	return nil, fmt.Errorf("%w: run %s of %s is marked finish-only, but the kind is a %s", ErrNotFinishable, a.id, a.d.kind, a.d.shape)
}

// batchFinishOnly runs a Batch's Finish over the stored chunk ledger. The
// Summary is what the earlier attempts committed: every complete chunk, read
// whatever the kind's resume policy says (a ResumeFromZero requeue voids the
// ledger for a resumed run, but the rows its chunks record were applied all
// the same: HEAD plan(), registry/batch_runner.go:396-401), so zeroFrom is
// not applied. Total is the run's stored total, since the source is never
// read; ChunksTotal is the chunks done, as at the end of a full run.
func (a *attempt) batchFinishOnly(rc *tasks.RC, p any) (json.RawMessage, error) {
	ctx := rc.Context()
	ledger, err := a.loadChunksFrom(ctx, 0)
	if err != nil {
		return nil, err
	}
	tot := &batchTotals{sum: tasks.Summary{Counters: map[string]int64{}, Total: int64(a.rec.Total)}}
	for _, c := range ledger.byN {
		if c.Complete {
			tot.add(c)
		}
	}
	tot.sum.ChunksTotal = tot.sum.ChunksDone
	if ctx.Err() != nil {
		return nil, context.Cause(ctx)
	}
	if fin := a.d.batch.Finish; fin != nil {
		return fin(rc, p, tot.sum)
	}
	return json.Marshal(tot.sum)
}

// fixerFinishOnly is a Fixer's finish. An apply run (Live) runs AfterApply
// over the rows its marks record as applied, that still stand (the same
// evidence rule as fixerApply: standing), sorted by row ID; AfterApply is
// skipped when no row was applied, as HEAD's does (pkg/ops/fixer.go:252).
// The run's result is the applied count.
//
// A plan run (Preview) applied nothing, and core's plan has no AfterApply
// (fixerPlan), so there is nothing to follow up on: it ends succeeded with
// the plan rows that are stored (none when the plan was interrupted before
// it wrote them, which is what HEAD's plan Finish yields too: an empty plan).
func (a *attempt) fixerFinishOnly(rc *tasks.RC, p any) (json.RawMessage, error) {
	ctx := rc.Context()
	if a.rec.Mode == tasks.ModePreview {
		rows, err := a.e.planRows(ctx, a.id)
		if err != nil {
			return nil, err
		}
		if ctx.Err() != nil {
			return nil, context.Cause(ctx)
		}
		return json.Marshal(fixerResult{Rows: len(rows)})
	}
	var marked []string
	for m, err := range a.e.runs.Marks(ctx, a.id) {
		if err != nil {
			return nil, err
		}
		marked = append(marked, m)
	}
	applied, undone, err := a.standing(ctx, marked)
	if err != nil {
		return nil, err
	}
	slices.Sort(applied)
	if ctx.Err() != nil {
		return nil, context.Cause(ctx)
	}
	if after := a.d.fixer.AfterApply; after != nil && len(applied) > 0 {
		if err := after(rc, applied); err != nil {
			return nil, err
		}
	}
	return json.Marshal(fixerResult{Applied: len(applied), Skipped: undone})
}
