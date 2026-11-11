// file: core/internal/executor/window_guard_test.go
// version: 1.0.0
// guid: a9f75ef9-99a0-4970-a150-0407885c4bc3
// last-edited: 2026-11-11

package executor

import (
	"context"
	"testing"

	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

const (
	nightly   = "core.window.nightly"
	manageKey = rbac.Permission("core:tasks.manage")
	scopeArk  = rbac.Permission("arkham:run")
)

// windowDefs are a nightly window (Perms.Start core:tasks.manage, as
// scheduler.WindowDefinition declares it) and two members: arkham.relocate
// (needs arkham:run) and gotham.signal (needs gotham:run).
func windowDefs() []tasks.Definition {
	return []tasks.Definition{
		taskDef(nightly, okBody, readOnly(nightly), func(c *tasks.Common) {
			c.Perms = tasks.Permissions{Start: rbac.Need(manageKey)}
			c.Priority = tasks.PrioritySystem
		}),
		taskDef("arkham.relocate", okBody),
		taskDef("gotham.signal", okBody),
	}
}

func windowHarness(t testing.TB, members func(string) []tasks.Kind) *harness {
	t.Helper()
	return newHarness(t, windowDefs(), func(o *Options) {
		withPrincipal(o)
		o.WindowMembers = members
		o.Catalog = catalogWith(t, scopeArk, "gotham:run", "arkham:approve")
	})
}

func twoMembers(window string) []tasks.Kind {
	if window != "nightly" {
		return nil
	}
	return []tasks.Kind{"arkham.relocate", "gotham.signal"}
}

func startWindow(h *harness, ctx context.Context, trigger tasks.Trigger) error {
	_, err := h.x.Start(ctx, nightly, tasks.StartRequest{Mode: tasks.ModeRequestLive, Trigger: trigger})
	return err
}

// TestStart_WindowRefusedUnlessCallerHoldsEveryMemberScope: a manual start of
// a window kind runs its members as their own modules' system actors, so the
// caller must hold every member's Perms.Start as well as core:tasks.manage.
func TestStart_WindowRefusedUnlessCallerHoldsEveryMemberScope(t *testing.T) {
	cells := []struct {
		name    string
		ctx     context.Context
		members func(string) []tasks.Kind
		trigger tasks.Trigger
		want    error
	}{
		{name: "manage only, members need more", ctx: asUser("gordon", manageKey), members: twoMembers, want: ErrNotPermitted},
		{name: "manage and one of two member scopes", ctx: asUser("gordon", manageKey, scopeArk), members: twoMembers, want: ErrNotPermitted},
		{name: "manage and every member scope", ctx: asUser("gordon", manageKey, scopeArk, "gotham:run"), members: twoMembers},
		{name: "members without scope are not members of another window", ctx: asUser("gordon", manageKey),
			members: func(string) []tasks.Kind { return nil }},
		{name: "a client-claimed window trigger skips nothing", ctx: asUser("gordon", manageKey), members: twoMembers,
			trigger: tasks.TriggerWindow, want: ErrNotPermitted},
		{name: "no member list refuses (fail closed)", ctx: asUser("gordon", manageKey, scopeArk, "gotham:run"), members: nil, want: ErrNotPermitted},
		{name: "member with no definition refuses", ctx: asUser("gordon", manageKey, scopeArk, "gotham:run"),
			members: func(string) []tasks.Kind { return []tasks.Kind{"gotham.missing"} }, want: ErrNotPermitted},
		{name: "no manage scope refuses regardless", ctx: asUser("gordon", scopeArk, "gotham:run"), members: twoMembers, want: ErrNotPermitted},
	}
	for _, c := range cells {
		t.Run(c.name, func(t *testing.T) {
			h := windowHarness(t, c.members)
			err := startWindow(h, c.ctx, c.trigger)
			if c.want != nil {
				if !isErr(err, c.want) {
					t.Fatalf("start = %v, want %v", err, c.want)
				}
				for r, rerr := range h.stores.Runs.List(context.Background(), tasks.RunQuery{}) {
					if rerr != nil {
						t.Fatal(rerr)
					}
					t.Fatalf("a refused window start left run %s (%s)", r.ID, r.Kind)
				}
				return
			}
			if err != nil {
				t.Fatalf("start = %v, want success", err)
			}
		})
	}
}

// TestStart_WindowGuardSparesSchedulerAndOtherKinds: the scheduled path (no
// caller checker) and kinds that are not windows never consult the member
// list.
func TestStart_WindowGuardSparesSchedulerAndOtherKinds(t *testing.T) {
	called := 0
	h := windowHarness(t, func(string) []tasks.Kind { called++; return twoMembers("nightly") })

	// The scheduler: StartIn has no caller; the window kind's own system
	// actor (module core) may start it.
	b := h.ks.NewIndexedBatch()
	if _, err := h.x.StartIn(context.Background(), b, nightly, tasks.StartRequest{Mode: tasks.ModeRequestLive, Trigger: tasks.TriggerWindow}); err != nil {
		t.Fatalf("scheduled window StartIn: %v", err)
	}
	_ = b.Close()

	// A kernel-internal Start with no identity in ctx is not a caller either.
	if err := startWindow(h, context.Background(), tasks.TriggerWindow); err != nil {
		t.Fatalf("Start with no identity: %v", err)
	}

	// A member started directly asks only its own scope.
	if _, err := h.x.Start(asUser("gordon", scopeArk), "arkham.relocate", tasks.StartRequest{Mode: tasks.ModeRequestLive}); err != nil {
		t.Fatalf("member start: %v", err)
	}
	if called != 0 {
		t.Fatalf("the member list was read %d times on paths that are not a caller's window start", called)
	}
}
