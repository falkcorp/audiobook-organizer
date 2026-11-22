// file: core/internal/executor/control.go
// version: 1.4.0
// guid: e0da087e-cd1b-40dd-8ed9-97f8b015517f
// last-edited: 2026-11-11

package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"slices"

	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// Scopes the executor checks itself (05-rbac's fixed core:* catalog).
const (
	scopeTasksView   rbac.Permission = "core:tasks.view"
	scopeTasksManage rbac.Permission = "core:tasks.manage"
)

// caller resolves the caller of a Client call (principal).
func (e *executor) caller(ctx context.Context) (tasks.Actor, rbac.Checker) {
	a, who, ok := e.principal(ctx)
	if !ok {
		return tasks.Actor{}, nil
	}
	return a, who
}

// mayView applies a definition's View requirement (omitted: core:tasks.view).
// A module's own client (ModuleClient) outside a request reads every run of
// its own module's kinds: the module owns them, and its system actor holds
// core:tasks.view for no kind. A kind of another module goes through the
// checker like any caller's.
func (e *executor) mayView(who rbac.Checker, kind tasks.Kind) bool {
	if who == nil {
		return true
	}
	if mc, ok := who.(moduleChecker); ok && kind.Module() == mc.module {
		return true
	}
	if d := e.defs[kind]; d != nil && !d.c.Perms.View.IsZero() {
		return who.Satisfies(d.c.Perms.View)
	}
	return who.Has(scopeTasksView)
}

// Get implements tasks.Client.Get; a run the caller may not view is not
// found (closed).
func (e *executor) Get(ctx context.Context, id tasks.RunID) (tasks.RunView, error) {
	rec, err := e.runs.Get(ctx, id)
	if err != nil {
		return tasks.RunView{}, err
	}
	_, who := e.caller(ctx)
	if !e.mayView(who, rec.Kind) {
		return tasks.RunView{}, fmt.Errorf("%w: %s", tasks.ErrRunNotFound, id)
	}
	return e.viewAs(who, rec), nil
}

// Runs implements tasks.Client.Runs, omitting runs the caller may not view.
func (e *executor) Runs(ctx context.Context, q tasks.RunQuery) iter.Seq2[tasks.RunView, error] {
	_, who := e.caller(ctx)
	return func(yield func(tasks.RunView, error) bool) {
		for rec, err := range e.runs.List(ctx, q) {
			if err != nil {
				e.ins.undecodable("run")
				continue // hidden (5T.5)
			}
			if !e.mayView(who, rec.Kind) {
				continue
			}
			if !yield(e.viewAs(who, rec), nil) {
				return
			}
		}
	}
}

// Timeline implements tasks.Client.Timeline: runs finished in [From, To)
// plus unfinished ones, newest first.
func (e *executor) Timeline(ctx context.Context, q tasks.TimelineQuery) ([]tasks.RunView, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 100
	}
	limit = min(limit, 1000)
	var out []tasks.RunView
	for v, err := range e.Runs(ctx, tasks.RunQuery{Kinds: q.Kinds, States: q.States, Limit: 1000}) {
		if err != nil {
			return nil, err
		}
		if !v.Finished.IsZero() && (!q.From.IsZero() && v.Finished.Before(q.From) || !q.To.IsZero() && !v.Finished.Before(q.To)) {
			continue
		}
		out = append(out, v)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// Log implements tasks.Client.Log (5T.3.10): the run's View rule, and each line
// redacted by RedactRunView's rule (tasks.RedactLogAppended) for the caller's
// checker, so no caller of the client, not only GET /runs/{id}/log, can read a
// line's user-identifying text without core:users.view.
//
// A caller with no resolved identity (who == nil: a kernel-internal call, or an
// executor built without a Principal) sees the lines as stored, the same way
// mayView treats it. That is the one place this differs from RedactLogAppended,
// which blanks for a nil checker because a stream subscriber with no checker is
// an unwired entry; here nil means in-process, which is trusted.
func (e *executor) Log(ctx context.Context, id tasks.RunID, q tasks.PageQuery) iter.Seq2[tasks.LogLine, error] {
	return func(yield func(tasks.LogLine, error) bool) {
		if _, err := e.Get(ctx, id); err != nil {
			yield(tasks.LogLine{}, err)
			return
		}
		_, who := e.caller(ctx)
		q.Page = tasks.PageLog
		for p, err := range e.runs.Pages(ctx, id, q) {
			line := p.Log
			if err == nil && who != nil {
				r := tasks.RedactLogAppended(who, tasks.NewLogAppended(id, line))
				line = tasks.LogLine{At: r.At, Level: r.Level, Message: r.Message, Fields: r.Fields}
			}
			if !yield(line, err) || err != nil {
				return
			}
		}
	}
}

// PauseState implements tasks.Client.PauseState.
func (e *executor) PauseState(ctx context.Context) (tasks.Pause, bool, error) {
	return e.opts.Stores.Pause.Get(ctx)
}

// Pause implements tasks.Client.Pause (core:tasks.manage).
func (e *executor) Pause(ctx context.Context, reason string) error {
	actor, who := e.caller(ctx)
	if who != nil && !who.Has(scopeTasksManage) {
		return fmt.Errorf("%w: pause", ErrNotPermitted)
	}
	return e.PauseAs(reason, actor)
}

// Unpause implements tasks.Client.Unpause (core:tasks.manage).
func (e *executor) Unpause(ctx context.Context) error {
	actor, who := e.caller(ctx)
	if who != nil && !who.Has(scopeTasksManage) {
		return fmt.Errorf("%w: unpause", ErrNotPermitted)
	}
	return e.UnpauseAs(actor)
}

// PauseAs stages the pause marker inside WriteRuns, then closes the
// in-process gate: dispatch stops and items park before their clock.
func (e *executor) PauseAs(reason string, by tasks.Actor) error {
	p := &tasks.Pause{Reason: reason, By: by, At: e.now()}
	if err := e.write(context.Background(), func(ctx context.Context, b *storage.Batch) error {
		return e.opts.Stores.Pause.Set(ctx, b, p)
	}); err != nil {
		return err
	}
	e.setPaused(true)
	return nil
}

// UnpauseAs clears the marker and opens the gate.
func (e *executor) UnpauseAs(by tasks.Actor) error {
	if err := e.write(context.Background(), func(ctx context.Context, b *storage.Batch) error {
		return e.opts.Stores.Pause.Set(ctx, b, nil)
	}); err != nil {
		return err
	}
	e.setPaused(false)
	e.log.Info("task executor unpaused", "by", by.UserID+by.System)
	e.wakeDispatcher()
	return nil
}

// Cancel implements tasks.Client.Cancel: the run's own actor, Perms.Cancel
// (omitted: Perms.Start) or core:tasks.manage.
func (e *executor) Cancel(ctx context.Context, id tasks.RunID) error {
	actor, who := e.caller(ctx)
	if who != nil {
		rec, err := e.runs.Get(ctx, id)
		if err != nil {
			return err
		}
		if !e.moduleMayAct(who, rec, actor) || !e.mayCancel(rec, actor, who) {
			return fmt.Errorf("%w: cancel %s", ErrNotPermitted, id)
		}
	}
	return e.CancelAs(ctx, id, actor)
}

func (e *executor) mayCancel(rec tasks.RunRecord, actor tasks.Actor, who rbac.Checker) bool {
	if actor != (tasks.Actor{}) && actor == rec.Actor {
		return true
	}
	if who.Has(scopeTasksManage) {
		return true
	}
	d := e.defs[rec.Kind]
	if d == nil {
		return false
	}
	req := d.c.Perms.Cancel
	if req.IsZero() {
		req = d.c.Perms.Start
	}
	return who.Satisfies(req)
}

// CancelAs cancels a run (5T.3.6). A run that is not executing in this
// process ends canceled at once; an executing one goes stopping{cancel},
// its context is canceled with tasks.ErrCanceled and its supervisor
// abandons it after the contract's grace. A run stopping for a quiesce or a
// shutdown (a stop that would resume it later) is turned into a cancel; one
// already stopping for a cancel, the watchdog or its timeout is answered
// nil; a terminal one is not found (HEAD's repeat-cancel rule,
// cancel_persisted_test.go). A held run is refused (ErrHeld).
func (e *executor) CancelAs(ctx context.Context, id tasks.RunID, by tasks.Actor) error {
	rec, err := e.runs.Get(ctx, id)
	if err != nil {
		return err
	}
	if e.held(rec.Kind) {
		return fmt.Errorf("%w: %s", ErrHeld, id)
	}
	return e.stop(ctx, id, by, reasonCancel, tasks.ErrCanceled)
}

// stop is cancel's core, shared with the watchdog (reason watchdog). The
// in-process attempt is looked up inside the move, from the same fresh read
// the CAS is staged against, so a run that goes live between Cancel's call
// and its write is still stopped (attemptFor).
func (e *executor) stop(ctx context.Context, id tasks.RunID, by tasks.Actor, reason string, cause error) error {
	actor := &by
	if by == (tasks.Actor{}) {
		actor = nil
	}
	var target *attempt // the attempt the committed move put in stopping
	_, err := e.move(ctx, id, func(cur tasks.RunRecord) (tasks.RunRecord, tasks.RunEvent, error) {
		target = nil
		if cur.State.Terminal() {
			return cur, tasks.RunEvent{}, fmt.Errorf("%w: %s is %s", tasks.ErrRunNotFound, id, cur.State)
		}
		next := cur
		ev := tasks.RunEvent{Event: "cancel", Reason: reason, Actor: actor}
		if cur.State == tasks.StateStopping {
			// A cancel is final: it overrides a stop that would resume the
			// run later. Any other stop already ends the run.
			if reason != reasonCancel || (cur.Reason != reasonQuiesce && cur.Reason != reasonShutdown) {
				return cur, tasks.RunEvent{}, errAlreadyStopping
			}
			target = e.attemptFor(cur)
			next.Reason = reasonCancel
			return next, ev, nil
		}
		if target = e.attemptFor(cur); target != nil {
			next.State, next.Reason = tasks.StateStopping, reason
			return next, ev, nil
		}
		next.State, next.Reason, next.Finished = tasks.StateCanceled, reason, e.now()
		return next, ev, nil
	}, nil)
	if errors.Is(err, errAlreadyStopping) {
		return nil
	}
	if err != nil || target == nil {
		return err
	}
	e.mu.Lock()
	if target.stopping == "" || reason == reasonCancel {
		target.stopping = reason // a cancel replaces a quiesce or shutdown already under way
	}
	if target.stopCause == nil {
		target.stopCause = cause
	}
	live := target.live
	e.mu.Unlock()
	if live {
		target.cancel(cause)
	} // else startAttempt applies stopCause as the attempt goes live, and the body never starts
	return nil
}

// attemptFor is the in-process attempt executing cur, read at each staging
// of a stop: a live attempt of cur's attempt and fence, or a claim whose
// queued->running commit is cur but whose attempt has not started yet (the
// dispatcher claims only queued runs, so a registered, not-yet-live attempt
// of a running record made that commit). nil for a zombie, a claim still in
// flight (cur is queued: the stop's CAS and the claim's both want queued, so
// one of them loses) and a run not executing in this process.
func (e *executor) attemptFor(cur tasks.RunRecord) *attempt {
	if cur.State != tasks.StateRunning && cur.State != tasks.StateStopping {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	a := e.running[cur.ID]
	switch {
	case a == nil || a.zombie || a.exited:
		return nil
	case a.live:
		if a.n == cur.Attempt && a.fence == cur.FenceEpoch {
			return a
		}
		return nil
	case cur.State == tasks.StateRunning:
		return a
	}
	return nil
}

var errAlreadyStopping = errors.New("executor: run is already stopping")

// Decide implements tasks.Client.Decide: Perms.Approve if set, else
// Perms.Start (the verified-method half is the HTTP route's, F2-05). As
// with Approve, a call with no caller identity (no Principal, or a context
// it resolves to no one) is refused: a decision is always a person's.
// Kernel-internal callers that carry the actor themselves use DecideAs.
func (e *executor) Decide(ctx context.Context, id tasks.RunID, d tasks.Decision) error {
	rec, err := e.runs.Get(ctx, id)
	if err != nil {
		return err
	}
	if e.held(rec.Kind) {
		return fmt.Errorf("%w: %s", ErrHeld, id)
	}
	def := e.defs[rec.Kind]
	if def == nil {
		return fmt.Errorf("%w: %s", ErrUnknownKind, rec.Kind)
	}
	actor, who := e.caller(ctx)
	if who == nil {
		return fmt.Errorf("%w: decide %s: no caller identity", ErrNotPermitted, id)
	}
	req := def.c.Perms.Approve
	if req.IsZero() {
		req = def.c.Perms.Start
	}
	if !e.moduleMayAct(who, rec, actor) || !who.Satisfies(req) {
		return fmt.Errorf("%w: decide %s", ErrNotPermitted, id)
	}
	return e.DecideAs(ctx, id, d, actor)
}

// DecideAs answers a run awaiting a decision (ResumeAsk): Resume re-queues
// it to continue from its checkpoint (a full attempt), Finish re-queues it
// to run only its Finish (Batch) or AfterApply (Fixer) over the stored
// ledger without resuming (finishOnly, finish.go), Drop ends it
// interrupted_dropped.
//
// Finish is refused with ErrNotFinishable, before any write, for a kind
// that is neither a Batch nor a Fixer (HEAD's CanFinishWithoutResuming,
// registry/finish.go:34-44, which has no "succeed now" for a plain task
// either: its "finish" would be a full run, Retry under another name).
func (e *executor) DecideAs(ctx context.Context, id tasks.RunID, d tasks.Decision, by tasks.Actor) error {
	rec, err := e.runs.Get(ctx, id)
	if err != nil {
		return err
	}
	if e.held(rec.Kind) {
		return fmt.Errorf("%w: %s", ErrHeld, id)
	}
	if d == tasks.DecideFinish {
		def := e.defs[rec.Kind]
		switch {
		case def == nil:
			return fmt.Errorf("%w: %s", ErrUnknownKind, rec.Kind)
		case def.batch == nil && def.fixer == nil:
			return fmt.Errorf("%w: %s is a %s run of %s", ErrNotFinishable, id, def.shape, rec.Kind)
		}
	}
	actor := by
	_, err = e.move(ctx, id, func(cur tasks.RunRecord) (tasks.RunRecord, tasks.RunEvent, error) {
		if cur.State != tasks.StateAwaitingDecision {
			return cur, tasks.RunEvent{}, fmt.Errorf("%w: %s is %s", ErrNotAwaitingDecision, id, cur.State)
		}
		next := cur
		ev := tasks.RunEvent{Event: "decide", Actor: &actor}
		switch d {
		case tasks.DecideResume:
			// An explicit resume is a full attempt: it clears a finish-only
			// marker (HEAD: every entry into queued but decide_finish and
			// wait_cleared does, pebble_store_ops_v3.go:1080-1085).
			next.State, next.Reason, next.FinishOnly = tasks.StateQueued, "", false
			ev.Reason = "resume"
		case tasks.DecideFinish:
			// Back to queued under the same run ID with the marker set: the
			// next attempt is dispatched like any other (fresh lease and
			// fence) and runs only Finish (attempt.runFinishOnly).
			next.State, next.Reason, next.FinishOnly = tasks.StateQueued, "", true
			ev.Reason = "finish"
		case tasks.DecideDrop:
			next.State, next.Reason, next.Finished = tasks.StateInterruptedDropped, "decided_drop", e.now()
			ev.Reason = "drop"
		default:
			return cur, tasks.RunEvent{}, fmt.Errorf("executor: unknown decision %d", d)
		}
		return next, ev, nil
	}, nil)
	return err
}

// Approve answers a Fixer plan (5T.3.7). A plan is decided once: the
// decision is a move on the plan run (an approve or reject event), so two
// answers to one plan serialise on the plan's record and the second is
// refused (ErrPlanDecided, or ErrPlanRejected for a rejected plan). An
// approval starts the Live apply run of the same kind with the plan's
// params, PlanRef set and an approval record naming the plan and the
// approved row IDs, in the same batch as the decision. The apply run is
// always a new run: it never folds into a queued run (stageStart), so it
// shares no state with Start and takes no startMu. Rows must belong to the
// plan, which must be a succeeded Preview run within MaxPlanAge.
//
// Approve and reject fail closed: they need a caller identity (a
// Principal that resolves, with a checker) holding Perms.Approve, which
// definition validation requires whenever Approval is set. A call with no
// identity is refused, never treated as kernel-internal. The apply run is
// a start, so the approver also needs Perms.Start (permitStart).
func (e *executor) Approve(ctx context.Context, id tasks.RunID, req tasks.ApproveRequest) error {
	plan, err := e.runs.Get(ctx, id)
	if err != nil {
		return err
	}
	if e.held(plan.Kind) {
		return fmt.Errorf("%w: %s", ErrHeld, id)
	}
	d := e.defs[plan.Kind]
	if d == nil || d.c.Approval == nil {
		return fmt.Errorf("%w: %s's kind takes no approval", ErrPlanInvalid, id)
	}
	actor, who := e.caller(ctx)
	if who == nil {
		return fmt.Errorf("%w: approve %s: no caller identity", ErrNotPermitted, id)
	}
	if need := d.c.Perms.Approve; need.IsZero() || !who.Satisfies(need) {
		return fmt.Errorf("%w: approve %s", ErrNotPermitted, id)
	}
	if err := e.checkPlan(ctx, d, id); err != nil {
		return err
	}
	var by *tasks.Actor
	if actor != (tasks.Actor{}) {
		by = &actor
	}
	decide := func(event string) editFunc {
		return func(cur tasks.RunRecord) (tasks.RunRecord, tasks.RunEvent, error) {
			if err := e.undecided(ctx, cur.ID); err != nil {
				return cur, tasks.RunEvent{}, err
			}
			return cur, tasks.RunEvent{Event: event, Reason: req.Reason, Actor: by}, nil
		}
	}
	if req.Reject {
		_, err := e.move(ctx, id, decide(eventReject), nil)
		return err
	}
	inPlan, err := e.planRows(ctx, id)
	if err != nil {
		return err
	}
	rows := slices.Clone(req.Rows)
	slices.Sort(rows)
	rows = slices.Compact(rows)
	for _, r := range rows {
		if _, ok := inPlan[r]; !ok {
			return fmt.Errorf("%w: %s", ErrRowNotInPlan, r)
		}
	}
	start := tasks.StartRequest{Mode: tasks.ModeRequestLive, Params: plan.Params, PlanRef: id, Actor: actor, Trigger: tasks.TriggerManual}
	prep, err := e.prepare(ctx, plan.Kind, start, starter{who: who}, modeApprove)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(approvalRecord{Plan: id, Rows: rows})
	if err != nil {
		return err
	}
	env, err := json.Marshal(ckptEnvelope{A: 1, V: raw})
	if err != nil {
		return err
	}
	_, err = e.move(ctx, id, decide(eventApprove), func(ctx context.Context, b *storage.Batch, _ tasks.RunRecord) error {
		ref, err := e.stageStart(ctx, b, prep)
		if err != nil {
			return err
		}
		if ref.Existing {
			return fmt.Errorf("executor: approve of %s reached existing run %s; an apply run is always new", id, ref.ID)
		}
		return e.runs.SaveCheckpoint(ctx, b, ref.ID, ckptApproved, env)
	})
	if err == nil {
		e.wakeDispatcher()
	}
	return err
}

// approvalRecord is an apply run's core.approved value: the plan it
// approves and the approved row IDs. The apply run applies the rows only
// while Plan is its own PlanRef.
type approvalRecord struct {
	Plan tasks.RunID `json:"plan"`
	Rows []string    `json:"rows"`
}

// Discard implements tasks.Client.Discard: Cancel's permission rule; a run
// that is not terminal, or whose goroutine still lives here, is ErrRunActive.
func (e *executor) Discard(ctx context.Context, id tasks.RunID) error {
	rec, err := e.runs.Get(ctx, id)
	if err != nil {
		return err
	}
	if e.held(rec.Kind) {
		return fmt.Errorf("%w: %s", ErrHeld, id)
	}
	if actor, who := e.caller(ctx); who != nil && (!e.moduleMayAct(who, rec, actor) || !e.mayCancel(rec, actor, who)) {
		return fmt.Errorf("%w: discard %s", ErrNotPermitted, id)
	}
	e.mu.Lock()
	_, live := e.running[id]
	e.mu.Unlock()
	if live {
		return fmt.Errorf("%w: %s is held by a live worker", tasks.ErrRunActive, id)
	}
	return e.write(ctx, func(ctx context.Context, b *storage.Batch) error {
		return e.runs.Delete(ctx, b, id)
	})
}
