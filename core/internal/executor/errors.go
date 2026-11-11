// file: core/internal/executor/errors.go
// version: 1.2.0
// guid: ee118157-68ed-4213-be77-f44028207038
// last-edited: 2026-11-11

package executor

import "errors"

// The executor's own refusals. They live here, not in core/tasks, until the
// HTTP mapping (F2-05: 404 tasks.unknown_kind, 403 tasks.actor_not_permitted,
// 409 tasks.held) and the kernel start rule (F2-16) need them in the SDK;
// seams.d/F2-03.md names them as that seam.
var (
	// ErrUnknownKind: a start, or a stored run, names a kind no definition
	// registered (5T.5: 404 tasks.unknown_kind; there are no aliases, D10).
	ErrUnknownKind = errors.New("executor: unknown task kind")
	// ErrHeld: a mutation of a run whose module is disabled (5T.3.11: 409
	// tasks.held). Held runs are read-only while the process runs.
	ErrHeld = errors.New("executor: the run's module is disabled; the run is held and read-only")
	// ErrNotPermitted: the stamped actor may not start, cancel, decide or
	// pause (403; tasks.actor_not_permitted for a module system actor).
	ErrNotPermitted = errors.New("executor: actor not permitted")
	// ErrPlanRequired: a Live start of a kind whose Approval.PlanRequired is
	// set names no plan_ref.
	ErrPlanRequired = errors.New("executor: a live start of this kind must name a preview plan run")
	// ErrPlanInvalid: plan_ref does not name a succeeded Preview run of the
	// same kind.
	ErrPlanInvalid = errors.New("executor: plan_ref does not name a succeeded preview run of this kind")
	// ErrPlanTooOld: the plan finished longer ago than Approval.MaxPlanAge.
	ErrPlanTooOld = errors.New("executor: the plan is older than the kind's maximum plan age")
	// ErrApprovalRequired: a start names a plan_ref without coming through
	// Approve; a plan's apply run starts only there.
	ErrApprovalRequired = errors.New("executor: a run of a plan starts only through Approve")
	// ErrPlanRejected: Approve of a plan that was rejected, or an apply run
	// of one.
	ErrPlanRejected = errors.New("executor: the plan was rejected")
	// ErrPlanDecided: approve or reject of a plan that was already approved.
	// A plan is decided once (a rejected one gives ErrPlanRejected).
	ErrPlanDecided = errors.New("executor: the plan was already approved")
	// ErrNoApproval: a Live run that names a plan has no approval record of
	// that plan, or the plan carries no approval; it applies nothing (fail
	// closed).
	ErrNoApproval = errors.New("executor: no approval recorded for this plan run; nothing applied")
	// ErrReservedCheckpoint: a checkpoint name in the executor's reserved
	// "core." namespace.
	ErrReservedCheckpoint = errors.New("executor: checkpoint names starting with core. are reserved")
	// ErrRowNotInPlan: Approve names a row the plan does not hold.
	ErrRowNotInPlan = errors.New("executor: approved row is not in the plan")
	// ErrNotAwaitingDecision: Decide on a run that is not awaiting_decision.
	ErrNotAwaitingDecision = errors.New("executor: run is not awaiting a decision")
	// ErrHoldLost: a stand-down hold's lease expired without renewal; the
	// holder's next write is refused (5T.3.7).
	ErrHoldLost = errors.New("executor: stand-down hold lost: its lease expired")
	// ErrLaneNotDeclared: Lane names a lane the definition does not declare
	// in Common.Lanes, or one no Options.Lanes entry registers.
	ErrLaneNotDeclared = errors.New("executor: lane not declared")
	// ErrNoLaneCapacity: the lane has no capacity and the work may run
	// in-process instead (Lane.NoLocalFallback is false); with
	// NoLocalFallback the lease waits instead (5T.5).
	ErrNoLaneCapacity = errors.New("executor: lane has no capacity; run in-process")
	// ErrDirtySource: a Batch source built with tasks.DirtySet. The
	// dirty-set store it drains is not built yet (a later row); the run
	// fails closed rather than draining nothing.
	ErrDirtySource = errors.New("executor: DirtySet sources are not supported yet")
	// ErrAlreadyRunning: Run called twice.
	ErrAlreadyRunning = errors.New("executor: Run called twice")
	// ErrShuttingDown: a dispatch-side call after Shutdown began.
	ErrShuttingDown = errors.New("executor: shutting down")
)
