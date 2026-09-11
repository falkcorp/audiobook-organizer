// file: internal/database/activity_compact_progress.go
// version: 1.1.0
// guid: 7f3e9a21-5c4d-4b8e-9d1f-2a6b8c0e4d73
// last-edited: 2026-09-10

package database

import "context"

// CompactProgressEvent is one liveness/progress notification emitted while
// CompactByDay runs. Backends emit one after every chunk and every day they
// finish (Done=false, Result = that backend's running totals so far); a
// wrapper that fans compaction out across backends (MigratingActivityStore)
// emits one more per backend when that backend returns (Done=true, Result =
// its final counters, Err = its error, if any).
type CompactProgressEvent struct {
	// Backend names the store that produced the event: "sqlite", "pebble" or
	// "nuts". A wrapper never relabels a backend's own events.
	Backend string
	// Result is the running (Done=false) or final (Done=true) counters for
	// Backend alone — never a cross-backend total.
	Result CompactResult
	// Done is true for the single completion event a wrapper emits per backend.
	Done bool
	// Err is set on a Done event when that backend's compaction failed.
	Err error
}

// CompactProgress receives CompactProgressEvents. It is called synchronously
// from the compaction loop, so it must be cheap and must not call back into
// the store.
type CompactProgress func(CompactProgressEvent)

type compactProgressKey struct{}

// WithCompactProgress attaches a progress hook to ctx for CompactByDay.
//
// The hook travels in the context rather than in CompactByDay's signature
// because it is an observability side-channel, not an input that changes what
// compaction does — the same reason tracing spans live there. ActivityStorer
// has seven implementations plus wrappers and ~40 call sites; threading a
// parameter through all of them buys nothing the hook does not, and a store
// that ignores the hook is still correct, only silent.
//
// What the hook is FOR: the operations registry cancels an op that reports no
// progress for def.ProgressTimeout (internal/operations/registry/watchdog.go),
// and reporter.Log deliberately does not count as progress
// (registry/types.go, LivenessManual). A multi-hour compaction that only logs
// would therefore be killed as "never_reported". The maintenance ops that run
// compaction attach a hook here that forwards each event to
// reporter.UpdateProgress, which is what keeps the watchdog's clock fresh.
func WithCompactProgress(ctx context.Context, fn CompactProgress) context.Context {
	if fn == nil {
		return ctx
	}
	return context.WithValue(ctx, compactProgressKey{}, fn)
}

// compactProgressFrom returns the hook attached by WithCompactProgress, or nil.
func compactProgressFrom(ctx context.Context) CompactProgress {
	if ctx == nil {
		return nil
	}
	fn, _ := ctx.Value(compactProgressKey{}).(CompactProgress)
	return fn
}

// reportCompactProgress emits a non-terminal progress event for backend if a
// hook is attached. Stores call it after every chunk and every day.
func reportCompactProgress(ctx context.Context, backend string, result CompactResult) {
	if fn := compactProgressFrom(ctx); fn != nil {
		fn(CompactProgressEvent{Backend: backend, Result: result})
	}
}

// reportCompactDone emits the per-backend completion event. Only wrappers that
// fan out across backends call it; a bare store's caller already has the
// return value.
func reportCompactDone(ctx context.Context, backend string, result CompactResult, err error) {
	if fn := compactProgressFrom(ctx); fn != nil {
		fn(CompactProgressEvent{Backend: backend, Result: result, Done: true, Err: err})
	}
}

// ── the other maintenance passes ────────────────────────────────────────────
//
// The nightly cleanup runs compaction, then Summarize, then Prune, then
// RepairActivityIndexes, and since 2026-09-10 each of those runs on every
// activity backend rather than the active one alone. Compaction reports
// through the hook above; the other passes were silent, which on the first
// night over an unsummarized SQLite history — tens of thousands of one-
// transaction groups — is long enough for the registry watchdog to strike the
// op as never_reported. This second hook carries their liveness the same way.

// Maintenance phase names, as reported in MaintenanceProgressEvent.Phase.
const (
	MaintenancePhaseSummarize     = "summarize"
	MaintenancePhasePrune         = "prune"
	MaintenancePhaseRepairIndexes = "repair-indexes"
)

// activityMaintenanceProgressEvery is how many groups a Summarize pass works
// through between progress events. One group is one transaction, so this is a
// few hundred milliseconds at most between stamps.
const activityMaintenanceProgressEvery = 100

// MaintenanceProgressEvent is one liveness/progress notification from a
// non-compaction maintenance pass. Stores emit them mid-run (Done=false, Rows
// = that backend's running count so far); the migrating wrapper emits one per
// backend when that backend returns (Done=true, Rows = its final count, Err =
// its error, if any).
type MaintenanceProgressEvent struct {
	// Phase is one of the MaintenancePhase* constants.
	Phase string
	// Backend names the store the event is about: "sqlite", "pebble" or "nuts".
	Backend string
	// Rows is the running or final count for Backend alone — rows deleted for
	// summarize/prune, index entries deleted for repair-indexes.
	Rows int64
	// Done is true for the single completion event a wrapper emits per backend.
	Done bool
	// Err is set on a Done event when that backend's pass failed.
	Err error
}

// MaintenanceProgress receives MaintenanceProgressEvents. Called synchronously
// from the maintenance loop; it must be cheap and must not call back into the
// store.
type MaintenanceProgress func(MaintenanceProgressEvent)

type maintenanceProgressKey struct{}

// WithMaintenanceProgress attaches a progress hook to ctx for Summarize and
// RepairActivityIndexes. It rides the context for the same reason
// WithCompactProgress does; see that function. Prune takes no context on the
// ActivityStorer interface and so cannot report — its callers bracket it with
// their own stamps.
func WithMaintenanceProgress(ctx context.Context, fn MaintenanceProgress) context.Context {
	if fn == nil {
		return ctx
	}
	return context.WithValue(ctx, maintenanceProgressKey{}, fn)
}

func maintenanceProgressFrom(ctx context.Context) MaintenanceProgress {
	if ctx == nil {
		return nil
	}
	fn, _ := ctx.Value(maintenanceProgressKey{}).(MaintenanceProgress)
	return fn
}

// ReportMaintenanceProgress emits a non-terminal event for phase/backend if a
// hook is attached.
func ReportMaintenanceProgress(ctx context.Context, phase, backend string, rows int) {
	if fn := maintenanceProgressFrom(ctx); fn != nil {
		fn(MaintenanceProgressEvent{Phase: phase, Backend: backend, Rows: int64(rows)})
	}
}

// reportMaintenanceDone emits the per-backend completion event for phase.
// Only wrappers that fan out across backends call it.
func reportMaintenanceDone(ctx context.Context, phase, backend string, rows int64, err error) {
	if fn := maintenanceProgressFrom(ctx); fn != nil {
		fn(MaintenanceProgressEvent{Phase: phase, Backend: backend, Rows: rows, Done: true, Err: err})
	}
}
