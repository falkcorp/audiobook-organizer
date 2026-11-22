// file: core/internal/executor/module_client_test.go
// version: 1.0.0
// guid: 3f8a2d61-95b7-4e0c-a4d3-6c1e7b905a28
// last-edited: 2026-11-22

package executor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// moduleHarness boots gotham (a module with arkham.cleanup in TaskStarts)
// over fixture kinds and returns gotham's ModuleClient.
func moduleHarness(t *testing.T, starts []tasks.Kind) (*harness, tasks.Client) {
	t.Helper()
	purge := func(c *tasks.Common) { c.Perms.Start = rbac.Need("core:system.manage") } // AuthRequired
	defs := []tasks.Definition{
		taskDef("gotham.dispatch", blockUntilCtx(nil)),
		taskDef("gotham.signal", okBody),
		taskDef("arkham.cleanup", okBody),
		taskDef("arkham.transfer", okBody),
		taskDef("arkham.purge", okBody, purge),
	}
	h := newHarness(t, defs, func(o *Options) {
		withPrincipal(o)
		o.Catalog = catalogWith(t, "gotham:run", "arkham:run")
		o.Manifests = []Manifest{{Module: "gotham", Enabled: true, TaskStarts: starts}}
	})
	c, err := ModuleClient(h.x, "gotham")
	if err != nil {
		t.Fatalf("ModuleClient: %v", err)
	}
	return h, c
}

// TestModuleClient_StartsAsTheModulesSystemActor (COR-21, SEC2-15): outside a
// request the run's actor is the module's system actor whatever req.Actor the
// caller set, and the reach is the module's own kinds plus its TaskStarts.
func TestModuleClient_StartsAsTheModulesSystemActor(t *testing.T) {
	arkhamSystem := tasks.Actor{AuthMethod: systemMethod, System: "arkham"}
	cells := []struct {
		kind    tasks.Kind
		starts  []tasks.Kind
		allowed bool
	}{
		{kind: "gotham.signal", allowed: true},
		{kind: "arkham.cleanup"},
		{kind: "arkham.cleanup", starts: []tasks.Kind{"arkham.cleanup"}, allowed: true},
		{kind: "arkham.transfer", starts: []tasks.Kind{"arkham.cleanup"}},
		{kind: "arkham.purge", starts: []tasks.Kind{"arkham.purge"}},
	}
	for _, c := range cells {
		t.Run(string(c.kind)+"/"+strings.Join(kindStrings(c.starts), ","), func(t *testing.T) {
			h, mc := moduleHarness(t, c.starts)
			// A spoofed actor changes nothing: arkham's own system actor may
			// start arkham.transfer, gotham's may not.
			ref, err := mc.Start(context.Background(), c.kind, tasks.StartRequest{Mode: tasks.ModeRequestLive, Actor: arkhamSystem})
			switch {
			case c.allowed && err != nil:
				t.Fatalf("start %s: %v, want allowed", c.kind, err)
			case !c.allowed && !errors.Is(err, ErrNotPermitted):
				t.Fatalf("start %s: %v, want ErrNotPermitted", c.kind, err)
			case c.allowed:
				if got := h.get(ref.ID).Actor; got != (tasks.Actor{AuthMethod: systemMethod, System: "gotham"}) {
					t.Fatalf("the run's actor = %+v, want gotham's system actor", got)
				}
			}
		})
	}
}

// TestModuleClient_RequestPrincipalWins: a call inside a request acts as the
// request's user, checked against what that user holds, not the module's reach.
func TestModuleClient_RequestPrincipalWins(t *testing.T) {
	h, mc := moduleHarness(t, nil)
	// Gordon holds arkham:run, so he may start arkham.cleanup through gotham's
	// client although gotham's own reach does not include it.
	ref, err := mc.Start(asUser("gordon", "arkham:run"), "arkham.cleanup", tasks.StartRequest{Mode: tasks.ModeRequestLive})
	if err != nil {
		t.Fatalf("a user holding arkham:run starting arkham.cleanup: %v", err)
	}
	if got := h.get(ref.ID).Actor.UserID; got != "gordon" {
		t.Fatalf("the run's actor = %q, want gordon", got)
	}
	// And without the scope the user is refused, whatever gotham's reach.
	if _, err := mc.Start(asUser("robin", "gotham:run"), "arkham.cleanup", tasks.StartRequest{Mode: tasks.ModeRequestLive}); !errors.Is(err, ErrNotPermitted) {
		t.Fatalf("a user without arkham:run: %v, want ErrNotPermitted", err)
	}
}

// TestModuleClient_ControlAsTheSystemActor: outside a request Cancel applies
// the route's rule to the module's system actor (its own runs and kinds it may
// start), Pause needs core:tasks.manage, which that actor does not hold, and
// the module reads the runs of its own kinds but no other module's.
func TestModuleClient_ControlAsTheSystemActor(t *testing.T) {
	h, mc := moduleHarness(t, nil)
	ctx := context.Background()
	own, err := mc.Start(ctx, "gotham.dispatch", tasks.StartRequest{Mode: tasks.ModeRequestLive})
	if err != nil {
		t.Fatalf("start gotham.dispatch: %v", err)
	}
	h.waitState(own.ID, tasks.StateRunning)
	if _, err := mc.Get(ctx, own.ID); err != nil {
		t.Fatalf("the module reading its own run: %v", err)
	}
	if err := mc.Pause(ctx, "the module may not"); !errors.Is(err, ErrNotPermitted) {
		t.Fatalf("Pause as a module system actor: %v, want ErrNotPermitted", err)
	}
	// A run of another module's kind, started by a user, is neither readable
	// nor cancellable by gotham: its system actor holds neither core:tasks.view
	// nor arkham's start scope.
	foreign, err := h.x.Start(asUser("gordon", "arkham:run"), "arkham.cleanup", tasks.StartRequest{Mode: tasks.ModeRequestLive})
	if err != nil {
		t.Fatalf("start arkham.cleanup: %v", err)
	}
	h.waitState(foreign.ID, tasks.StateSucceeded)
	if _, err := mc.Get(ctx, foreign.ID); !errors.Is(err, tasks.ErrRunNotFound) {
		t.Fatalf("a module reading another module's run: %v, want ErrRunNotFound", err)
	}
	if err := mc.Cancel(ctx, own.ID); err != nil {
		t.Fatalf("the module canceling its own run: %v", err)
	}
	h.waitState(own.ID, tasks.StateCanceled)
}

// TestModuleClient_NotAnExecutor: ModuleClient refuses what is not this
// package's executor and an empty module, instead of returning a client that
// would act as nobody.
func TestModuleClient_NotAnExecutor(t *testing.T) {
	h, _ := moduleHarness(t, nil)
	if _, err := ModuleClient(h.x, ""); err == nil {
		t.Fatal("ModuleClient accepted an empty module")
	}
	if _, err := ModuleClient(nil, "gotham"); err == nil {
		t.Fatal("ModuleClient accepted a nil executor")
	}
}

// opsHarness is moduleHarness plus the kinds the operation-gate cells need:
// arkham.shared, a foreign kind that (as no kernel-registered kind may) starts
// under gotham's own scope, so that gotham's permissions alone would let it act
// on a run and only the kind gate refuses; and arkham.manage, a listed kind
// that starts under core:tasks.manage.
func opsHarness(t *testing.T, starts []tasks.Kind) (*harness, tasks.Client) {
	t.Helper()
	gothamScope := func(c *tasks.Common) { c.Perms.Start = rbac.Need("gotham:run") }
	manage := func(c *tasks.Common) { c.Perms.Start = rbac.Need("core:tasks.manage") }
	defs := []tasks.Definition{
		taskDef("gotham.dispatch", blockUntilCtx(nil)),
		taskDef("gotham.signal", okBody),
		taskDef("arkham.cleanup", okBody),
		taskDef("arkham.shared", blockUntilCtx(nil), gothamScope),
		taskDef("arkham.shared-done", okBody, gothamScope),
		taskDef("arkham.manage", okBody, manage),
	}
	h := newHarness(t, defs, func(o *Options) {
		withPrincipal(o)
		o.Catalog = catalogWith(t, "gotham:run", "arkham:run")
		o.Manifests = []Manifest{{Module: "gotham", Enabled: true, TaskStarts: starts}}
	})
	c, err := ModuleClient(h.x, "gotham")
	if err != nil {
		t.Fatalf("ModuleClient: %v", err)
	}
	return h, c
}

// TestModuleClient_OperationsGatedByKind (SEC2-15): Cancel, Discard and Decide
// of a module's system actor apply the kind gate Start applies. The module may
// act on a run of a kind it owns, a kind it lists in TaskStarts, or a run it
// started itself; a run of any other module's kind is refused even where the
// module's own scope would satisfy the kind's permission.
func TestModuleClient_OperationsGatedByKind(t *testing.T) {
	ctx := context.Background()
	gordon := asUser("gordon", "gotham:run", "arkham:run")

	t.Run("cancel", func(t *testing.T) {
		h, mc := opsHarness(t, nil)
		foreign, err := h.x.Start(gordon, "arkham.shared", tasks.StartRequest{Mode: tasks.ModeRequestLive})
		if err != nil {
			t.Fatalf("start arkham.shared: %v", err)
		}
		h.waitState(foreign.ID, tasks.StateRunning)
		if err := mc.Cancel(ctx, foreign.ID); !errors.Is(err, ErrNotPermitted) {
			t.Fatalf("gotham canceling arkham's run of a kind it neither owns nor lists: %v, want ErrNotPermitted", err)
		}
		own, err := mc.Start(ctx, "gotham.dispatch", tasks.StartRequest{Mode: tasks.ModeRequestLive})
		if err != nil {
			t.Fatalf("start gotham.dispatch: %v", err)
		}
		h.waitState(own.ID, tasks.StateRunning)
		if err := mc.Cancel(ctx, own.ID); err != nil {
			t.Fatalf("gotham canceling its own kind's run: %v", err)
		}
		h.waitState(own.ID, tasks.StateCanceled)
		if err := h.x.Cancel(gordon, foreign.ID); err != nil {
			t.Fatalf("cleanup cancel: %v", err)
		}
	})

	t.Run("cancel of a listed kind the module started", func(t *testing.T) {
		h, mc := opsHarness(t, []tasks.Kind{"arkham.shared"})
		own, err := mc.Start(ctx, "arkham.shared", tasks.StartRequest{Mode: tasks.ModeRequestLive})
		if err != nil {
			t.Fatalf("start listed arkham.shared: %v", err)
		}
		h.waitState(own.ID, tasks.StateRunning)
		if err := mc.Cancel(ctx, own.ID); err != nil {
			t.Fatalf("gotham canceling the listed-kind run it started: %v", err)
		}
		h.waitState(own.ID, tasks.StateCanceled)
	})

	t.Run("a listed kind another actor started is still refused by permission", func(t *testing.T) {
		// arkham.cleanup is listed, so the kind gate passes; the narrower
		// checker holds no arkham:run, so the permission rule refuses.
		h, mc := opsHarness(t, []tasks.Kind{"arkham.cleanup"})
		foreign, err := h.x.Start(gordon, "arkham.cleanup", tasks.StartRequest{Mode: tasks.ModeRequestLive})
		if err != nil {
			t.Fatalf("start arkham.cleanup: %v", err)
		}
		h.waitState(foreign.ID, tasks.StateSucceeded)
		if err := mc.Discard(ctx, foreign.ID); !errors.Is(err, ErrNotPermitted) {
			t.Fatalf("gotham discarding a listed kind's run it did not start: %v, want ErrNotPermitted", err)
		}
	})

	t.Run("discard", func(t *testing.T) {
		h, mc := opsHarness(t, nil)
		foreign, err := h.x.Start(gordon, "arkham.shared-done", tasks.StartRequest{Mode: tasks.ModeRequestLive})
		if err != nil {
			t.Fatalf("start arkham.shared-done: %v", err)
		}
		h.waitState(foreign.ID, tasks.StateSucceeded)
		if err := mc.Discard(ctx, foreign.ID); !errors.Is(err, ErrNotPermitted) {
			t.Fatalf("gotham discarding arkham's run: %v, want ErrNotPermitted", err)
		}
		own, err := mc.Start(ctx, "gotham.signal", tasks.StartRequest{Mode: tasks.ModeRequestLive})
		if err != nil {
			t.Fatalf("start gotham.signal: %v", err)
		}
		h.waitState(own.ID, tasks.StateSucceeded)
		if err := mc.Discard(ctx, own.ID); errors.Is(err, ErrNotPermitted) {
			t.Fatalf("gotham discarding its own kind's run: %v, want it past the permission gate", err)
		}
	})

	t.Run("decide", func(t *testing.T) {
		h, mc := opsHarness(t, nil)
		foreign, err := h.x.Start(gordon, "arkham.shared-done", tasks.StartRequest{Mode: tasks.ModeRequestLive})
		if err != nil {
			t.Fatalf("start arkham.shared-done: %v", err)
		}
		h.waitState(foreign.ID, tasks.StateSucceeded)
		// The gate answers before the state is read: a foreign run is refused
		// whatever its state, and the module's own run reaches the state check.
		if err := mc.Decide(ctx, foreign.ID, tasks.DecideResume); !errors.Is(err, ErrNotPermitted) {
			t.Fatalf("gotham deciding arkham's run: %v, want ErrNotPermitted", err)
		}
		own, err := mc.Start(ctx, "gotham.signal", tasks.StartRequest{Mode: tasks.ModeRequestLive})
		if err != nil {
			t.Fatalf("start gotham.signal: %v", err)
		}
		h.waitState(own.ID, tasks.StateSucceeded)
		// A system actor's decision is not a person's (Decide's own rule for an
		// actor it cannot identify is for requests with no principal; here the
		// module is one), so the own-run call must not be the kind gate's refusal
		// of a foreign run: it is judged by the run's state.
		if err := mc.Decide(ctx, own.ID, tasks.DecideResume); !errors.Is(err, ErrNotAwaitingDecision) {
			t.Fatalf("gotham deciding its own succeeded run: %v, want ErrNotAwaitingDecision", err)
		}
	})
}

// TestModuleClient_NoManagementThroughTaskStarts (SEC2-15): a module that lists a
// kind starting under core:tasks.manage may start it (the kernel refuses such a
// listing at boot; this is the executor's own defense), and still cannot view or
// manage other runs, nor pause the executor, because every operation but a start
// is judged by a checker that never carries a core:* scope.
func TestModuleClient_NoManagementThroughTaskStarts(t *testing.T) {
	ctx := context.Background()
	h, mc := opsHarness(t, []tasks.Kind{"arkham.manage"})
	if _, err := mc.Start(ctx, "arkham.manage", tasks.StartRequest{Mode: tasks.ModeRequestLive}); err != nil {
		t.Fatalf("starting the listed core:tasks.manage kind: %v", err)
	}
	foreign, err := h.x.Start(asUser("gordon", "arkham:run"), "arkham.cleanup", tasks.StartRequest{Mode: tasks.ModeRequestLive})
	if err != nil {
		t.Fatalf("start arkham.cleanup: %v", err)
	}
	h.waitState(foreign.ID, tasks.StateSucceeded)
	if err := mc.Pause(ctx, "gotham may not"); !errors.Is(err, ErrNotPermitted) {
		t.Fatalf("Pause through a core:tasks.manage TaskStart: %v, want ErrNotPermitted", err)
	}
	if err := mc.Unpause(ctx); !errors.Is(err, ErrNotPermitted) {
		t.Fatalf("Unpause through a core:tasks.manage TaskStart: %v, want ErrNotPermitted", err)
	}
	if _, err := mc.Get(ctx, foreign.ID); !errors.Is(err, tasks.ErrRunNotFound) {
		t.Fatalf("viewing another module's run through a core:tasks.manage TaskStart: %v, want ErrRunNotFound", err)
	}
	if err := mc.Cancel(ctx, foreign.ID); !errors.Is(err, ErrNotPermitted) {
		t.Fatalf("canceling another module's run through a core:tasks.manage TaskStart: %v, want ErrNotPermitted", err)
	}
}
