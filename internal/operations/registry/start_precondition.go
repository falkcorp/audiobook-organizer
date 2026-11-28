// file: internal/operations/registry/start_precondition.go
// version: 1.0.0
// guid: 5b0d8e47-2c91-4f36-a7d4-9e13c6f08b52
// last-edited: 2026-11-28

package registry

import (
	"context"
	"fmt"
)

// StartPrecondition is a definition's own answer to "may this request start me
// at all", asked before anything is queued (OperationDef.StartPrecondition).
//
// It exists for a condition that is about the mode the server runs in or the
// shape of the request, not about what the caller may do: a permission is
// checked on each route, and a route is one of several ways to start an
// operation (POST /operations/v2, its retry, POST /api/v3/ops/runs, a
// definition's own route). A refusal written on one route is a refusal the
// others do not make; a refusal written here is made by every start, whichever
// route it came through.
//
// WHERE IT IS ASKED. Exactly the two paths by which a request starts a run:
//   - EnqueueOp, after the options are applied and before the batch, merge,
//     dedupe and insert branches (a refused start leaves nothing behind in any
//     of them);
//   - RetryInterrupted, the in-place retry of an interrupted row, which never
//     reaches EnqueueOp, with the options of the retrying request.
//
// WHERE IT IS NOT, AND WHY THAT HOLDS. Other paths create or resume a run, but
// none of them is a request starting one:
//   - The boot sweep (resume.go) resumes a row a request already started: Restart
//     and Ask re-queue the same row, which keeps the actor it was queued under,
//     and Drop abandons it. An Ask row waits for an operator, whose decision to
//     retry is a RetryInterrupted and so is asked.
//   - FinishWithoutResuming re-queues a native v3 Batch or Fixer in place; those
//     are registered through the v3 adapter, which builds a definition with no
//     precondition.
//   - Three paths would NOT carry the request's actor into the run, so a
//     precondition that judges the actor would be satisfied by a request and
//     then run by something else: a Batchable def (the flush inserts a run for a
//     bucket of subjects, stamped with no actor), MergeQueuedParams (a request is
//     folded into another account's queued run), and ResumeRequeue (the boot
//     sweep inserts a replacement row that drops the actor). ValidateOpDef
//     refuses a definition that sets StartPrecondition together with any of the
//     three, so a def cannot rely on the precondition through a path that
//     bypasses it.
//
// req is the options the start carries (who started it, with which permissions),
// already applied. Return nil to allow the start; RefuseStart(reason) to refuse
// it with a reason a client can be told; any other error is treated as a failure
// of the check itself and surfaces as an internal error. A refused start enqueues
// nothing: no row, no batch entry, no dedupe against an active run, and a
// refused retry leaves its row as it was.
type StartPrecondition func(ctx context.Context, req EnqueueOptions) error

// StartRefusedError is the error EnqueueOp returns when a definition's
// StartPrecondition refuses the start. Reason is worded for the client: a route
// answers it as a 409 conflict with this text (the request is well formed and
// the caller may make it, but the server cannot start the operation in its
// current mode).
type StartRefusedError struct {
	Reason string
}

func (e *StartRefusedError) Error() string { return "registry: start refused: " + e.Reason }

// RefuseStart is how a StartPrecondition refuses: it returns a StartRefusedError
// carrying reason.
func RefuseStart(reason string) error { return &StartRefusedError{Reason: reason} }

// validateStartPrecondition refuses a definition that sets StartPrecondition
// together with a path that creates the run without carrying the starting
// request's actor into it (see StartPrecondition, "where it is not, and why that
// holds"). Called from ValidateOpDef, so the server refuses to boot on it.
func validateStartPrecondition(def OperationDef) error {
	if def.StartPrecondition == nil {
		return nil
	}
	switch {
	case def.Batchable:
		return fmt.Errorf("registry: OperationDef.StartPrecondition cannot be combined with Batchable (id=%s): "+
			"the batch flush creates the run for a bucket of subjects with no actor, so the precondition would "+
			"judge a request that is not the one that runs", def.ID)
	case def.MergeQueuedParams != nil:
		return fmt.Errorf("registry: OperationDef.StartPrecondition cannot be combined with MergeQueuedParams (id=%s): "+
			"a merged request runs inside another request's queued run, under that run's actor", def.ID)
	case def.ResumePolicy == ResumeRequeue:
		return fmt.Errorf("registry: OperationDef.StartPrecondition cannot be combined with ResumeRequeue (id=%s): "+
			"the boot sweep's replacement row drops the actor the precondition judged", def.ID)
	}
	return nil
}
