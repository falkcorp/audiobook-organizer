// file: internal/database/activity_compact_progress.go
// version: 2.0.1
// guid: 7f3e9a21-5c4d-4b8e-9d1f-2a6b8c0e4d73
// last-edited: 2026-10-19

package database

import (
	"context"
	"time"
)

// CompactProgressEvent is one liveness/progress notification emitted while
// CompactByDay runs. The store emits one after every chunk and every day it
// finishes, carrying its running totals so far. There is no completion event:
// the caller of CompactByDay already has the final counters as its return
// value. (A Done/Err/Heartbeat trio existed while a migrating wrapper fanned
// compaction out across a Pebble and a SQLite store; it went with the SQLite
// backend on 2026-10-19.)
type CompactProgressEvent struct {
	// Backend names the store that produced the event ("pebble").
	Backend string
	// Result is the store's running counters for this CompactByDay call.
	Result CompactResult
	// Day is the UTC calendar day the store is working on when the event
	// fired. Zero wherever the emitter has no day in hand.
	Day time.Time
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
// has several implementations (the Pebble store plus test fakes) and ~40 call
// sites; threading a parameter through all of them buys nothing the hook does
// not, and a store that ignores the hook is still correct, only silent.
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
// hook is attached. Stores call it after every chunk and every day; day is the
// day being compacted (zero if unknown).
func reportCompactProgress(ctx context.Context, backend string, day time.Time, result CompactResult) {
	if fn := compactProgressFrom(ctx); fn != nil {
		fn(CompactProgressEvent{Backend: backend, Result: result, Day: day})
	}
}

// ── the other maintenance passes ────────────────────────────────────────────
//
// The nightly cleanup runs compaction, then Summarize, then Prune, then
// RepairActivityIndexes. Compaction reports through the hook above; the other
// passes were silent, and a Summarize over a long unsummarized history — tens
// of thousands of one-transaction groups — is long enough for the registry
// watchdog to strike the op as never_reported. This second hook carries their
// liveness the same way.

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
// non-compaction maintenance pass. The store emits them mid-run with its
// running count for the phase; the caller stamps a phase boundary with an
// empty Backend and zero Rows (see Server.MaintainActivityLog). There is no
// completion event: the caller already has each pass's final count as its
// return value.
type MaintenanceProgressEvent struct {
	// Phase is one of the MaintenancePhase* constants.
	Phase string
	// Backend names the store the event is about ("pebble"), or is empty on a
	// phase-boundary stamp from the caller.
	Backend string
	// Rows is the running count for this phase — rows deleted for
	// summarize/prune, index entries deleted for repair-indexes.
	Rows int64
}

// MaintenanceProgress receives MaintenanceProgressEvents. Called synchronously
// from the maintenance loop; it must be cheap and must not call back into the
// store.
type MaintenanceProgress func(MaintenanceProgressEvent)

type maintenanceProgressKey struct{}

// WithMaintenanceProgress attaches a progress hook to ctx for Summarize, Prune
// and RepairActivityIndexes. It rides the context for the same reason
// WithCompactProgress does; see that function.
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
