// file: core/internal/executor/control.go
// version: 1.2.0
// guid: e0da087e-cd1b-40dd-8ed9-97f8b015517f
// last-edited: 2026-11-10

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

// caller resolves Options.Principal for a Client call.
func (e *executor) caller(ctx context.Context) (tasks.Actor, rbac.Checker) {
	if e.opts.Principal == nil {
		return tasks.Actor{}, nil
	}
	a, who, ok := e.opts.Principal(ctx)
	if !ok {
		return tasks.Actor{}, nil
	}
	return a, who
}

// mayView applies a definition's View requirement (omitted: core:tasks.view).
func (e *executor) mayView(who rbac.Checker, kind tasks.Kind) bool {
	if who == nil {
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
	if _, who := e.caller(ctx); !e.mayView(who, rec.Kind) {
		return tasks.RunView{}, fmt.Errorf("%w: %s", tasks.ErrRunNotFound, id)
	}
	return e.view(rec), nil
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
			if !yield(e.view(rec), nil) {
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
	for v, err := range e.Runs(ctx, tasks.RunQuery{Kinds: q.Kinds, RunFilter: tasks.RunFilter{States: q.States, Limit: 1000}}) {
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

// Log implements tasks.Client.Log.
func (e *executor) Log(ctx context.Context, id tasks.RunID, q tasks.PageQuery) iter.Seq2[tasks.LogLine, error] {
	return func(yield func(tasks.LogLine, error) bool) {
		if _, err := e.Get(ctx, id); err != nil {
			yield(tasks.LogLine{}, err)
			return
		}
		q.Page = tasks.PageLog
		for p, err := range e.runs.Pages(ctx, id, q) {
			if !yield(p.Log, err) || err != nil {
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
		if !e.mayCancel(rec, actor, who) {
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
	} // else startAttempt applies stopCause before the body runs
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
// Perms.Start (the verified-method half is the HTTP route's, F2-05).
func (e *executor) Decide(ctx context.Context, id tasks.RunID, d tasks.Decision) error {
	actor, who := e.caller(ctx)
	if who != nil {
		rec, err := e.runs.Get(ctx, id)
		if err != nil {
			return err
		}
		def := e.defs[rec.Kind]
		if def == nil {
			return fmt.Errorf("%w: %s", ErrUnknownKind, rec.Kind)
		}
		req := def.c.Perms.Approve
		if req.IsZero() {
			req = def.c.Perms.Start
		}
		if !who.Satisfies(req) {
			return fmt.Errorf("%w: decide %s", ErrNotPermitted, id)
		}
	}
	return e.DecideAs(ctx, id, d, actor)
}

// DecideAs answers a run awaiting a decision (ResumeAsk): Resume re-queues
// it to continue from its checkpoint, Finish ends it succeeded without
// resuming, Drop ends it interrupted_dropped.
func (e *executor) DecideAs(ctx context.Context, id tasks.RunID, d tasks.Decision, by tasks.Actor) error {
	rec, err := e.runs.Get(ctx, id)
	if err != nil {
		return err
	}
	if e.held(rec.Kind) {
		return fmt.Errorf("%w: %s", ErrHeld, id)
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
			next.State, next.Reason = tasks.StateQueued, ""
			ev.Reason = "resume"
		case tasks.DecideFinish:
			next.State, next.Reason, next.Finished = tasks.StateSucceeded, "decided_finish", e.now()
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

// Approve answers a Fixer plan (5T.3.7): a rejection is recorded on the
// plan run; an approval starts the Live apply run of the same kind with the
// plan's params, PlanRef set and the approved row IDs staged with it, all
// in one batch. Rows must belong to the plan, which must be a succeeded
// Preview run within MaxPlanAge.
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
	// 5T.3.7: approve and reject need Perms.Approve (Perms.Start when a
	// non-Fixer kind leaves it unset, as Decide); the verified-method half
	// is the HTTP route's (F2-05). A caller with no identity is a
	// kernel-internal call and acts as no one: never as the plan's actor.
	actor, who := e.caller(ctx)
	if who != nil {
		need := d.c.Perms.Approve
		if need.IsZero() {
			need = d.c.Perms.Start
		}
		if !who.Satisfies(need) {
			return fmt.Errorf("%w: approve %s", ErrNotPermitted, id)
		}
	}
	if err := e.checkPlan(ctx, d, id); err != nil {
		return err
	}
	var by *tasks.Actor
	if actor != (tasks.Actor{}) {
		by = &actor
	}
	if req.Reject {
		_, err := e.move(ctx, id, func(cur tasks.RunRecord) (tasks.RunRecord, tasks.RunEvent, error) {
			return cur, tasks.RunEvent{Event: eventReject, Reason: req.Reason, Actor: by}, nil
		}, nil)
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
	prep, err := e.prepare(ctx, plan.Kind, start, who, modeApprove)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(rows)
	if err != nil {
		return err
	}
	err = e.write(ctx, func(ctx context.Context, b *storage.Batch) error {
		ref, err := e.stageStart(ctx, b, prep)
		if err != nil {
			return err
		}
		env, err := json.Marshal(ckptEnvelope{A: 1, V: raw})
		if err != nil {
			return err
		}
		return e.runs.SaveCheckpoint(ctx, b, ref.ID, ckptApproved, env)
	})
	if err == nil {
		e.wakeDispatcher()
	}
	return err
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
	if actor, who := e.caller(ctx); who != nil && !e.mayCancel(rec, actor, who) {
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
