// file: internal/server/op_run_context_test.go
// version: 1.0.0
// guid: 8e5b1c93-7a20-4f6e-b1d8-53c9e0a7f412
// last-edited: 2026-08-14

package server

import (
	"context"
	"log/slog"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
	maintenanceplugin "github.com/falkcorp/audiobook-organizer/internal/plugins/maintenance"
)

// TestOpRunContextDecorator_CarriesOpIDForMaintenanceOps pins the half of the
// decorator that was missing in production: the op ID in the form the
// maintenance package reads.
//
// maintenance.WithOpID was defined and exported but never called from any
// production code path, so ctxOpID returned "" inside every op, and each
// `if operationID != ""` guard skipped its CreateOperationChange without an
// error or any other visible signal. A series-prune run deleted 326 rows and
// recorded zero undo history. Nothing failed; the audit trail was simply
// absent.
//
// That is why this asserts through maintenance.OpIDFromContext rather than
// re-reading the context key locally: the assertion has to travel the same
// path the ops do, across the package boundary, or it proves nothing about
// them.
func TestOpRunContextDecorator_CarriesOpIDForMaintenanceOps(t *testing.T) {
	const opID = "01KZZRYX6ENB1CGH855N9AVHQJ"

	got := maintenanceplugin.OpIDFromContext(opRunContextDecorator(context.Background(), opID))

	if got != opID {
		t.Errorf("maintenance.OpIDFromContext = %q, want %q — ops that key their "+
			"undo history off this value will record nothing", got, opID)
	}
}

// TestOpRunContextDecorator_PreservesLoggerDecoration guards the OTHER half.
//
// The wiring used to be a bare logger.WithOperation. Composing a second
// decoration on top of it created a new way to regress: drop the
// logger.WithOperation call and op-ID log correlation disappears while the
// audit-trail test above still passes. Both halves need their own assertion.
//
// This compares against slog.Default() by identity rather than installing a
// capturing handler, deliberately. logger.WithOperation reads slog.Default()
// internally, so a capturing assertion means slog.SetDefault — process-global
// state mutated inside a large -race package whose siblings log freely. The
// identity check answers the question that matters (was a logger attached, or
// is FromContext falling through to the default?) without that hazard.
func TestOpRunContextDecorator_PreservesLoggerDecoration(t *testing.T) {
	ctx := opRunContextDecorator(context.Background(), "op-123")

	if logger.FromContext(ctx) == slog.Default() {
		t.Error("logger.FromContext returned slog.Default() — the op-ID logger " +
			"decoration was dropped, so run log lines lose their op_id attribute")
	}
}

// TestOpRunContextDecorator_UndecoratedContextHasNoOpID is the negative
// control. Without it the test above cannot distinguish "the decorator
// attached the ID" from "OpIDFromContext returns something for any context" —
// the assertion would pass against a stub that ignored its argument.
func TestOpRunContextDecorator_UndecoratedContextHasNoOpID(t *testing.T) {
	if got := maintenanceplugin.OpIDFromContext(context.Background()); got != "" {
		t.Errorf("OpIDFromContext(Background) = %q, want \"\"", got)
	}
	if logger.FromContext(context.Background()) != slog.Default() {
		t.Error("logger.FromContext(Background) did not return slog.Default() — " +
			"the decoration check in the sibling test cannot distinguish decorated " +
			"from undecorated contexts")
	}
}
