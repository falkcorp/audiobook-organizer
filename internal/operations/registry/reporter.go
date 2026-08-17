// file: internal/operations/registry/reporter.go
// version: 1.1.1
// guid: e5f6a7b8-c9d0-1e2f-3a4b-5c6d7e8f9a0b
// last-edited: 2026-08-17

package registry

// Reporter is the per-run handle a plugin's Run function uses to emit
// progress, logs, and checkpoints. The real implementation is reporterDB
// in reporter_db.go (UOS-03+). This file is interface-only — the
// transitional UOS-02 stub (stubReporter / newStubReporter) was removed
// after UOS-03 made it unused.

import (
	"context"
	"log/slog"
)

// Reporter is the per-run API surface for an in-flight operation.
type Reporter interface {
	UpdateProgress(current, total int, message string) error
	Log(level slog.Level, message string, attrs ...slog.Attr) error
	Logger() *slog.Logger
	Checkpoint(state any) error
	IsCanceled() bool
	RunPhase(ctx context.Context, name string, fn func(context.Context, Reporter) error) error
	Trigger(ctx context.Context, eventName string, payload any) error
	// SetCurrentItem sets the ephemeral "currently working on" label. It is
	// purely in-memory (no DB write) and fans out via SSE as op.current_item.
	// Pass an empty string to clear the label. Safe to call once per loop
	// iteration without measurable cost.
	SetCurrentItem(label string)
}

// ReporterOpID returns the operation id a Reporter belongs to, or "" if the
// reporter cannot say.
//
// Reporter itself deliberately does not carry OpID: see the comment on
// (*dbReporter).OpID for why widening the interface would cost twenty-four edits
// for a concern that only the scheduler has. The production reporter implements
// it; a fake that does not simply yields "".
//
// Callers must therefore treat "" as "unknown", not as an error. The scheduler
// uses the result to tag activity-log entries, where an empty id means the entry
// is uncorrelated — the same state those entries were in before the scheduler
// had any id to give them, and strictly better than the previous behaviour of
// tagging them with a legacy row that no longer exists.
func ReporterOpID(rep Reporter) string {
	if r, ok := rep.(interface{ OpID() string }); ok {
		return r.OpID()
	}
	return ""
}
