// file: core/internal/executor/manage_test.go
// version: 0.1.0
// guid: 5c0b6f7e-2d41-4c8a-9e53-7a1f0d9b4e26
// last-edited: 2026-11-22

package executor

import (
	"context"
	"errors"
	"testing"

	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// The owner ruling of 2026-11-22: cancel, discard, decide and approve of a run
// all need the kind's Perms.Manage AND the operation's own requirement. The
// run's own actor and a holder of core:tasks.manage alone are not admitted. These
// tables put every caller shape against each of the four operations; a refused
// cell must leave the run as it was, and the admitted cell runs last (a
// cancel, discard or decision is not repeatable).

const (
	scopeGothamCancel = rbac.Permission("gotham:cancel")
	runOwner          = "robin" // the run's own actor
)

// manageShape is one caller and whether the operation admits it.
type manageShape struct {
	name string
	ctx  context.Context
	ok   bool
}

// manageShapes is every caller shape for an operation whose own requirement is
// the scopes op, on a kind whose Perms.Manage is manage. Exactly the last holds
// both.
func manageShapes(op []rbac.Permission, manage rbac.Permission) []manageShape {
	with := func(extra ...rbac.Permission) []rbac.Permission {
		return append(append([]rbac.Permission{}, op...), extra...)
	}
	return []manageShape{
		{name: "run's own actor, no scopes", ctx: asUser(runOwner)},
		{name: "run's own actor holding the operation's scopes", ctx: asUser(runOwner, op...)},
		{name: "core:tasks.manage alone", ctx: asUser("lucius", scopeTasksManage)},
		{name: "core:tasks.manage and the operation's scopes", ctx: asUser("lucius", with(scopeTasksManage)...)},
		{name: "Manage alone", ctx: asUser("lucius", manage)},
		{name: "Manage and core:tasks.manage without the operation's scopes", ctx: asUser("lucius", manage, scopeTasksManage)},
		{name: "the operation's scopes and Manage", ctx: asUser("lucius", with(manage)...), ok: true},
	}
}

func manageCatalog(t testing.TB) *rbac.Catalog {
	return catalogWith(t, scopeRun, scopeApprov, scopeArkManage, scopeGothamGo, scopeGothamOK, scopeGothamManage, scopeGothamCancel)
}

func withManageCatalog(t testing.TB) func(*Options) {
	return func(o *Options) {
		o.Catalog = manageCatalog(t)
		withPrincipal(o)
	}
}

// refused fails unless err is ErrNotPermitted.
func refused(t *testing.T, op string, err error) {
	t.Helper()
	if !errors.Is(err, ErrNotPermitted) {
		t.Fatalf("%s: %v, want ErrNotPermitted", op, err)
	}
}

// blockingKinds are gotham.stakeout (Cancel omitted: Start gates, so Start AND
// Manage) and gotham.vault (Perms.Cancel gotham:cancel: Cancel AND Manage, the
// Start scope alone not enough).
func blockingKinds(started chan<- tasks.RunID) []tasks.Definition {
	return []tasks.Definition{
		taskDef("gotham.stakeout", blockUntilCtx(started)),
		taskDef("gotham.vault", blockUntilCtx(started), func(c *tasks.Common) { c.Perms.Cancel = rbac.Need(scopeGothamCancel) }),
	}
}

// TestCancel_NeedsManageAndTheCancelRequirement: Cancel admits only a caller
// holding the kind's Perms.Manage and its Perms.Cancel (omitted: Perms.Start).
// The run's own actor, and core:tasks.manage alone, are refused.
func TestCancel_NeedsManageAndTheCancelRequirement(t *testing.T) {
	for _, kind := range []struct {
		name string
		op   rbac.Permission
	}{{"gotham.stakeout", scopeGothamGo}, {"gotham.vault", scopeGothamCancel}} {
		t.Run(kind.name, func(t *testing.T) {
			started := make(chan tasks.RunID, 4)
			h := newHarness(t, blockingKinds(started), withManageCatalog(t))
			ref := h.startAs(asUser(runOwner, scopeGothamGo), kind.name, tasks.StartRequest{})
			<-started
			shapes := manageShapes([]rbac.Permission{kind.op}, scopeGothamManage)
			if kind.name == "gotham.vault" {
				// The Start scope is not the cancel scope of a kind that names its own.
				shapes = append([]manageShape{{name: "Start scope and Manage without the Cancel scope",
					ctx: asUser("lucius", scopeGothamGo, scopeGothamManage)}}, shapes...)
			}
			for _, s := range shapes {
				if !s.ok {
					refused(t, s.name, h.x.Cancel(s.ctx, ref.ID))
					if r := h.get(ref.ID); r.State != tasks.StateRunning {
						t.Fatalf("%s: a refused cancel left the run %s", s.name, r.State)
					}
					continue
				}
				if err := h.x.Cancel(s.ctx, ref.ID); err != nil {
					t.Fatalf("%s: %v, want admitted", s.name, err)
				}
				h.waitState(ref.ID, tasks.StateCanceled)
			}
		})
	}
}

// TestCancel_UnknownKindIsRefusedToACaller: a run whose kind has no definition
// has no Perms.Manage to hold, so a caller with a checker cannot cancel or
// discard it (the kernel's own caller, with no identity, still can).
func TestCancel_UnknownKindIsRefusedToACaller(t *testing.T) {
	started := make(chan tasks.RunID, 1)
	defs := []tasks.Definition{taskDef("gotham.stakeout", blockUntilCtx(started))}
	h := newHarness(t, defs, withManageCatalog(t))
	ref := h.startAs(asUser(runOwner, scopeGothamGo), "gotham.stakeout", tasks.StartRequest{})
	<-started
	if err := h.x.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	h = h.reboot(nil, withManageCatalog(t)) // the kind is gone
	everything := asUser("lucius", scopeGothamGo, scopeGothamManage, scopeTasksManage)
	refused(t, "cancel", h.x.Cancel(everything, ref.ID))
	refused(t, "discard", h.x.Discard(everything, ref.ID))
	if _, err := h.stores.Runs.Get(context.Background(), ref.ID); err != nil {
		t.Fatalf("a refused discard removed the run: %v", err)
	}
}

// TestDiscard_NeedsManageAndTheCancelRequirement: Discard follows Cancel's rule.
func TestDiscard_NeedsManageAndTheCancelRequirement(t *testing.T) {
	for _, kind := range []struct {
		name string
		op   rbac.Permission
	}{{"gotham.stakeout", scopeGothamGo}, {"gotham.vault", scopeGothamCancel}} {
		t.Run(kind.name, func(t *testing.T) {
			defs := []tasks.Definition{
				taskDef("gotham.stakeout", okBody),
				taskDef("gotham.vault", okBody, func(c *tasks.Common) { c.Perms.Cancel = rbac.Need(scopeGothamCancel) }),
			}
			h := newHarness(t, defs, withManageCatalog(t))
			ref := h.startAs(asUser(runOwner, scopeGothamGo), kind.name, tasks.StartRequest{})
			h.waitState(ref.ID, tasks.StateSucceeded)
			shapes := manageShapes([]rbac.Permission{kind.op}, scopeGothamManage)
			if kind.name == "gotham.vault" {
				shapes = append([]manageShape{{name: "Start scope and Manage without the Cancel scope",
					ctx: asUser("lucius", scopeGothamGo, scopeGothamManage)}}, shapes...)
			}
			for _, s := range shapes {
				if !s.ok {
					refused(t, s.name, h.x.Discard(s.ctx, ref.ID))
					if _, err := h.stores.Runs.Get(context.Background(), ref.ID); err != nil {
						t.Fatalf("%s: a refused discard removed the run: %v", s.name, err)
					}
					continue
				}
				if err := h.x.Discard(s.ctx, ref.ID); err != nil {
					t.Fatalf("%s: %v, want admitted", s.name, err)
				}
				if _, err := h.stores.Runs.Get(context.Background(), ref.ID); err == nil {
					t.Fatalf("%s: the admitted discard left the run", s.name)
				}
			}
		})
	}
}

// TestDecide_NeedsManageAndTheApproveRequirement: Decide admits only a caller
// holding the kind's Perms.Manage and its Perms.Approve (omitted: Perms.Start).
func TestDecide_NeedsManageAndTheApproveRequirement(t *testing.T) {
	for _, kind := range []struct {
		name string
		op   rbac.Permission
		edit func(*tasks.Common)
	}{
		{"gotham.stakeout", scopeGothamGo, nil},
		{"gotham.vault", scopeGothamOK, func(c *tasks.Common) { c.Perms.Approve = rbac.Need(scopeGothamOK) }},
	} {
		t.Run(kind.name, func(t *testing.T) {
			started := make(chan tasks.RunID, 1)
			edits := []func(*tasks.Common){withResume(tasks.ResumeAsk)}
			if kind.edit != nil {
				edits = append(edits, kind.edit)
			}
			defs := []tasks.Definition{taskDef(kind.name, blockUntilCtx(started), edits...)}
			edit := withManageCatalog(t)
			h := newHarness(t, defs, edit)
			ref := h.startAs(asUser(runOwner, scopeGothamGo), kind.name, tasks.StartRequest{})
			<-started
			if err := h.x.Shutdown(context.Background()); err != nil {
				t.Fatal(err)
			}
			h = h.reboot(defs, edit)
			h.waitState(ref.ID, tasks.StateAwaitingDecision)
			shapes := manageShapes([]rbac.Permission{kind.op}, scopeGothamManage)
			if kind.edit != nil {
				shapes = append([]manageShape{{name: "Start scope and Manage without the Approve scope",
					ctx: asUser("lucius", scopeGothamGo, scopeGothamManage)}}, shapes...)
			}
			for _, s := range shapes {
				if !s.ok {
					refused(t, s.name, h.x.Decide(s.ctx, ref.ID, tasks.DecideDrop))
					if r := h.get(ref.ID); r.State != tasks.StateAwaitingDecision {
						t.Fatalf("%s: a refused decide left the run %s", s.name, r.State)
					}
					continue
				}
				if err := h.x.Decide(s.ctx, ref.ID, tasks.DecideDrop); err != nil {
					t.Fatalf("%s: %v, want admitted", s.name, err)
				}
				h.waitState(ref.ID, tasks.StateInterruptedDropped)
			}
		})
	}
}

// TestApprove_NeedsManageAndTheApproveRequirement: Approve admits only a caller
// holding the kind's Perms.Manage and its Perms.Approve (and the Start the apply
// run needs). A reject is an approve operation and needs the same.
func TestApprove_NeedsManageAndTheApproveRequirement(t *testing.T) {
	h := newHarness(t, []tasks.Definition{fixerDef(&applyLog{})}, withManageCatalog(t))
	plan := h.plan()
	// The apply run is a start, so an approver also holds the Start scope.
	shapes := manageShapes([]rbac.Permission{scopeApprov, scopeRun}, scopeArkManage)
	for _, s := range shapes {
		if !s.ok {
			refused(t, s.name+" (approve)", h.x.Approve(s.ctx, plan, tasks.ApproveRequest{Rows: []string{"joker"}}))
			refused(t, s.name+" (reject)", h.x.Approve(s.ctx, plan, tasks.ApproveRequest{Reject: true, Reason: "not tonight"}))
			if n := len(h.applyRuns(plan)); n != 0 {
				t.Fatalf("%s: a refused approve started %d runs", s.name, n)
			}
			if _, rejected := h.rejectEvent(plan); rejected {
				t.Fatalf("%s: a refused reject was recorded", s.name)
			}
			continue
		}
		if err := h.x.Approve(s.ctx, plan, tasks.ApproveRequest{Rows: []string{"joker"}}); err != nil {
			t.Fatalf("%s: %v, want admitted", s.name, err)
		}
		h.waitState(h.onlyApplyRun(plan).ID, tasks.StateSucceeded)
	}
}

// TestInternalCaller_ControlIsUnchanged pins the path the Manage rule does not
// touch (the dark half of the owner's 2026-11-22 ruling): a call whose context
// carries no identity is the kernel's own, so Cancel and Discard are admitted
// for any kind, Manage or not, exactly as before. Decide and Approve refuse such
// a caller before and after this change (approve_test.go's "no identity" cells).
// A caller who carries an identity is the one Manage binds.
func TestInternalCaller_ControlIsUnchanged(t *testing.T) {
	started := make(chan tasks.RunID, 2)
	h := newHarness(t, blockingKinds(started), withManageCatalog(t))
	ref := h.startAs(asUser(runOwner, scopeGothamGo), "gotham.stakeout", tasks.StartRequest{})
	<-started
	if err := h.x.Cancel(context.Background(), ref.ID); err != nil {
		t.Fatalf("internal Cancel: %v, want admitted", err)
	}
	h.waitState(ref.ID, tasks.StateCanceled)
	if err := h.x.Discard(context.Background(), ref.ID); err != nil {
		t.Fatalf("internal Discard: %v, want admitted", err)
	}
	if _, err := h.stores.Runs.Get(context.Background(), ref.ID); err == nil {
		t.Fatal("the discarded run is still stored")
	}
}
