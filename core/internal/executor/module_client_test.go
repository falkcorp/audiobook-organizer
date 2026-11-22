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
