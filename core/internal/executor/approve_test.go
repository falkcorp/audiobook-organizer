// file: core/internal/executor/approve_test.go
// version: 1.0.0
// guid: a500492e-f488-4dcd-aaeb-3ffe778b43c1
// last-edited: 2026-11-10

package executor

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// grants is a caller's checker: exactly the scopes it names.
type grants map[rbac.Permission]bool

func (g grants) Has(p rbac.Permission) bool { return g[p] }

func (g grants) Satisfies(r rbac.Requirement) bool {
	ps := r.Permissions()
	switch r.Kind() {
	case rbac.KindAllOf:
		for _, p := range ps {
			if !g[p] {
				return false
			}
		}
		return len(ps) > 0
	case rbac.KindAnyOf:
		return slices.ContainsFunc(ps, g.Has)
	}
	return false
}

func (grants) Confined() bool { return false }

func (g grants) Concrete() []rbac.Permission {
	var out []rbac.Permission
	for p := range g {
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

type callerKey struct{}

type callerID struct {
	actor tasks.Actor
	who   rbac.Checker
}

// asUser is a request context of a signed-in user holding perms.
func asUser(user string, perms ...rbac.Permission) context.Context {
	g := grants{}
	for _, p := range perms {
		g[p] = true
	}
	return context.WithValue(context.Background(), callerKey{}, callerID{tasks.Actor{UserID: user, AuthMethod: "session"}, g})
}

// principal is Options.Principal over asUser's contexts; any other context
// carries no identity (a kernel-internal call).
func principal(ctx context.Context) (tasks.Actor, rbac.Checker, bool) {
	c, ok := ctx.Value(callerKey{}).(callerID)
	return c.actor, c.who, ok
}

func withPrincipal(o *Options) { o.Principal = principal }

// arkhamCatalog is core's scopes plus Arkham's two.
func arkhamCatalog(t testing.TB) *rbac.Catalog {
	t.Helper()
	cat, err := rbac.NewCatalog(append(rbac.CoreScopes(),
		rbac.PermissionSpec{Name: scopeRun, Title: "Run Arkham tasks", Description: "Start Arkham's tasks"},
		rbac.PermissionSpec{Name: scopeApprov, Title: "Approve Arkham plans", Description: "Approve or reject Arkham's plans"})...)
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

const (
	relocate    = "arkham.relocate"
	scopeRun    = rbac.Permission("arkham:run")
	scopeApprov = rbac.Permission("arkham:approve")
)

var inmates = []string{"joker", "penguin", "riddler"}

// applyLog records the rows a Fixer applied.
type applyLog struct {
	mu  sync.Mutex
	ids []string
}

func (l *applyLog) add(id string) {
	l.mu.Lock()
	l.ids = append(l.ids, id)
	l.mu.Unlock()
}

func (l *applyLog) got() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := slices.Clone(l.ids)
	slices.Sort(out)
	return out
}

type fixerEdit func(*tasks.FixerSpec[noParams, string])

// fixerDef is arkham.relocate: one row per inmate; Apply records the row.
func fixerDef(log *applyLog, edits ...fixerEdit) tasks.Definition {
	s := tasks.FixerSpec[noParams, string]{
		Common: common(relocate, func(c *tasks.Common) {
			c.Perms.Approve = rbac.Need(scopeApprov)
			c.Approval = &tasks.Approval{PlanRequired: true}
		}),
		Candidates: func(*tasks.RC, noParams) (tasks.Source[string], error) {
			return tasks.Source[string]{Items: inmates, Key: func(s string) string { return s }}, nil
		},
		Load: func(_ *tasks.RC, _ noParams, id string) (string, bool, error) {
			return id, slices.Contains(inmates, id), nil
		},
		Evaluate: func(_ *tasks.ItemRC, _ noParams, c string) (tasks.Row, error) {
			return tasks.Row{ID: c, Proposed: map[string]string{"cell": "arkham-" + c}}, nil
		},
		Apply: func(_ *tasks.ItemRC, _ tasks.Writer, _ noParams, row tasks.Row) error {
			log.add(row.ID)
			return nil
		},
		Parallelism: tasks.Sequential("one cell block at a time"),
	}
	for _, e := range edits {
		e(&s)
	}
	return tasks.Fixer(tasks.Kind(relocate), s)
}

// planner is the user who previews the plans.
func planner() context.Context { return asUser("robin", scopeRun) }

// plan runs a Preview of arkham.relocate as robin and returns the plan.
func (h *harness) plan() tasks.RunID {
	h.t.Helper()
	ref, err := h.x.Start(planner(), relocate, tasks.StartRequest{Mode: tasks.ModeRequestPreview})
	if err != nil {
		h.t.Fatalf("plan: %v", err)
	}
	h.waitState(ref.ID, tasks.StateSucceeded)
	return ref.ID
}

// applyRuns lists the runs that name plan.
func (h *harness) applyRuns(plan tasks.RunID) []tasks.RunRecord {
	h.t.Helper()
	var out []tasks.RunRecord
	for r, err := range h.stores.Runs.List(context.Background(), tasks.RunQuery{}) {
		if err != nil {
			h.t.Fatalf("list: %v", err)
		}
		if r.PlanRef == plan {
			out = append(out, r)
		}
	}
	return out
}

// onlyApplyRun is the one run of plan.
func (h *harness) onlyApplyRun(plan tasks.RunID) tasks.RunRecord {
	h.t.Helper()
	runs := h.applyRuns(plan)
	if len(runs) != 1 {
		h.t.Fatalf("%d runs of plan %s, want 1", len(runs), plan)
	}
	return runs[0]
}

// rejectEvent is the plan's reject event, if any.
func (h *harness) rejectEvent(plan tasks.RunID) (tasks.RunEvent, bool) {
	h.t.Helper()
	for p, err := range h.stores.Runs.Pages(context.Background(), plan, tasks.PageQuery{Page: tasks.PageEvents, Limit: 1000}) {
		if err != nil {
			h.t.Fatalf("events: %v", err)
		}
		if p.Event.Event == eventReject {
			return p.Event, true
		}
	}
	return tasks.RunEvent{}, false
}

func result(t testing.TB, r tasks.RunRecord) fixerResult {
	t.Helper()
	var res fixerResult
	if err := json.Unmarshal(r.Result, &res); err != nil {
		t.Fatalf("result of %s: %v (%s)", r.ID, err, r.Result)
	}
	return res
}

// TestApprove_RefusesCallerWithoutApprovePermission (5T.3.7 table: approve
// and reject need Perms.Approve; the apply run is a start, so Perms.Start
// too). A caller with no identity is a kernel-internal call: it may
// approve, and acts as no one, never as the plan's actor.
func TestApprove_RefusesCallerWithoutApprovePermission(t *testing.T) {
	cells := []struct {
		name       string
		ctx        context.Context
		approveErr error
		rejectErr  error
		wantActor  tasks.Actor
	}{
		{name: "start scope only", ctx: asUser("harley", scopeRun), approveErr: ErrNotPermitted, rejectErr: ErrNotPermitted},
		{name: "view scope only", ctx: asUser("vicki", scopeTasksView), approveErr: ErrNotPermitted, rejectErr: ErrNotPermitted},
		{name: "approve scope without start", ctx: asUser("gordon", scopeApprov), approveErr: ErrNotPermitted,
			wantActor: tasks.Actor{UserID: "gordon", AuthMethod: "session"}},
		{name: "approve and start", ctx: asUser("gordon", scopeApprov, scopeRun),
			wantActor: tasks.Actor{UserID: "gordon", AuthMethod: "session"}},
		{name: "no identity acts as no one", ctx: context.Background()},
	}
	for _, c := range cells {
		t.Run(c.name, func(t *testing.T) {
			log := &applyLog{}
			h := newHarness(t, []tasks.Definition{fixerDef(log)}, withPrincipal)

			plan := h.plan()
			err := h.x.Approve(c.ctx, plan, tasks.ApproveRequest{Rows: []string{"joker"}})
			if c.approveErr != nil {
				if !isErr(err, c.approveErr) {
					t.Fatalf("approve: %v, want %v", err, c.approveErr)
				}
				if n := len(h.applyRuns(plan)); n != 0 {
					t.Fatalf("a refused approve started %d runs", n)
				}
			} else {
				if err != nil {
					t.Fatalf("approve: %v", err)
				}
				r := h.onlyApplyRun(plan)
				if r.Actor != c.wantActor {
					t.Fatalf("apply run actor %+v, want %+v (never the plan's robin)", r.Actor, c.wantActor)
				}
				h.waitState(r.ID, tasks.StateSucceeded)
				if got := log.got(); !slices.Equal(got, []string{"joker"}) {
					t.Fatalf("applied %v, want [joker]", got)
				}
			}

			other := h.plan()
			err = h.x.Approve(c.ctx, other, tasks.ApproveRequest{Reject: true, Reason: "not tonight"})
			ev, rejected := h.rejectEvent(other)
			if c.rejectErr != nil {
				if !isErr(err, c.rejectErr) || rejected {
					t.Fatalf("reject: %v (recorded %v), want %v and no reject event", err, rejected, c.rejectErr)
				}
				return
			}
			if err != nil || !rejected {
				t.Fatalf("reject: %v (recorded %v), want nil and a reject event", err, rejected)
			}
			var by tasks.Actor
			if ev.Actor != nil {
				by = *ev.Actor
			}
			if by != c.wantActor {
				t.Fatalf("reject event actor %+v, want %+v", by, c.wantActor)
			}
		})
	}
}

// TestStart_PlanRefOnlyThroughApprove: a plan's apply run starts only
// through Approve. Start, StartIn and a parent's Enqueue naming a plan_ref
// are refused whatever the caller holds, and a Live start of a kind whose
// Approval requires a plan cannot skip the plan.
func TestStart_PlanRefOnlyThroughApprove(t *testing.T) {
	log := &applyLog{}
	planCh := make(chan tasks.RunID, 1)
	parent := taskDef("gotham.dispatch", func(rc *tasks.RC, _ noParams) (string, error) {
		plan := <-planCh
		_, err := rc.Enqueue(relocate, noParams{}, func(r *tasks.StartRequest) {
			r.Mode = tasks.ModeRequestLive
			r.PlanRef = plan
		})
		return "", err
	})
	h := newHarness(t, []tasks.Definition{fixerDef(log), parent}, func(o *Options) {
		withPrincipal(o)
		o.Catalog = arkhamCatalog(t) // arkham's system actor holds arkham:run
	})
	plan := h.plan()
	both := asUser("gordon", scopeApprov, scopeRun)

	for _, mode := range []tasks.ModeRequest{tasks.ModeRequestLive, tasks.ModeRequestPreview} {
		if _, err := h.x.Start(both, relocate, tasks.StartRequest{Mode: mode, PlanRef: plan}); !isErr(err, ErrApprovalRequired) {
			t.Errorf("Start %v naming the plan: %v, want ErrApprovalRequired", mode, err)
		}
	}
	b := h.ks.NewIndexedBatch()
	if _, err := h.x.StartIn(context.Background(), b, relocate, tasks.StartRequest{Mode: tasks.ModeRequestPreview}); err != nil {
		t.Fatalf("StartIn of a plan (the system actor may start the kind): %v", err)
	}
	if _, err := h.x.StartIn(context.Background(), b, relocate, tasks.StartRequest{Mode: tasks.ModeRequestLive, PlanRef: plan}); !isErr(err, ErrApprovalRequired) {
		t.Errorf("StartIn naming the plan: %v, want ErrApprovalRequired", err)
	}
	_ = b.Close()
	if _, err := h.x.Start(both, relocate, tasks.StartRequest{Mode: tasks.ModeRequestLive}); !isErr(err, ErrPlanRequired) {
		t.Errorf("Live start without a plan: %v, want ErrPlanRequired", err)
	}

	pid := h.start("gotham.dispatch", tasks.StartRequest{})
	planCh <- plan
	if r := h.waitState(pid, tasks.StateFailed, tasks.StateSucceeded); r.State != tasks.StateFailed || !strings.Contains(r.Error, ErrApprovalRequired.Error()) {
		t.Errorf("parent enqueue naming the plan: %s %q, want failed with ErrApprovalRequired", r.State, r.Error)
	}
	if n := len(h.applyRuns(plan)); n != 0 {
		t.Fatalf("%d runs of the plan started outside Approve", n)
	}
	if got := log.got(); len(got) != 0 {
		t.Fatalf("applied %v outside Approve", got)
	}
}

// TestApprove_RejectedPlanRefused: a rejected plan is never applied, whether
// the approve comes after the reject or the apply run was approved first
// and the reject lands before it runs.
func TestApprove_RejectedPlanRefused(t *testing.T) {
	gordon := asUser("gordon", scopeApprov, scopeRun)
	t.Run("approve after reject", func(t *testing.T) {
		log := &applyLog{}
		h := newHarness(t, []tasks.Definition{fixerDef(log)}, withPrincipal)
		plan := h.plan()
		if err := h.x.Approve(gordon, plan, tasks.ApproveRequest{Reject: true, Reason: "wrong wing"}); err != nil {
			t.Fatalf("reject: %v", err)
		}
		if err := h.x.Approve(gordon, plan, tasks.ApproveRequest{Rows: inmates}); !isErr(err, ErrPlanRejected) {
			t.Fatalf("approve after reject: %v, want ErrPlanRejected", err)
		}
		if n := len(h.applyRuns(plan)); n != 0 {
			t.Fatalf("%d runs of a rejected plan", n)
		}
	})
	t.Run("reject before the apply run runs", func(t *testing.T) {
		log := &applyLog{}
		defs := []tasks.Definition{fixerDef(log)}
		fs := vfs.NewCrashableMem()
		stores, ks := bootStores(t, fs)
		planning := onStores(t, fs, stores, ks, defs, withPrincipal, true)
		plan := planning.plan()
		if err := planning.x.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
		h := onStores(t, fs, stores, ks, defs, withPrincipal, false) // not dispatching yet
		if err := h.x.Approve(gordon, plan, tasks.ApproveRequest{Rows: inmates}); err != nil {
			t.Fatalf("approve: %v", err)
		}
		if err := h.x.Approve(gordon, plan, tasks.ApproveRequest{Reject: true, Reason: "riot in D wing"}); err != nil {
			t.Fatalf("reject: %v", err)
		}
		if err := h.x.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
		r := h.waitState(h.onlyApplyRun(plan).ID, tasks.StateFailed, tasks.StateSucceeded)
		if r.State != tasks.StateFailed || !strings.Contains(r.Error, ErrPlanRejected.Error()) {
			t.Fatalf("apply run of a plan rejected after approval: %s %q, want failed with ErrPlanRejected", r.State, r.Error)
		}
		if got := log.got(); len(got) != 0 {
			t.Fatalf("applied %v of a rejected plan", got)
		}
	})
}

// TestFixer_ApplyFailsClosedWithoutApproval: an apply run applies only the
// rows its approval names. With no approval record it applies nothing and
// fails; a ResumeFromZero resume voids progress, never the approval.
func TestFixer_ApplyFailsClosedWithoutApproval(t *testing.T) {
	t.Run("no approval record", func(t *testing.T) {
		log := &applyLog{}
		h := newHarness(t, []tasks.Definition{fixerDef(log)}, withPrincipal)
		plan := h.plan()
		// An apply run with no approval record (as a lost or never-written
		// record would leave it): prepared as Approve does, staged without
		// the record.
		ctx := context.Background()
		prep, err := h.e.prepare(ctx, relocate, tasks.StartRequest{Mode: tasks.ModeRequestLive, PlanRef: plan}, nil, modeApprove)
		if err != nil {
			t.Fatal(err)
		}
		var id tasks.RunID
		if err := h.e.write(ctx, func(ctx context.Context, b *storage.Batch) error {
			ref, err := h.e.stageStart(ctx, b, prep)
			id = ref.ID
			return err
		}); err != nil {
			t.Fatal(err)
		}
		h.e.wakeDispatcher()
		r := h.waitState(id, tasks.StateFailed, tasks.StateSucceeded)
		if r.State != tasks.StateFailed || !strings.Contains(r.Error, ErrNoApproval.Error()) {
			t.Fatalf("apply run without approval: %s %q, want failed with ErrNoApproval", r.State, r.Error)
		}
		if got := log.got(); len(got) != 0 {
			t.Fatalf("applied %v with no approval (fail open)", got)
		}
	})
	t.Run("from-zero resume keeps the approval", func(t *testing.T) {
		applying := make(chan string, 1)
		first := &applyLog{}
		fromZero := func(s *tasks.FixerSpec[noParams, string]) { s.Resume = tasks.ResumeFromZero }
		blocking := func(s *tasks.FixerSpec[noParams, string]) {
			s.Apply = func(ic *tasks.ItemRC, _ tasks.Writer, _ noParams, row tasks.Row) error {
				applying <- row.ID
				<-ic.Context().Done()
				return context.Cause(ic.Context())
			}
		}
		h := newHarness(t, []tasks.Definition{fixerDef(first, fromZero, blocking)}, withPrincipal)
		plan := h.plan()
		if err := h.x.Approve(asUser("gordon", scopeApprov, scopeRun), plan, tasks.ApproveRequest{Rows: []string{"joker"}}); err != nil {
			t.Fatalf("approve: %v", err)
		}
		if got := <-applying; got != "joker" {
			t.Fatalf("first attempt applying %q", got)
		}
		second := &applyLog{}
		next := h.crashBoot([]tasks.Definition{fixerDef(second, fromZero)}, withPrincipal)
		r := next.waitState(next.onlyApplyRun(plan).ID, tasks.StateSucceeded, tasks.StateFailed)
		if r.State != tasks.StateSucceeded || r.Attempt != 2 {
			t.Fatalf("after the crash: %s attempt %d (err %q), want succeeded on attempt 2", r.State, r.Attempt, r.Error)
		}
		if got := second.got(); !slices.Equal(got, []string{"joker"}) {
			t.Fatalf("second attempt applied %v, want [joker]", got)
		}
		if res := result(t, r); res.Applied != 1 {
			t.Fatalf("result %+v, want 1 applied", res)
		}
	})
}

// TestFixer_PlanRefAgeAndRowsOnly (5T.3.7): Approve takes a succeeded
// Preview run of the same kind no older than MaxPlanAge, and the apply run
// applies exactly the approved rows.
func TestFixer_PlanRefAgeAndRowsOnly(t *testing.T) {
	var skew atomic.Int64
	log := &applyLog{}
	maxAge := func(s *tasks.FixerSpec[noParams, string]) { s.Approval.MaxPlanAge = time.Hour }
	h := newHarness(t, []tasks.Definition{fixerDef(log, maxAge)}, func(o *Options) {
		withPrincipal(o)
		o.Now = func() time.Time { return time.Now().Add(time.Duration(skew.Load())) }
	})
	gordon := asUser("gordon", scopeApprov, scopeRun)

	plan := h.plan()
	if err := h.x.Approve(gordon, plan, tasks.ApproveRequest{Rows: []string{"bane"}}); !isErr(err, ErrRowNotInPlan) {
		t.Fatalf("approve a row the plan lacks: %v, want ErrRowNotInPlan", err)
	}
	if err := h.x.Approve(gordon, plan, tasks.ApproveRequest{Rows: []string{"riddler", "joker", "joker"}}); err != nil {
		t.Fatalf("approve: %v", err)
	}
	apply := h.onlyApplyRun(plan)
	r := h.waitState(apply.ID, tasks.StateSucceeded, tasks.StateFailed)
	if got := log.got(); !slices.Equal(got, []string{"joker", "riddler"}) {
		t.Fatalf("applied %v, want exactly the approved [joker riddler]", got)
	}
	if res := result(t, r); res.Applied != 2 {
		t.Fatalf("result %+v, want 2 applied", res)
	}
	if err := h.x.Approve(gordon, apply.ID, tasks.ApproveRequest{}); !isErr(err, ErrPlanInvalid) {
		t.Fatalf("approve of a Live run: %v, want ErrPlanInvalid", err)
	}

	old := h.plan()
	skew.Store(int64(time.Hour + time.Minute))
	if err := h.x.Approve(gordon, old, tasks.ApproveRequest{Rows: inmates}); !isErr(err, ErrPlanTooOld) {
		t.Fatalf("approve of a plan past MaxPlanAge: %v, want ErrPlanTooOld", err)
	}
	if n := len(h.applyRuns(old)); n != 0 {
		t.Fatalf("%d runs of a stale plan", n)
	}
}

// TestStoredName_ReservedCheckpointNames: RC's unnamed checkpoint is stored
// under the reserved name, and a body may not name the reserved space, so
// it can neither forge an approval nor read the executor's records.
func TestStoredName_ReservedCheckpointNames(t *testing.T) {
	for name, want := range map[string]string{"": ckptRC, "rooftop": "rooftop", "corelation": "corelation"} {
		if got, err := storedName(name); err != nil || got != want {
			t.Errorf("storedName(%q) = %q, %v; want %q", name, got, err, want)
		}
	}
	for _, name := range []string{ckptApproved, ckptZero, ckptRC, "core.anything"} {
		if _, err := storedName(name); !errors.Is(err, ErrReservedCheckpoint) {
			t.Errorf("storedName(%q): %v, want ErrReservedCheckpoint", name, err)
		}
	}
}
