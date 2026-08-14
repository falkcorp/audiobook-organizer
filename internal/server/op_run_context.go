// file: internal/server/op_run_context.go
// version: 1.0.0
// guid: 2a7f4e18-63cb-49d0-9e75-8c31b0d4a6f2
// last-edited: 2026-08-14

package server

import (
	"context"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
	maintenanceplugin "github.com/falkcorp/audiobook-organizer/internal/plugins/maintenance"
)

// opRunContextDecorator builds the context every operation Run receives. It is
// installed via Registry.SetRunContextDecorator in registry_wire.go, and it is
// the ONLY place where an operation's ID and its run context meet — the
// registry deliberately does not import internal/logger or the plugin packages
// (SDKGUARD-VIOLATION #1795), so anything that needs the op ID in-band has to
// be attached here.
//
// It carries two things:
//
//  1. A context-bound slog.Logger tagged with op_id, so every line a run emits
//     is attributable. This was already wired.
//  2. The op ID in the form maintenance.ctxOpID reads. This was NOT. WithOpID
//     was defined and exported but never called anywhere in production code, so
//     ctxOpID returned "" for all eight maintenance ops that ask for it, and
//     every downstream `if operationID != ""` guard silently skipped its
//     CreateOperationChange.
//
// The cost of (2) being missing is not cosmetic. A maintenance.series-prune run
// on 2026-08-14 deleted 326 series and recorded ZERO operation changes: no
// record of which rows went, and nothing for the revert path to replay.
// maintenance.purge-deleted has the same gap while permanently destroying
// books. It also means "0 changes recorded" cannot be read as evidence that an
// op did not run — a mistake made during that same investigation.
//
// Extracted from an inline closure so it can be tested; see
// op_run_context_test.go.
func opRunContextDecorator(ctx context.Context, opID string) context.Context {
	return maintenanceplugin.WithOpID(logger.WithOperation(ctx, opID), opID)
}
