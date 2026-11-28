// file: core/internal/executor/outbox_start_test.go
// version: 1.0.0
// guid: 7d2c4e19-3a85-4b60-9f1e-c58a0b6d2e73
// last-edited: 2026-11-28

package executor

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// The rig boots gotham's executor with kinds whose Start asks for
// core:system.manage (AuthRequired, so no module system actor can hold it, the
// shape of the settings.manage expansion):
//
//	gotham.sched      SystemStart set and a Schedule: the scheduler's kind
//	gotham.schedplain a Schedule and the same Start, flag unset
//	gotham.relocate   SystemStart set, no Schedule: the outbox consumer's kind
//	gotham.plain      the same Start, flag unset
//	gotham.parent     Start holdable by the system actor; SystemStart set and a
//	                  Schedule, so the scheduler may start it; its body runs enq
//	gotham.child      Start holdable: a child the user-started parent may enqueue
//	arkham.relocate   another module's kind with the flag set
//
// gotham lists arkham.relocate in TaskStarts when listed is true.
type systemStartRig struct {
	*harness
	mu    sync.Mutex
	enq   func(rc *tasks.RC) (tasks.RunRef, error)
	enqed []enqResult
}

type enqResult struct {
	ref tasks.RunRef
	err error
}

const sysStartReason = "a start the executor began on the system's behalf"

func newSystemStartRig(t *testing.T, listed bool) *systemStartRig {
	t.Helper()
	r := &systemStartRig{}
	manage := func(c *tasks.Common) {
		c.Perms.Start = rbac.AllOf("core:tasks.manage", "core:system.manage", "gotham:run")
	}
	flagged := func(c *tasks.Common) {
		manage(c)
		c.Perms.SystemStart = sysStartReason
	}
	scheduled := func(c *tasks.Common) {
		c.Schedule = tasks.ScheduleFromConfig("modules.gotham.test.schedules.fire").Live()
	}
	holdable := func(c *tasks.Common) { c.Perms.Start = rbac.Need("gotham:run") }
	parentBody := func(rc *tasks.RC, _ noParams) (string, error) {
		r.mu.Lock()
		f := r.enq
		r.mu.Unlock()
		if f != nil {
			ref, err := f(rc)
			r.mu.Lock()
			r.enqed = append(r.enqed, enqResult{ref, err})
			r.mu.Unlock()
		}
		return "closed", nil
	}
	var starts []tasks.Kind
	if listed {
		starts = []tasks.Kind{"arkham.relocate"}
	}
	r.harness = newHarness(t, []tasks.Definition{
		taskDef("gotham.sched", okBody, flagged, scheduled),
		taskDef("gotham.schedplain", okBody, manage, scheduled),
		taskDef("gotham.relocate", okBody, flagged),
		taskDef("gotham.plain", okBody, manage),
		taskDef("gotham.parent", parentBody, holdable, scheduled, func(c *tasks.Common) { c.Perms.SystemStart = sysStartReason }),
		taskDef("gotham.child", okBody, holdable),
		taskDef("arkham.relocate", okBody, flagged),
		taskDef("gotham.operator", okBody, func(c *tasks.Common) {
			c.Perms.Start = rbac.OperatorRequirement()
			c.Perms.SystemStart = sysStartReason
		}, scheduled),
	}, func(o *Options) {
		withPrincipal(o)
		o.Catalog = catalogWith(t, "gotham:run", "arkham:run")
		o.Manifests = []Manifest{{Module: "gotham", Enabled: true, TaskStarts: starts}, {Module: "arkham", Enabled: true}}
	})
	return r
}

// enqueue sets the body of gotham.parent for the next run and returns the
// results its Enqueue calls saw once the run ended.
func (r *systemStartRig) enqueueFrom(f func(rc *tasks.RC) (tasks.RunRef, error)) {
	r.mu.Lock()
	r.enq, r.enqed = f, nil
	r.mu.Unlock()
}

func (r *systemStartRig) results() []enqResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]enqResult(nil), r.enqed...)
}

var gothamSystem = tasks.Actor{AuthMethod: systemMethod, System: "gotham"}

// stageVia is the handle's StartIn in a relay-shaped batch.
func (r *systemStartRig) stageVia(st tasks.Stager, kind tasks.Kind, trigger tasks.Trigger) (tasks.RunRef, error) {
	var ref tasks.RunRef
	err := r.e.write(context.Background(), func(ctx context.Context, b *storage.Batch) error {
		var err error
		ref, err = st.StartIn(ctx, b, kind, tasks.StartRequest{Mode: tasks.ModeRequestLive, Trigger: trigger})
		return err
	})
	return ref, err
}

func (r *systemStartRig) scheduleHandle(t *testing.T) tasks.Stager {
	t.Helper()
	st, err := ScheduleStager(r.x)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func (r *systemStartRig) outboxHandle(t *testing.T) tasks.Stager {
	t.Helper()
	st, err := OutboxRuns(r.x)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// TestSystemStart_ScheduleOrigin: the scheduler's start of a kind that carries
// SystemStart and declares a Schedule goes through as the owner's system actor
// although that actor cannot hold Start, and the run records its origin. A
// flagged kind with no Schedule, and a scheduled kind with no flag, are refused.
func TestSystemStart_ScheduleOrigin(t *testing.T) {
	r := newSystemStartRig(t, false)
	ref, err := r.stageVia(r.scheduleHandle(t), "gotham.sched", tasks.TriggerSchedule)
	if err != nil {
		t.Fatalf("the scheduler's start of a flagged scheduled kind: %v, want it started", err)
	}
	rec := r.get(ref.ID)
	if rec.Actor != gothamSystem || rec.Origin != recordedSchedule {
		t.Fatalf("run = actor %+v origin %q, want gotham's system actor and %q", rec.Actor, rec.Origin, recordedSchedule)
	}
	if _, err := r.stageVia(r.scheduleHandle(t), "gotham.relocate", tasks.TriggerSchedule); !errors.Is(err, ErrNotPermitted) {
		t.Errorf("the scheduler's start of a flagged kind that declares no Schedule: %v, want ErrNotPermitted", err)
	}
	if _, err := r.stageVia(r.scheduleHandle(t), "gotham.schedplain", tasks.TriggerSchedule); !errors.Is(err, ErrNotPermitted) {
		t.Errorf("the scheduler's start of a scheduled kind with no flag: %v, want ErrNotPermitted", err)
	}
}

// TestSystemStart_OutboxOrigin: the relay's start of a flagged kind goes through
// as the owner's system actor, and an unflagged one is refused, as it was before
// the rule existed.
func TestSystemStart_OutboxOrigin(t *testing.T) {
	r := newSystemStartRig(t, false)
	ref, err := r.stageVia(r.outboxHandle(t), "gotham.relocate", tasks.TriggerOutbox)
	if err != nil {
		t.Fatalf("the relay's start of a flagged kind: %v, want it started", err)
	}
	rec := r.get(ref.ID)
	if rec.Actor != gothamSystem || rec.Origin != recordedOutbox {
		t.Fatalf("run = actor %+v origin %q, want gotham's system actor and %q", rec.Actor, rec.Origin, recordedOutbox)
	}
	if _, err := r.stageVia(r.outboxHandle(t), "gotham.plain", tasks.TriggerOutbox); !errors.Is(err, ErrNotPermitted) {
		t.Errorf("the relay's start of an unflagged kind: %v, want ErrNotPermitted", err)
	}
}

// TestSystemStart_NoOriginWithoutAMintedHandle: no exported surface but the two
// minted handles carries an origin. Executor.StartIn and Stager.StartIn as the
// kernel wires them elsewhere, whatever Trigger the request names, the module
// client, a StartRequest that claims the system actor, and a person or an API key
// are all judged on Start alone: a flagged kind whose Start they lack is refused.
func TestSystemStart_NoOriginWithoutAMintedHandle(t *testing.T) {
	r := newSystemStartRig(t, false)
	for _, kind := range []tasks.Kind{"gotham.sched", "gotham.relocate"} {
		for _, trig := range []tasks.Trigger{tasks.TriggerSchedule, tasks.TriggerWindow, tasks.TriggerOutbox, tasks.TriggerManual, tasks.TriggerParent, ""} {
			if _, err := r.stageVia(r.x, kind, trig); !errors.Is(err, ErrNotPermitted) {
				t.Errorf("Executor.StartIn of %s naming trigger %q: %v, want ErrNotPermitted", kind, trig, err)
			}
		}
		mc, err := ModuleClient(r.x, "gotham")
		if err != nil {
			t.Fatal(err)
		}
		req := tasks.StartRequest{Mode: tasks.ModeRequestLive, Trigger: tasks.TriggerOutbox}
		if _, err := mc.Start(context.Background(), kind, req); !errors.Is(err, ErrNotPermitted) {
			t.Errorf("the module client's start of %s naming Trigger outbox: %v, want ErrNotPermitted", kind, err)
		}
		// A request that claims the system actor in its Actor field, with no
		// principal in the context, is still a start with no origin.
		claim := tasks.StartRequest{Mode: tasks.ModeRequestLive, Trigger: tasks.TriggerSchedule, Actor: gothamSystem}
		if _, err := r.x.Start(context.Background(), kind, claim); !errors.Is(err, ErrNotPermitted) {
			t.Errorf("Client.Start of %s claiming the system actor: %v, want ErrNotPermitted", kind, err)
		}
		// A person, and an API key (a token principal), without Start.
		for _, who := range []tasks.Actor{{UserID: "gordon", AuthMethod: "session"}, {UserID: "gordon", AuthMethod: "token"}} {
			ctx := context.WithValue(context.Background(), callerKey{}, callerID{who, grants{"gotham:run": true}})
			if _, err := r.x.Start(ctx, kind, tasks.StartRequest{Mode: tasks.ModeRequestLive}); !errors.Is(err, ErrNotPermitted) {
				t.Errorf("a %s start of %s lacking Start: %v, want ErrNotPermitted", who.AuthMethod, kind, err)
			}
		}
		holder := asUser("barbara", "core:tasks.manage", "core:system.manage", "gotham:run")
		if _, err := r.x.Start(holder, kind, tasks.StartRequest{Mode: tasks.ModeRequestLive}); err != nil {
			t.Errorf("a person holding Start of %s: %v, want started", kind, err)
		}
	}
}

// TestSystemStart_OtherModulesActorIsRefused: the exemption belongs to the module
// that owns the kind. gotham's system actor on any origin is refused for
// arkham's flagged kind, whether or not gotham lists it in TaskStarts; arkham's
// own actor on the relay's origin is not.
func TestSystemStart_OtherModulesActorIsRefused(t *testing.T) {
	for _, listed := range []bool{false, true} {
		r := newSystemStartRig(t, listed)
		d := r.e.defs["arkham.relocate"]
		for _, o := range []startOrigin{{kind: originSchedule}, {kind: originOutbox}} {
			if err := r.e.permitStart(d, gothamSystem, starter{origin: o}); !errors.Is(err, ErrNotPermitted) {
				t.Errorf("TaskStarts listed=%v origin %v: gotham's actor for arkham's kind: %v, want ErrNotPermitted", listed, o.kind, err)
			}
		}
		arkham := tasks.Actor{AuthMethod: systemMethod, System: "arkham"}
		if err := r.e.permitStart(d, arkham, starter{origin: startOrigin{kind: originOutbox}}); err != nil {
			t.Errorf("TaskStarts listed=%v: the owner's actor on the relay's origin: %v, want permitted", listed, err)
		}
	}
}

// TestSystemStart_ChildOfUserStartedParent: a child enqueued by a run a person
// started keeps the person's actor and gets no origin, so a flagged kind the
// system actor cannot hold is refused as it is at HEAD (permitStart's parent rule:
// a child whose Start needs an AuthRequired scope is started directly, never
// enqueued). A holdable child still runs, as that person.
func TestSystemStart_ChildOfUserStartedParent(t *testing.T) {
	r := newSystemStartRig(t, false)
	r.enqueueFrom(func(rc *tasks.RC) (tasks.RunRef, error) { return rc.Enqueue("gotham.relocate", noParams{}) })
	user := asUser("barbara", "gotham:run")
	ref, err := r.x.Start(user, "gotham.parent", tasks.StartRequest{Mode: tasks.ModeRequestLive})
	if err != nil {
		t.Fatalf("user start of the parent: %v", err)
	}
	parent := r.waitState(ref.ID, tasks.StateSucceeded)
	if parent.Origin != "" {
		t.Fatalf("a person's run records origin %q, want none", parent.Origin)
	}
	res := r.results()
	if len(res) != 1 || !errors.Is(res[0].err, ErrNotPermitted) {
		t.Fatalf("a user-started parent's enqueue of a flagged kind: %+v, want ErrNotPermitted", res)
	}

	r.enqueueFrom(func(rc *tasks.RC) (tasks.RunRef, error) { return rc.Enqueue("gotham.child", noParams{}) })
	ref, err = r.x.Start(user, "gotham.parent", tasks.StartRequest{Mode: tasks.ModeRequestLive})
	if err != nil {
		t.Fatal(err)
	}
	r.waitState(ref.ID, tasks.StateSucceeded)
	res = r.results()
	if len(res) != 1 || res[0].err != nil {
		t.Fatalf("a user-started parent's enqueue of a holdable child: %+v, want started", res)
	}
	child := r.get(res[0].ref.ID)
	if child.Actor != (tasks.Actor{UserID: "barbara", AuthMethod: "session"}) || child.Origin != "" {
		t.Fatalf("child = actor %+v origin %q, want the user's actor and no origin", child.Actor, child.Origin)
	}
}

// TestSystemStart_ChildOfSystemStartedParent: a child a run the system started
// (the scheduler's tick) enqueues, of a flagged kind of the SAME module, is
// admitted as the module's system actor and records "child:<parent>". A flagged
// kind of another module is refused, listed in TaskStarts or not.
func TestSystemStart_ChildOfSystemStartedParent(t *testing.T) {
	r := newSystemStartRig(t, true)
	r.enqueueFrom(func(rc *tasks.RC) (tasks.RunRef, error) { return rc.Enqueue("gotham.relocate", noParams{}) })
	ref, err := r.stageVia(r.scheduleHandle(t), "gotham.parent", tasks.TriggerSchedule)
	if err != nil {
		t.Fatalf("the scheduler's start of the parent: %v", err)
	}
	r.waitState(ref.ID, tasks.StateSucceeded)
	res := r.results()
	if len(res) != 1 || res[0].err != nil {
		t.Fatalf("a system-started parent's enqueue of a flagged same-module kind: %+v, want started", res)
	}
	child := r.get(res[0].ref.ID)
	if child.Actor != gothamSystem || child.Origin != recordedChild+string(ref.ID) {
		t.Fatalf("child = actor %+v origin %q, want gotham's system actor and %q", child.Actor, child.Origin, recordedChild+string(ref.ID))
	}

	r.enqueueFrom(func(rc *tasks.RC) (tasks.RunRef, error) { return rc.Enqueue("arkham.relocate", noParams{}) })
	ref, err = r.stageVia(r.scheduleHandle(t), "gotham.parent", tasks.TriggerSchedule)
	if err != nil {
		t.Fatal(err)
	}
	r.waitState(ref.ID, tasks.StateSucceeded)
	if res := r.results(); len(res) != 1 || !errors.Is(res[0].err, ErrNotPermitted) {
		t.Fatalf("a system-started parent's enqueue of another module's flagged kind: %+v, want ErrNotPermitted", res)
	}
}

// TestSystemStart_ModuleClientParentIsNoSystemOrigin: a run a module client
// started is stamped with the module's system actor but has no origin, so its
// children get no exemption. Without this the module's own Client.Start of a
// holdable kind would be a way to the exemption for whatever that kind enqueues.
func TestSystemStart_ModuleClientParentIsNoSystemOrigin(t *testing.T) {
	r := newSystemStartRig(t, false)
	r.enqueueFrom(func(rc *tasks.RC) (tasks.RunRef, error) { return rc.Enqueue("gotham.relocate", noParams{}) })
	mc, err := ModuleClient(r.x, "gotham")
	if err != nil {
		t.Fatal(err)
	}
	ref, err := mc.Start(context.Background(), "gotham.parent", tasks.StartRequest{Mode: tasks.ModeRequestLive})
	if err != nil {
		t.Fatalf("the module client's start of the parent: %v", err)
	}
	if got := r.waitState(ref.ID, tasks.StateSucceeded); got.Actor != gothamSystem || got.Origin != "" {
		t.Fatalf("parent = actor %+v origin %q, want the system actor and no origin", got.Actor, got.Origin)
	}
	if res := r.results(); len(res) != 1 || !errors.Is(res[0].err, ErrNotPermitted) {
		t.Fatalf("the module-client-started parent's enqueue of a flagged kind: %+v, want ErrNotPermitted", res)
	}
}

// TestSystemStart_RowOriginAloneGrantsNothing: a row's Origin is stored data an
// importer can write, so it is one input to the rule and never a grant. A parent
// whose actor is a person but whose row names a system origin, and a system actor
// of another module, take no exemption.
func TestSystemStart_RowOriginAloneGrantsNothing(t *testing.T) {
	r := newSystemStartRig(t, false)
	d := r.e.defs["gotham.relocate"]
	parent := &def{module: "gotham"}
	person := tasks.Actor{UserID: "barbara", AuthMethod: "session"}
	arkham := tasks.Actor{AuthMethod: systemMethod, System: "arkham"}
	for _, a := range []tasks.Actor{person, arkham} {
		s := starter{parent: parent, parentOrigin: recordedSchedule, parentRun: "r1"}
		if err := r.e.permitStart(d, a, s); !errors.Is(err, ErrNotPermitted) {
			t.Errorf("actor %+v, parent row naming a system origin: %v, want ErrNotPermitted", a, err)
		}
	}
	if err := r.e.permitStart(d, gothamSystem, starter{parent: parent, parentOrigin: recordedSchedule, parentRun: "r1"}); err != nil {
		t.Errorf("gotham's system actor, a same-module parent with a system origin: %v, want permitted", err)
	}
	if err := r.e.permitStart(d, gothamSystem, starter{parent: parent, parentOrigin: "", parentRun: "r1"}); !errors.Is(err, ErrNotPermitted) {
		t.Errorf("gotham's system actor, a parent with no origin: %v, want ErrNotPermitted", err)
	}
}

// TestCheckSystemStart is the P7 check's question, answered by the rule StartIn
// and rc.Enqueue apply, for each of the three origins: an unregistered kind, an
// unflagged one, a kind of another module and (schedule only) a flagged kind with
// no Schedule are each refused, the flagged own kind is not.
func TestCheckSystemStart(t *testing.T) {
	r := newSystemStartRig(t, true)
	cells := []struct {
		name   string
		from   CheckOrigin
		module string
		kind   tasks.Kind
		want   error
	}{
		{"schedule: flagged scheduled kind", CheckSchedule, "gotham", "gotham.sched", nil},
		{"schedule: flagged kind with no Schedule", CheckSchedule, "gotham", "gotham.relocate", ErrNotPermitted},
		{"schedule: scheduled kind with no flag", CheckSchedule, "gotham", "gotham.schedplain", ErrNotPermitted},
		{"schedule: unregistered kind", CheckSchedule, "gotham", "gotham.missing", ErrUnknownKind},
		{"outbox: flagged own kind", CheckOutbox, "gotham", "gotham.relocate", nil},
		{"outbox: unflagged own kind", CheckOutbox, "gotham", "gotham.plain", ErrNotPermitted},
		{"outbox: unregistered kind", CheckOutbox, "gotham", "gotham.missing", ErrUnknownKind},
		{"outbox: another module's kind, listed in TaskStarts", CheckOutbox, "gotham", "arkham.relocate", ErrNotPermitted},
		{"outbox: the owner of the other module's kind", CheckOutbox, "arkham", "arkham.relocate", nil},
		{"enqueue: flagged own kind", CheckEnqueue, "gotham", "gotham.relocate", nil},
		{"enqueue: unflagged own kind", CheckEnqueue, "gotham", "gotham.plain", ErrNotPermitted},
		{"enqueue: unregistered kind", CheckEnqueue, "gotham", "gotham.missing", ErrUnknownKind},
		{"enqueue: another module's kind", CheckEnqueue, "gotham", "arkham.relocate", ErrNotPermitted},
	}
	for _, c := range cells {
		err := CheckSystemStart(r.x, c.module, c.kind, c.from)
		switch {
		case c.want == nil && err != nil:
			t.Errorf("%s: %v, want nil", c.name, err)
		case c.want != nil && !errors.Is(err, c.want):
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
	}
	if err := CheckSystemStart(r.x, "gotham", "gotham.sched", CheckOrigin(0)); err == nil {
		t.Error("an unknown origin passed")
	}
}

// TestSystemStart_EachConditionOfTheRuleIsNeeded asks admitsSystemStart itself,
// one condition at a time. permitStart and moduleReach repeat the own-module
// condition, so through StartIn alone a guard that is dropped from this
// function is covered by the layer below; here each one is the only thing
// standing.
func TestSystemStart_EachConditionOfTheRuleIsNeeded(t *testing.T) {
	r := newSystemStartRig(t, false)
	own, other := r.e.defs["gotham.relocate"], r.e.defs["arkham.relocate"]
	plain, scheduled := r.e.defs["gotham.plain"], r.e.defs["gotham.sched"]
	relay, tick := starter{origin: startOrigin{kind: originOutbox}}, starter{origin: startOrigin{kind: originSchedule}}
	arkham := tasks.Actor{AuthMethod: systemMethod, System: "arkham"}
	person := tasks.Actor{UserID: "barbara", AuthMethod: "session"}
	// A hand-built actor that names a module without being a system actor.
	spoof := tasks.Actor{UserID: "barbara", AuthMethod: "session", System: "gotham"}
	childOf := func(m string, origin string) starter {
		return starter{parent: &def{module: m}, parentOrigin: origin, parentRun: "r1"}
	}
	cells := []struct {
		name string
		s    starter
		d    *def
		a    tasks.Actor
		want bool
	}{
		{"relay, own flagged kind, its system actor", relay, own, gothamSystem, true},
		{"the flag is needed", relay, plain, gothamSystem, false},
		{"the kind's own module is needed", relay, other, gothamSystem, false},
		{"the owner's actor is admitted for its own kind", relay, other, arkham, true},
		{"a person is no system actor", relay, own, person, false},
		{"a non-system actor that names a module is no system actor", relay, own, spoof, false},
		{"schedule, flagged scheduled kind", tick, scheduled, gothamSystem, true},
		{"the schedule origin needs a Schedule", tick, own, gothamSystem, false},
		{"no origin", starter{}, own, gothamSystem, false},
		{"child of a system-origin parent of the module", childOf("gotham", recordedSchedule), own, gothamSystem, true},
		{"child of an outbox-origin parent of the module", childOf("gotham", recordedOutbox), own, gothamSystem, true},
		{"child of a system-started child", childOf("gotham", recordedChild+"r0"), own, gothamSystem, true},
		{"a parent with no origin is a person's or a client's run", childOf("gotham", ""), own, gothamSystem, false},
		{"a parent of another module", childOf("arkham", recordedSchedule), own, gothamSystem, false},
		{"a row origin of a made-up kind", childOf("gotham", "console"), own, gothamSystem, false},
	}
	for _, c := range cells {
		if got := c.s.admitsSystemStart(c.d, c.a); got != c.want {
			t.Errorf("%s: admitsSystemStart = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestSystemStart_OperatorRequirementIsAdmittedPerOrigin: a kind whose Start is
// the seven-scope operator requirement (the settings.manage atom's core scopes,
// core:system.manage among them) is started by its module's system actor from
// each origin although that actor can not hold it, and a person still needs every
// scope: the flag lifts nothing for a start with no origin.
func TestSystemStart_OperatorRequirementIsAdmittedPerOrigin(t *testing.T) {
	r := newSystemStartRig(t, false)
	if _, err := r.stageVia(r.scheduleHandle(t), "gotham.operator", tasks.TriggerSchedule); err != nil {
		t.Errorf("schedule origin: %v, want started", err)
	}
	if err := CheckSystemStart(r.x, "gotham", "gotham.operator", CheckOutbox); err != nil {
		t.Errorf("outbox origin: %v, want admitted", err)
	}
	r.enqueueFrom(func(rc *tasks.RC) (tasks.RunRef, error) { return rc.Enqueue("gotham.operator", noParams{}) })
	ref, err := r.stageVia(r.scheduleHandle(t), "gotham.parent", tasks.TriggerSchedule)
	if err != nil {
		t.Fatal(err)
	}
	r.waitState(ref.ID, tasks.StateSucceeded)
	if res := r.results(); len(res) != 1 || res[0].err != nil {
		t.Errorf("enqueue origin (a child of a system-started run): %+v, want started", res)
	}
	all := []rbac.Permission{"core:settings.manage", "core:tasks.manage", "core:system.view", "core:system.manage",
		"core:library.manage", "core:library.browse", "core:integrations.manage"}
	if _, err := r.x.Start(asUser("barbara", all...), "gotham.operator", tasks.StartRequest{Mode: tasks.ModeRequestLive}); err != nil {
		t.Errorf("a holder of the seven scopes: %v, want started", err)
	}
	for i := range all {
		fewer := append(append([]rbac.Permission{}, all[:i]...), all[i+1:]...)
		if _, err := r.x.Start(asUser("barbara", fewer...), "gotham.operator", tasks.StartRequest{Mode: tasks.ModeRequestLive}); !errors.Is(err, ErrNotPermitted) {
			t.Errorf("a person lacking %s: %v, want ErrNotPermitted", all[i], err)
		}
	}
}
