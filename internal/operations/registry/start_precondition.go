// file: internal/operations/registry/start_precondition.go
// version: 1.0.0
// guid: 5b0d8e47-2c91-4f36-a7d4-9e13c6f08b52
// last-edited: 2026-11-28

package registry

import "context"

// StartPrecondition is a definition's own answer to "may this request start me
// at all", asked by EnqueueOp before anything is queued (OperationDef.StartPrecondition).
//
// It exists for a condition that is about the mode the server runs in or the
// shape of the request, not about what the caller may do: a permission is
// checked on each route, and a route is one of several ways to reach EnqueueOp
// (POST /operations/v2, its retry, POST /api/v3/ops/runs, a definition's own
// route). A refusal written on one route is a refusal the others do not make; a
// refusal written here is made by every start, whichever route it came through.
//
// req is the options the start carries (who started it, with which permissions),
// already applied. Return nil to allow the start; RefuseStart(reason) to refuse
// it with a reason a client can be told; any other error is treated as a failure
// of the check itself and surfaces as an internal error. A refused start enqueues
// nothing: no row, no batch entry, no dedupe against an active run.
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
