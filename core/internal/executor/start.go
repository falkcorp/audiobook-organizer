// file: core/internal/executor/start.go
// version: 1.4.0
// guid: 89eb5ff3-93fb-4a22-9970-d55b28fbbe40
// last-edited: 2026-11-11

package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/internal/authz"
	"github.com/falkcorp/mediacore/internal/taskstore"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// systemMethod is the AuthMethod of a module system actor (SEC2-15).
const systemMethod = "system"

// starter is who makes a start: the caller's checker (nil: none) and, for
// rc.Enqueue, the definition of the run whose body makes it (permitStart).
type starter struct {
	who    rbac.Checker
	parent *def
}

// startMode is how a start resolves an unset mode.
type startMode uint8

const (
	modeDefault  startMode = iota // HTTP route / Client.Start: rule 2 (Preview for writers, Live for ReadOnly)
	modeRequired                  // StartIn: ErrModeUnset
	modeApprove                   // Approve's apply run: Live, and the one start that may name a plan
)

// Start implements tasks.Client.Start: the caller is stamped from ctx
// (Options.Principal) and the rule-2 mode default applies (5T.3.7).
func (e *executor) Start(ctx context.Context, kind tasks.Kind, req tasks.StartRequest) (tasks.RunRef, error) {
	who, err := e.stamp(ctx, &req)
	if err != nil {
		return tasks.RunRef{}, err
	}
	var ref tasks.RunRef
	err = e.startLocked(ctx, kind, req, starter{who: who}, modeDefault, func(prep prepared) error {
		return e.write(ctx, func(ctx context.Context, b *storage.Batch) error {
			var err error
			ref, err = e.stageStart(ctx, b, prep)
			return err
		})
	})
	if err != nil {
		return tasks.RunRef{}, err
	}
	e.wakeDispatcher()
	return ref, nil
}

// StartIn implements tasks.Stager/Executor.StartIn: the run is staged into
// the caller's b (inside the caller's WriteRuns), the actor is the system
// actor of the module that owns kind, and Mode must be set (5T.3.10,
// 5T.3.12; COR2-5).
func (e *executor) StartIn(ctx context.Context, b *storage.Batch, kind tasks.Kind, req tasks.StartRequest) (tasks.RunRef, error) {
	if b == nil {
		return tasks.RunRef{}, errors.New("executor: StartIn needs a batch")
	}
	req.Actor = tasks.Actor{AuthMethod: systemMethod, System: kind.Module()}
	req.IdempotencyKey = "" // the relay's same-batch cursor commit is its guarantee (SIMP3-8)
	prep, err := e.prepare(ctx, kind, req, starter{}, modeRequired)
	if err != nil {
		return tasks.RunRef{}, err
	}
	ref, err := e.stageStart(ctx, b, prep)
	if err == nil {
		e.wakeDispatcher()
	}
	return ref, err
}

// startLocked prepares and runs commit under startMu: every start but a
// plan's apply run may fold into an existing run (stageFold), and the
// dedupe read and the insert must not interleave in this process (the
// store's own guard keys cover the commit).
func (e *executor) startLocked(ctx context.Context, kind tasks.Kind, req tasks.StartRequest, s starter, m startMode, commit func(prepared) error) error {
	prep, err := e.prepare(ctx, kind, req, s, m)
	if err != nil {
		return err
	}
	e.startMu.Lock()
	defer e.startMu.Unlock()
	return commit(prep)
}

// stamp applies Options.Principal to req (COR-21): the actor in ctx
// replaces req.Actor. It returns the caller's checker (nil: none).
func (e *executor) stamp(ctx context.Context, req *tasks.StartRequest) (rbac.Checker, error) {
	if e.opts.Principal == nil {
		return nil, nil
	}
	actor, who, ok := e.opts.Principal(ctx)
	if !ok {
		return nil, nil
	}
	req.Actor = actor
	return who, nil
}

// prepared is a validated start.
type prepared struct {
	d      *def
	req    tasks.StartRequest
	mode   tasks.Mode
	params json.RawMessage
	prio   tasks.Priority
}

// prepare validates a start before anything is staged: the kind, held,
// the actor's permission, the mode, the params, the write set and a
// Fixer's plan reference.
func (e *executor) prepare(ctx context.Context, kind tasks.Kind, req tasks.StartRequest, s starter, m startMode) (prepared, error) {
	d, ok := e.defs[kind]
	if !ok {
		if e.held(kind) {
			return prepared{}, fmt.Errorf("%w: %s", ErrHeld, kind)
		}
		return prepared{}, fmt.Errorf("%w: %s", ErrUnknownKind, kind)
	}
	if e.held(kind) {
		return prepared{}, fmt.Errorf("%w: %s", ErrHeld, kind)
	}
	if err := e.permitStart(d, req.Actor, s); err != nil {
		return prepared{}, err
	}
	mode, ok := req.Mode.Mode()
	if !ok {
		if m != modeDefault {
			return prepared{}, tasks.ErrModeUnset
		}
		mode = tasks.ModePreview // rule 2: omitted mode is Preview for writers
		if d.readOnly {
			mode = tasks.ModeLive
		}
	}
	params := req.Params
	if len(params) == 0 {
		params = json.RawMessage("null")
	}
	if _, err := d.decode(params); err != nil {
		return prepared{}, err // 5T.5: not enqueued when validated at start
	}
	if _, err := e.writeSet(d, params); err != nil {
		return prepared{}, err
	}
	// A plan's apply run starts only through Approve (fail closed): no other
	// path may name a plan_ref, and a kind whose Approval requires a plan
	// cannot go Live any other way.
	switch {
	case m == modeApprove:
		if err := e.checkPlan(ctx, d, req.PlanRef); err != nil {
			return prepared{}, err
		}
	case req.PlanRef != "":
		return prepared{}, fmt.Errorf("%w: %s names plan %s", ErrApprovalRequired, d.kind, req.PlanRef)
	case mode == tasks.ModeLive && d.c.Approval != nil && d.c.Approval.PlanRequired:
		return prepared{}, fmt.Errorf("%w: %s", ErrPlanRequired, d.kind)
	}
	// PrioritySystem is core-only (tasks.PrioritySystem; 05-tasks 5T.3.1):
	// its runs take the reserved system workers, so a module kind may not
	// claim it, and a value past it would sort ahead of system runs in the
	// queue (^priority) while dispatch treats it as non-system.
	prio := req.Priority
	if err := permitPriority(d, prio); err != nil {
		return prepared{}, err
	}
	if prio == 0 {
		prio = d.c.Priority
	}
	if prio == 0 {
		prio = tasks.PriorityNormal
	}
	return prepared{d: d, req: req, mode: mode, params: params, prio: prio}, nil
}

// permitPriority refuses a priority past PrioritySystem on any kind and
// PrioritySystem on a module kind (zero: the definition's default).
func permitPriority(d *def, p tasks.Priority) error {
	switch {
	case p > tasks.PrioritySystem:
		return fmt.Errorf("%w: %d is not a priority", ErrInvalidPriority, p)
	case p == tasks.PrioritySystem && d.module != coreModule:
		return fmt.Errorf("%w: PrioritySystem is core-only and %s is a module kind", ErrInvalidPriority, d.kind)
	}
	return nil
}

// permitStart is the start permission rule. A module system actor may start
// only its module's kinds plus its manifest's TaskStarts, with the union of
// those kinds' Perms.Start minus core:admin and every AuthRequired scope
// (SEC2-15, COR3-10); any other actor needs s.who to satisfy Perms.Start
// when a checker is present. A child start (s.parent set, rc.Enqueue) is
// module code, so it must also pass the same rule for the module whose
// body makes it. The two rules intersect: when the parent's actor is a
// module system actor, the child must be within both modules' reach, so a
// module that starts another module's kind must list that kind's whole
// child closure in its own TaskStarts (fail closed). When the parent's
// actor is a user, only the module rule applies: the run's context carries
// no request principal, so the user's own grants cannot be checked there,
// and a child whose Perms.Start needs core:admin or an AuthRequired scope
// is started directly, never enqueued.
func (e *executor) permitStart(d *def, actor tasks.Actor, s starter) error {
	if p := s.parent; p != nil {
		if err := e.moduleReach(p.module, d); err != nil {
			return fmt.Errorf("%w (enqueued by %s)", err, p.kind)
		}
	}
	if actor.AuthMethod == systemMethod && actor.System != "" {
		return e.moduleReach(actor.System, d)
	}
	if s.who != nil && !s.who.Satisfies(d.c.Perms.Start) {
		return fmt.Errorf("%w: start %s", ErrNotPermitted, d.kind)
	}
	return nil
}

// moduleReach is SEC2-15's start rule for module: d is one of its own
// kinds or listed in its manifest's TaskStarts, and its system actor's
// checker holds d's start requirement.
func (e *executor) moduleReach(module string, d *def) error {
	if d.module != module && !slices.Contains(e.starts[module], d.kind) {
		return fmt.Errorf("%w: module %s may not start %s (not its kind, not in TaskStarts)", ErrNotPermitted, module, d.kind)
	}
	if !e.systemChecker(module).Satisfies(d.c.Perms.Start) {
		return fmt.Errorf("%w: module %s's system actor does not hold %s's start requirement", ErrNotPermitted, module, d.kind)
	}
	return nil
}

// systemChecker builds module's system actor checker: the union of the
// start requirements of its own kinds and its TaskStarts, minus core:admin
// and every scope the catalog marks AuthRequired (authz.ForSystem refuses
// the whole set if handed either).
func (e *executor) systemChecker(module string) rbac.Checker {
	var perms []rbac.Permission
	add := func(k tasks.Kind) {
		if d, ok := e.defs[k]; ok {
			perms = append(perms, d.c.Perms.Start.Permissions()...)
		}
	}
	for k, d := range e.defs {
		if d.module == module {
			add(k)
		}
	}
	for _, k := range e.starts[module] {
		add(k)
	}
	var keep []rbac.Permission
	for _, p := range perms {
		if p == rbac.CoreAdmin || slices.Contains(keep, p) {
			continue
		}
		if e.opts.Catalog != nil {
			if spec, ok := e.opts.Catalog.Lookup(p); ok && spec.AuthRequired {
				continue
			}
		}
		keep = append(keep, p)
	}
	return authz.ForSystem(e.opts.Catalog, keep)
}

// writeSet is a run's dispatch write set (5T.3.4): Subjects(params) when
// the definition narrows it, else its declared Effects.Writes. Subjects
// outside the declared write kinds are refused.
func (e *executor) writeSet(d *def, params json.RawMessage) ([]entity.Ref, error) {
	if d.c.Subjects == nil {
		return d.writes, nil
	}
	refs, err := d.c.Subjects(params)
	if err != nil {
		return nil, fmt.Errorf("executor: %s: Subjects: %w", d.kind, err)
	}
	for _, r := range refs {
		if !declared(d.writes, r) {
			return nil, fmt.Errorf("%w: %s: subject %s is outside Effects.Writes", tasks.ErrOutsideEffects, d.kind, tasks.FormatRef(r))
		}
	}
	return refs, nil
}

// declared reports whether r is inside a declared write set: a declared
// kind-wide ref covers every ID of its kind; an ID ref covers only itself.
func declared(set []entity.Ref, r entity.Ref) bool {
	for _, w := range set {
		if w.Module == r.Module && w.Kind == r.Kind && (w.ID == "" || w.ID == r.ID) {
			return true
		}
	}
	return false
}

// checkPlan is the Approval rule for a Live Fixer start (5T.3.7, SEC-24):
// with PlanRequired, PlanRef must name a succeeded Preview run of the same
// kind that finished within MaxPlanAge (0 = 7 days).
func (e *executor) checkPlan(ctx context.Context, d *def, ref tasks.RunID) error {
	a := d.c.Approval
	if ref == "" {
		return fmt.Errorf("%w: %s", ErrPlanRequired, d.kind)
	}
	plan, err := e.runs.Get(ctx, ref)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrPlanInvalid, err)
	}
	if plan.Kind != d.kind || plan.Mode != tasks.ModePreview || plan.State != tasks.StateSucceeded {
		return fmt.Errorf("%w: %s is a %s %s run, %s", ErrPlanInvalid, ref, plan.Mode, plan.Kind, plan.State)
	}
	maxAge := tasks.DefaultMaxPlanAge
	if a != nil && a.MaxPlanAge > 0 {
		maxAge = a.MaxPlanAge
	}
	if age := e.now().Sub(plan.Finished); age > maxAge {
		return fmt.Errorf("%w: %s finished %s ago (max %s)", ErrPlanTooOld, ref, age.Round(time.Second), maxAge)
	}
	return e.undecided(ctx, ref)
}

// The events Approve records on a plan run: its one decision.
const (
	eventApprove = "approve"
	eventReject  = "reject"
)

// planDecision is a plan run's decision: the first approve or reject event
// on it, or "" while it is undecided.
func (e *executor) planDecision(ctx context.Context, id tasks.RunID) (string, error) {
	q := tasks.PageQuery{Page: tasks.PageEvents, Limit: 1000}
	for {
		n := 0
		for p, err := range e.runs.Pages(ctx, id, q) {
			if err != nil {
				return "", err
			}
			n++
			q.After = p.Key
			if ev := p.Event.Event; ev == eventApprove || ev == eventReject {
				return ev, nil
			}
		}
		if n < 1000 {
			return "", nil
		}
	}
}

// undecided refuses a plan that already carries its decision: a rejected
// plan with ErrPlanRejected, an approved one with ErrPlanDecided.
func (e *executor) undecided(ctx context.Context, id tasks.RunID) error {
	dec, err := e.planDecision(ctx, id)
	switch {
	case err != nil:
		return err
	case dec == eventReject:
		return fmt.Errorf("%w: %s", ErrPlanRejected, id)
	case dec == eventApprove:
		return fmt.Errorf("%w: %s", ErrPlanDecided, id)
	}
	return nil
}

// idemRecord is an idem/<kind>/<key> value.
type idemRecord struct {
	Run     tasks.RunID `json:"run"`
	Expires time.Time   `json:"expires"`
}

// stageStart stages one validated start into b: an idempotency replay, a
// dedupe fold or merge into a queued run of the kind, or a fresh insert.
func (e *executor) stageStart(ctx context.Context, b *storage.Batch, p prepared) (tasks.RunRef, error) {
	kind := p.d.kind
	var idemKey []byte
	if p.req.IdempotencyKey != "" {
		if e.idem == nil {
			return tasks.RunRef{}, errors.New("executor: idempotency keys need Options.Keyspace")
		}
		idemKey = taskstore.IdemKey(string(kind), p.req.IdempotencyKey)
		v, found, err := e.idem.ReadForUpdate(ctx, b, idemKey)
		if err != nil {
			return tasks.RunRef{}, err
		}
		if found {
			var rec idemRecord
			if json.Unmarshal(v, &rec) == nil && e.now().Before(rec.Expires) {
				return tasks.RunRef{ID: rec.Run, Existing: true}, nil
			}
		}
	}
	// An apply run (PlanRef set: only Approve sets it) is always a new run:
	// it never folds into a queued or running run, and stageFold never folds
	// a start into one, so one plan's approval can never land on another's
	// run.
	if p.req.PlanRef == "" {
		ref, ok, err := e.stageFold(ctx, b, p)
		if err != nil || ok {
			if err == nil && idemKey != nil {
				err = e.stageIdem(b, idemKey, ref.ID)
			}
			return ref, err
		}
	}
	id, err := e.runs.NewID(ctx)
	if err != nil {
		return tasks.RunRef{}, err
	}
	rec := tasks.RunRecord{
		ID: id, Kind: kind, Mode: p.mode, State: tasks.StateQueued, Actor: p.req.Actor,
		Trigger: p.req.Trigger, Parent: p.req.Parent, PlanRef: p.req.PlanRef, Created: e.now(),
		Priority: p.prio, Params: p.params,
	}
	if rec.Trigger == "" {
		rec.Trigger = tasks.TriggerManual
	}
	if err := e.runs.Insert(ctx, b, rec); err != nil {
		return tasks.RunRef{}, err
	}
	if idemKey != nil {
		if err := e.stageIdem(b, idemKey, id); err != nil {
			return tasks.RunRef{}, err
		}
	}
	return tasks.RunRef{ID: id}, nil
}

func (e *executor) stageIdem(b *storage.Batch, key []byte, id tasks.RunID) error {
	v, err := json.Marshal(idemRecord{Run: id, Expires: e.now().Add(e.config().IdempotencyTTL)})
	if err != nil {
		return err
	}
	return e.idem.Set(b, key, v)
}

// stageFold folds a start into an unfinished run of the same kind and mode
// under the kind's DedupePolicy (05-tasks; v2 EnqueueOp's dedupe,
// DedupeQueuedRuns, DedupeSameActorOnly, MergeQueuedParams). The zero
// policy folds into a queued or running run whose params are
// byte-identical; AnyParams folds into a queued or running run whatever its
// params; Key folds only into a queued run with an equal key; Merge unions
// the start into a queued run (a queued→queued transition) and is never
// called for a running run, which takes the start only on identical params.
// SameActorOnly narrows every rule to runs of the same actor. Queued runs
// are tried first, in dispatch order, so with a Merge kind an identical
// start may merge into a queued run rather than fold into the running one.
// A fold into a running run is a read with no write: a run that ends
// between the read and the return is still handed back as Existing (v2
// parity). waiting_deps and stopping runs are not fold targets.
func (e *executor) stageFold(ctx context.Context, b *storage.Batch, p prepared) (tasks.RunRef, bool, error) {
	pol := p.d.c.Dedupe
	var want string
	if pol.Key != nil {
		k, err := pol.Key(p.params)
		if err != nil {
			return tasks.RunRef{}, false, fmt.Errorf("executor: %s: Dedupe.Key: %w", p.d.kind, err)
		}
		want = k
	}
	for cur, err := range e.runs.Queued(ctx) {
		if err != nil {
			continue // undecodable: hidden (5T.5)
		}
		if cur.Kind != p.d.kind || cur.Mode != p.mode || cur.PlanRef != "" {
			continue // a plan's apply run takes no fold (stageStart)
		}
		if pol.SameActorOnly && cur.Actor != p.req.Actor {
			continue
		}
		match := pol.AnyParams || pol.Merge != nil
		if !match && pol.Key != nil {
			k, err := pol.Key(cur.Params)
			match = err == nil && k == want
		}
		if !match && pol.Key == nil {
			match = bytes.Equal(cur.Params, p.params)
		}
		if !match {
			continue
		}
		if pol.Merge != nil {
			merged, ok, err := pol.Merge(cur.Params, p.params)
			if err != nil {
				return tasks.RunRef{}, false, fmt.Errorf("executor: %s: Dedupe.Merge: %w", p.d.kind, err)
			}
			if !ok {
				continue
			}
			next := cur
			next.Params = merged
			next.Seq.Event = cur.Seq.Event + 1
			want := tasks.Expect{State: cur.State, Attempt: cur.Attempt, FenceEpoch: cur.FenceEpoch}
			if err := e.runs.Transition(ctx, b, cur.ID, want, next); err != nil {
				return tasks.RunRef{}, false, err
			}
			ev := tasks.RunEvent{From: cur.State, To: cur.State, Event: "merge", At: e.now()}
			if err := e.runs.AppendEvent(ctx, b, cur.ID, next.Seq.Event, ev); err != nil {
				return tasks.RunRef{}, false, err
			}
		}
		return tasks.RunRef{ID: cur.ID, Existing: true}, true, nil
	}
	if pol.Key != nil {
		return tasks.RunRef{}, false, nil // a Key folds into a queued run only
	}
	// The kind's running runs: at most one per worker, so the limit is
	// never the bound.
	q := tasks.RunQuery{Kinds: []tasks.Kind{p.d.kind}, States: []tasks.RunState{tasks.StateRunning}, Limit: runningFoldLimit}
	for cur, err := range e.runs.List(ctx, q) {
		if err != nil {
			continue // undecodable: hidden (5T.5)
		}
		if cur.Kind != p.d.kind || cur.State != tasks.StateRunning || cur.Mode != p.mode || cur.PlanRef != "" {
			continue
		}
		if pol.SameActorOnly && cur.Actor != p.req.Actor {
			continue
		}
		if pol.AnyParams || bytes.Equal(cur.Params, p.params) {
			return tasks.RunRef{ID: cur.ID, Existing: true}, true, nil
		}
	}
	return tasks.RunRef{}, false, nil
}

// runningFoldLimit caps stageFold's read of a kind's running runs (the
// store's own cap); the worker count bounds them far below it.
const runningFoldLimit = 1000
