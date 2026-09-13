// file: internal/operations/registry/reporter.go
// version: 1.3.0
// guid: e5f6a7b8-c9d0-1e2f-3a4b-5c6d7e8f9a0b
// last-edited: 2026-09-13

package registry

// Reporter is the per-run handle a plugin's Run function uses to emit
// progress, logs, and checkpoints. The real implementation is reporterDB
// in reporter_db.go (UOS-03+). This file is interface-only — the
// transitional UOS-02 stub (stubReporter / newStubReporter) was removed
// after UOS-03 made it unused.

import (
	"context"
	"fmt"
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

// LivenessToucher is implemented by reporters that can stamp an op's liveness
// clock without reporting progress.
//
// The stuck-op watchdog (watchdog.go) reads ONLY the runHandle's lastProgressAt
// atomic, and until this interface existed the only thing that stamped it was
// UpdateProgress (reporter_db.go). SetCurrentItem, Log and Checkpoint do not.
// That left an op driven by RunItems no sanctioned way to say "still working"
// inside one long item: RunItems owns the current/total it passes to
// UpdateProgress, and a mid-item UpdateProgress would have to invent a numerator
// (and bumps HighWaterProgress, which checkInfiniteRestart reads). On 2026-09-13
// maintenance.tag-backfill was killed as stuck while reading the 1,400+ files of
// one book, losing the whole dry-run result.
//
// Call TouchLiveness only after real work finished (a file read, a batch
// written) -- never from a timer. A heartbeat that fires regardless of progress
// would defeat the watchdog for exactly the ops it exists to catch.
//
// Separate from Reporter for the same reason OpID is (see above).
type LivenessToucher interface {
	TouchLiveness()
}

// TouchLiveness stamps rep's liveness clock if it can, and does nothing if it
// cannot (test fakes, adapters). It writes nothing to the store and changes no
// progress numbers, so it is cheap enough to call once per file.
func TouchLiveness(rep Reporter) {
	if t, ok := rep.(LivenessToucher); ok {
		t.TouchLiveness()
	}
}

// ResultSetter is implemented by reporters that can persist an operation's final
// result payload onto its v2 row.
//
// It is separate from Reporter for the same reason OpID is: widening Reporter costs
// twenty-four edits (see above) for a concern most ops do not have. Only ops that
// produce a payload a caller reads back — reconcile previews, diagnostic exports,
// AI suggestion sets — need this.
type ResultSetter interface {
	SetResult(v any) error
}

// ReporterSetResult persists v as the operation's result payload.
//
// UNLIKE ReporterOpID, THIS FAILS LOUDLY when the reporter cannot comply. That
// asymmetry is deliberate. ReporterOpID returns "" on absence because an untagged
// activity entry is merely uncorrelated — no worse than before there was an id to
// give it. A dropped result is not benign in the same way: the payload IS the
// operation's output, and the caller that later reads it back finds nothing, with
// no record that anything went wrong.
//
// That silent shape is exactly what stranded 1,737 v1 operation rows at "pending"
// (fixed 2026-08-22): a write nobody checked, failing quietly for months. An error
// here means the op fails and says why.
func ReporterSetResult(rep Reporter, v any) error {
	rs, ok := rep.(ResultSetter)
	if !ok {
		return fmt.Errorf("registry: reporter %T cannot persist results", rep)
	}
	return rs.SetResult(v)
}
