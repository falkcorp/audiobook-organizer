// file: core/tasks/client.go
// version: 1.0.0
// guid: 2019f76f-fc51-4302-aff9-afa0fcab2225
// last-edited: 2026-11-10

package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"time"

	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// Client is what modules get as module.Deps.Tasks (06 §6.4.4) to start and observe runs by Kind
// (cross-module by Kind string, never by importing the other module, R3).
type Client interface { //nolint:interfacebloat // spec-mandated SDK interface (05-tasks 5T.3.10), frozen by the API-stability gate
	Start(ctx context.Context, kind Kind, req StartRequest) (RunRef, error)
	Get(ctx context.Context, id RunID) (RunView, error)
	Runs(ctx context.Context, q RunQuery) iter.Seq2[RunView, error]
	// Cancel, Decide, Pause and Unpause act as the actor the kernel stamps from
	// ctx, as Start does (below), and apply the native route's permission rule to
	// that actor (the table below: Cancel = the run's actor or Perms.Cancel;
	// Decide = Approve/Start + verified method; Pause/Unpause =
	// core:tasks.manage).
	Cancel(ctx context.Context, id RunID) error
	Decide(ctx context.Context, id RunID, d Decision) error // Resume | Finish | Drop
	Pause(ctx context.Context, reason string) error
	Unpause(ctx context.Context) error
	// PauseState reads the operator pause marker (any authenticated caller;
	// GET /pause). paused is false when no marker is set.
	PauseState(ctx context.Context) (p Pause, paused bool, err error)
	// Timeline, Log and Discard are the code of GET /timeline,
	// GET /runs/{id}/log and DELETE /runs/{id} (FIN12-3), callable by a module
	// too. Timeline needs core:tasks.view and filters by each def's View.
	Timeline(ctx context.Context, q TimelineQuery) ([]RunView, error)
	// Log reads one run's log page, gated by the def's View, each line redacted
	// by RedactRunView's rule.
	Log(ctx context.Context, id RunID, q PageQuery) iter.Seq2[LogLine, error]
	// Discard deletes one run and every record it owns, with Cancel's
	// permission rule (HEAD's "same permission as cancel"). ErrRunNotFound
	// (404); ErrRunActive (409) while the run is queued, running, waiting or
	// held by a live worker, which only the executor knows (HEAD's rule,
	// operations_v2.go:665-679). The executor stages RunStore.Delete inside
	// WriteRuns, so the delete is checked against the run it read and the
	// resume sweep never sees a half-deleted run.
	Discard(ctx context.Context, id RunID) error
}

// Decision is the answer to a run awaiting a decision (ResumeAsk).
// Constant names coined by F2-01.
type Decision uint8

const (
	DecideResume Decision = iota + 1 // resume per the kind's checkpoint
	DecideFinish                     // finish without resuming (raw/08 §7)
	DecideDrop                       // drop: interrupted_dropped
)

// ApproveRequest is a Fixer plan's approval: the approved row IDs of the
// plan run, or a rejection. Fields coined by F2-01.
type ApproveRequest struct {
	Rows   []string // approved row IDs; empty with Reject false approves nothing
	Reject bool
	Reason string
}

// TimelineQuery is Client.Timeline's and GET /timeline's query (FIN12-3).
// HEAD bounds its timeline window on the finish time and keeps unfinished
// runs (the store rule quoted at operations_v2.go:298), which RunFilter's
// Created bound cannot express.
type TimelineQuery struct {
	Kinds  []Kind     // registered kinds; empty = every kind the caller may view
	States []RunState // empty = every state
	From   time.Time  // Finished >= From, unfinished runs included; zero = no bound
	To     time.Time  // Finished < To, unfinished runs included; zero = no bound
	Limit  int        // clamped as HEAD's GetOperationTimeline clamps it (operations_v2.go:237)
}

// RunFilter is what a run listing filters on besides the kind. The zero value
// lists every run, newest first (Created descending, then RunID).
type RunFilter struct {
	States []RunState // empty = every state
	Parent RunID      // non-empty = only the children of this run
	Since  time.Time  // Created >= Since; zero = no bound
	After  RunID      // keyset cursor: continue after this run in listing order
	Limit  int        // 0 = 100; values above 1000 are clamped to 1000
}

// RunQuery is Client.Runs' query, GET /runs' and RunStore.List's: Kinds are
// registered Kinds (there are no aliases to resolve, D10); empty = every kind
// the caller may view.
type RunQuery struct {
	Kinds []Kind
	RunFilter
}

// StartRequest is one start.
type StartRequest struct {
	Mode           ModeRequest // Unset | Preview | Live
	Params         json.RawMessage
	Priority       Priority
	PlanRef        RunID // Fixer / Approval.PlanRequired
	Parent         RunID
	Actor          Actor // on Client (module handle) overwritten by the kernel, see below
	Trigger        Trigger
	IdempotencyKey string // dedupe window core.tasks.idempotency_ttl. Only the HTTP start route sets it
	// (copying the Idempotency-Key header, SIMP2-8); the outbox relay leaves it
	// empty, because its same-batch cursor commit is the guarantee (5T.3.12, SIMP3-8)
}

// ErrModeUnset: a kernel-internal start (StartIn, outbox Tx.StartRun) with
// Mode unset. Only the HTTP route and Client.Start apply the rule-2 default
// (5T.3.7); automation always states its mode (COR2-5).
var ErrModeUnset = errors.New("tasks: start mode not stated")

// RunView is the read model of one run: what Client.Get/Runs return, what
// GET /runs[/{id}] serialise, and the payload of core.task.run-changed.v1.
type RunView struct {
	ID       RunID
	Kind     Kind
	Mode     Mode
	State    RunState
	Held     bool   // computed at read time, never stored (5T.3.11)
	Reason   string // fixed token for interrupted_*/failed/timed_out/canceled states ("" otherwise)
	Actor    Actor  // the stamped actor
	Trigger  Trigger
	Parent   RunID
	PlanRef  RunID
	Created  time.Time
	Started  time.Time // zero until first dispatch
	Finished time.Time // zero until terminal
	Done     int       // completed items (Batch/Fixer/RunItems; Task: last reported)
	Total    int       // 0 = unknown
	Current  string    // current item label; may name a user, so blanked by RedactRunView (below)
}

// usersView is the scope that may read user-identifying run text (05-authn GET /users).
const usersView rbac.Permission = "core:users.view"

// RedactRunView is the ONE redaction of a run's user-identifying text (COR2-2).
// It blanks Current (and any free error text a later version adds) unless who
// holds core:users.view, the canonical successor of HEAD's users.manage gate on
// labels and error text (opsv3/events.go:609-616, view.go:59-64): 05-rbac maps
// users.manage to core:users.manage, which implies core:users.view, and
// core:users.view is the scope that already gates reading user identities
// (05-authn GET /users). tasks sets it as the Redact function of the
// core.task.run-changed.v1 catalog entry (05-events 5E.3.1), so the realtime
// stream applies it, and the GET /runs[/{id}] serialiser calls the same
// function. No module redactor exists. A nil who blanks unconditionally.
func RedactRunView(who rbac.Checker, v RunView) RunView {
	if who == nil || !who.Has(usersView) {
		v.Current = ""
	}
	return v
}

// RunState is the state of a run as the executor and the API see it.
// Terminal: succeeded, failed, timed_out, canceled, interrupted_dropped.
// "held" is not a state (5T.3.11). NativeStores stores these values; the
// tasks cutover maps HEAD's stored vocabulary to them once (5T.8.3).
type RunState string

const (
	StateQueued              RunState = "queued"
	StateWaitingDeps         RunState = "waiting_deps" // Requires / NotWhile not yet satisfied
	StateRunning             RunState = "running"
	StateStopping            RunState = "stopping"             // Reason: cancel | quiesce | shutdown
	StateInterrupted         RunState = "interrupted"          // resume policy decides at next boot (5T.3.6)
	StateInterruptedQuiesced RunState = "interrupted_quiesced" // stood down; re-queued when the hold is released (5T.3.7)
	StateAwaitingDecision    RunState = "awaiting_decision"    // ResumeAsk
	StateSucceeded           RunState = "succeeded"
	StateFailed              RunState = "failed"
	StateTimedOut            RunState = "timed_out"
	StateCanceled            RunState = "canceled"
	StateInterruptedDropped  RunState = "interrupted_dropped" // ResumeDrop, or Decide(Drop)
)

// Terminal reports succeeded, failed, timed_out, canceled and
// interrupted_dropped.
func (s RunState) Terminal() bool {
	switch s {
	case StateSucceeded, StateFailed, StateTimedOut, StateCanceled, StateInterruptedDropped:
		return true
	default:
		return false
	}
}

// Actor stamping (COR-21). On Client.Start — the module handle,
// module.Deps.Tasks — the kernel overwrites req.Actor with the principal in
// ctx (a request) or, outside a request, with the calling module's system
// actor; a module therefore cannot start a run as another actor, and
// Perms.Start is checked against the stamped actor. Only kernel-internal
// callers (Executor.Start/StartIn: the HTTP route, the scheduler, the outbox
// relay) set Actor themselves.
//
// The module system actor (SEC2-15) may start exactly the module's own kinds
// plus the kinds listed in its Manifest.TaskStarts []tasks.Kind (06; a
// reviewed list of foreign and core kinds, each validated at P7 against the
// registered kinds plus every manifest's TaskLedger, 5T.3.1; COR3-10).
// Its permission set is the union of Perms.Start of those kinds, minus
// core:admin and every AuthRequired scope, which it never holds. A start of
// any other kind, or of a listed kind whose Perms.Start needs an excluded
// scope, is refused with 403 tasks.actor_not_permitted.

// Stager is the kernel-provided handle a core service gets when it must start
// a module kind built for it (05-ai's batch poll starting
// <module>.ai.batch-apply.<name>, acquire starting <module>.acquire.import;
// COR2-11, 5T.3.12). It is the ONE name for that starter in every file
// (COR3-8); Executor.StartIn is the relay's only. StartIn stages
// the run into b (the service's own batch) and the kernel stamps req.Actor with
// the system actor of the module that owns kind (SEC2-15 permission set), never
// a core or caller identity; req.Mode must be set (ErrModeUnset). Core services
// hold a Stager, never the Executor. The service builds b inside WriteRuns,
// which commits it (5T.7, R9-1).
type Stager interface {
	StartIn(ctx context.Context, b *storage.Batch, kind Kind, req StartRequest) (RunRef, error)
	// WriteRuns is the one commit path of a batch that carries a RunStore,
	// ClockStore, PauseStore or StandDownStore write. Each attempt opens a
	// fresh batch with open (the caller's own handle, so the caller stays the
	// initiating owner), calls stage, which performs every read the batch's
	// writes derive from and stages the caller's rows and its run writes (here
	// StartIn) and never commits, and then commits the batch with
	// storage.Sync, all under storage.RetryOnConflict (05-storage §5S.3.2), so
	// a conflict re-stages every participant from fresh reads (5T.7). The
	// checks are the stores' commit-time preconditions; there is no lock.
	// WriteRuns is not re-entrant: called from inside another WriteRuns'
	// stage it returns ErrWriteRunsNested and runs nothing, because a nested
	// commit would repeat on every retry of the outer build.
	WriteRuns(ctx context.Context, open func() *storage.Batch, stage func(ctx context.Context, b *storage.Batch) error) error
}

// ErrWriteRunsNested: WriteRuns called inside another WriteRuns' stage, a
// programming error; stage the write into the outer batch instead.
var ErrWriteRunsNested = errors.New("tasks: WriteRuns called inside WriteRuns")

// Executor is kernel-only (core/kernel holds it; apps see Client).
type Executor interface { //nolint:interfacebloat // spec-mandated SDK interface (05-tasks 5T.3.10), frozen by the API-stability gate
	Client
	// CancelAs and DecideAs take the actor explicitly (kernel-internal callers:
	// the HTTP route); Client's Cancel and Decide stamp it from ctx. Distinct
	// names, because Go refuses an embedded method redeclared with another
	// signature (as COR3-13 notes for Start).
	CancelAs(ctx context.Context, id RunID, by Actor) error
	Approve(ctx context.Context, id RunID, a ApproveRequest) error
	DecideAs(ctx context.Context, id RunID, d Decision, by Actor) error // Resume | Finish | Drop
	// StartIn stages a run into b (kernel-internal). The outbox relay uses it to
	// enqueue an outbox-triggered run in the same batch as its cursor advance (5T.3.12).
	StartIn(ctx context.Context, b *storage.Batch, kind Kind, req StartRequest) (RunRef, error)
	// WriteRuns is Stager.WriteRuns for kernel-internal builders (the relay,
	// 05-events 5E.4); the executor's own builders use it too (5T.7).
	WriteRuns(ctx context.Context, open func() *storage.Batch, stage func(ctx context.Context, b *storage.Batch) error) error
	// PauseAs and UnpauseAs stage the pause marker (PauseStore.Set) and commit it
	// inside WriteRuns, as the stand-down marker writes do (5T.3.7; COR9-8);
	// Client's Pause and Unpause call them with the stamped actor.
	PauseAs(reason string, by Actor) error
	UnpauseAs(by Actor) error
	// Lifecycle. Not named Start: Client.Start(ctx, kind, req) is already a
	// method of the embedded interface, and Go refuses the duplicate (COR3-13).
	Run(ctx context.Context) error      // resume sweep, dispatcher, workers (5T.4 step 2)
	Shutdown(ctx context.Context) error // bounded drain, 5T.4
}
