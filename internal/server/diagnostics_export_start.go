// file: internal/server/diagnostics_export_start.go
// version: 1.1.0
// guid: 8a66b718-17fa-4493-b04e-5e6f99fde1fc
// last-edited: 2026-11-28

package server

import (
	"context"

	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// diagnosticsStartPrecondition is the diagnostics export definition's start
// precondition (OperationDef.StartPrecondition), asked by EnqueueOp for every
// start of the export, whichever route it arrives by: POST /diagnostics/export,
// POST /operations/v2, a retry of a finished export, POST /api/v3/ops/runs and
// POST /api/v3/ops/defs/:id/runs.
//
// Under core identity an export is built for an account: the job runs after its
// request is gone, narrowed to what the account that queued it may read
// (opActorHolds), and a job with no account fails (errNoExportActor) rather than
// run narrowed to nothing or widened to everything. With core's auth disabled a
// request binds no user, so no actor is stamped, and an export started then could
// only be queued and then fail with nothing the caller can do about it. It is
// refused here with the reason, nothing enqueued; the routes answer it 409
// (servermiddleware.RespondEnqueueError), because the request is well formed and
// the caller may make it: the server's mode is what cannot start the operation.
//
// The actor judged is the one stamped on the start (EnqueueOptions.ActorUserID),
// the same value the job reads back from its row, so the refusal and the failure
// it prevents cannot disagree about who counts as an account.
//
// This does not widen anything: it refuses a start that would have failed. Whether
// an install with auth off should get whole exports is an owner decision
// (todo.d AUTH-OFF-EXPORT); until it is made an export under identity needs an
// account. Off core identity there is no such refusal (HEAD's rule, unchanged:
// its export runs with auth off and holds everything).
//
// s.identity is read when the start is asked, not when the definition is
// registered, so the precondition does not depend on registration order.
func (s *Server) diagnosticsStartPrecondition() opsregistry.StartPrecondition {
	return func(ctx context.Context, req opsregistry.EnqueueOptions) error {
		id := s.identity
		if id == nil || req.ActorUserID != "" {
			return nil
		}
		if id.Bridge != nil && id.Bridge.AuthOff(ctx) {
			return opsregistry.RefuseStart("a diagnostics export needs a signed-in account to be built for, and core " +
				"authentication is disabled on this server, so there is no account; turn core authentication on and " +
				"sign in to start one")
		}
		return opsregistry.RefuseStart("a diagnostics export needs a signed-in account to be built for, and this " +
			"request carries none; sign in and start it again")
	}
}
