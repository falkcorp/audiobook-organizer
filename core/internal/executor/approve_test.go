// file: core/internal/executor/approve_test.go
// version: 1.1.0
// guid: a500492e-f488-4dcd-aaeb-3ffe778b43c1
// last-edited: 2026-11-11

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

// catalogWith is core's scopes plus perms (fixture module scopes).
func catalogWith(t testing.TB, perms ...rbac.Permission) *rbac.Catalog {
	t.Helper()
	specs := rbac.CoreScopes()
	for _, p := range perms {
		specs = append(specs, rbac.PermissionSpec{Name: p, Title: string(p), Description: "Fixture scope " + string(p)})
	}
	cat, err := rbac.NewCatalog(specs...)
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

// arkhamCatalog is core's scopes plus Arkham's two.
func arkhamCatalog(t testing.TB) *rbac.Catalog { return catalogWith(t, scopeRun, scopeApprov) }

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
// too). Both fail closed: a call with no caller identity is refused, and so
// is every call to an executor that resolves no Principal. The apply run
// and the reject event carry the approver, never the plan's actor.
func TestApprove_RefusesCallerWithoutApprovePermission(t *testing.T) {
	cells := []struct {
		name        string
		ctx         context.Context
		noPrincipal bool
		approveErr  error
		rejectErr   error
		wantActor   tasks.Actor
	}{
		{name: "start scope only", ctx: asUser("harley", scopeRun), approveErr: ErrNotPermitted, rejectErr: ErrNotPermitted},
		{name: "view scope only", ctx: asUser("vicki", scopeTasksView), approveErr: ErrNotPermitted, rejectErr: ErrNotPermitted},
		{name: "approve scope without start", ctx: asUser("gordon", scopeApprov), approveErr: ErrNotPermitted,
			wantActor: tasks.Actor{UserID: "gordon", AuthMethod: "session"}},
		{name: "approve and start", ctx: asUser("gordon", scopeApprov, scopeRun),
			wantActor: tasks.Actor{UserID: "gordon", AuthMethod: "session"}},
		{name: "no identity", ctx: context.Background(), approveErr: ErrNotPermitted, rejectErr: ErrNotPermitted},
		{name: "executor without a Principal", ctx: asUser("gordon", scopeApprov, scopeRun), noPrincipal: true,
			approveErr: ErrNotPermitted, rejectErr: ErrNotPermitted},
	}
	for _, c := range cells {
		t.Run(c.name, func(t *testing.T) {
			log := &applyLog{}
			edit := withPrincipal
			if c.noPrincipal {
				edit = nil
			}
			h := newHarness(t, []tasks.Definition{fixerDef(log)}, edit)

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

// TestApprove_RejectedPlanRefused: an approve after a reject is refused
// and starts nothing. (A reject after an approve is refused by the
// decide-once rule, TestApprove_DecidesAPlanOnce; an apply run whose plan
// carries a reject applies nothing, TestFixer_ApplyFailsClosedWithoutApproval.)
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
}

// stageApply stages an apply run of plan by hand, as Approve's batch would
// but without its decision or checks, with rec as its approval record (nil:
// none), and returns its ID. It is how a lost, forged or mismatched record
// is put in front of the apply path.
func (h *harness) stageApply(plan tasks.RunID, rec *approvalRecord) tasks.RunID {
	h.t.Helper()
	ctx := context.Background()
	prep := prepared{d: h.e.defs[relocate], mode: tasks.ModeLive, params: json.RawMessage("null"), prio: tasks.PriorityNormal,
		req: tasks.StartRequest{Mode: tasks.ModeRequestLive, PlanRef: plan, Trigger: tasks.TriggerManual}}
	var id tasks.RunID
	if err := h.e.write(ctx, func(ctx context.Context, b *storage.Batch) error {
		ref, err := h.e.stageStart(ctx, b, prep)
		if err != nil {
			return err
		}
		id = ref.ID
		if rec == nil {
			return nil
		}
		raw, err := json.Marshal(rec)
		if err != nil {
			return err
		}
		env, err := json.Marshal(ckptEnvelope{A: 1, V: raw})
		if err != nil {
			return err
		}
		return h.stores.Runs.SaveCheckpoint(ctx, b, id, ckptApproved, env)
	}); err != nil {
		h.t.Fatal(err)
	}
	h.e.wakeDispatcher()
	return id
}

// markDecided appends a decision event to plan directly, as Approve's move
// does but without its checks.
func (h *harness) markDecided(plan tasks.RunID, event string) {
	h.t.Helper()
	if _, err := h.e.move(context.Background(), plan, func(cur tasks.RunRecord) (tasks.RunRecord, tasks.RunEvent, error) {
		return cur, tasks.RunEvent{Event: event}, nil
	}, nil); err != nil {
		h.t.Fatal(err)
	}
}

// TestFixer_ApplyFailsClosedWithoutApproval: an apply run applies only the
// rows its own plan's approval names. It applies nothing, and fails, when it
// has no approval record, when its record approves another plan (the shape
// of a fold that lands one plan's approval on another's run), when its plan
// carries a reject, or when its plan carries no approval at all. A
// ResumeFromZero resume voids progress, never the approval.
func TestFixer_ApplyFailsClosedWithoutApproval(t *testing.T) {
	for _, c := range []struct {
		name    string
		setup   func(h *harness, plan, other tasks.RunID) *approvalRecord
		wantErr error
	}{
		{name: "no approval record", wantErr: ErrNoApproval, setup: func(h *harness, plan, _ tasks.RunID) *approvalRecord {
			h.markDecided(plan, eventApprove)
			return nil
		}},
		{name: "approval of another plan", wantErr: ErrNoApproval, setup: func(h *harness, plan, other tasks.RunID) *approvalRecord {
			h.markDecided(plan, eventApprove)
			h.markDecided(other, eventApprove)
			return &approvalRecord{Plan: other, Rows: []string{"joker"}}
		}},
		{name: "plan carries a reject", wantErr: ErrPlanRejected, setup: func(h *harness, plan, _ tasks.RunID) *approvalRecord {
			h.markDecided(plan, eventReject)
			return &approvalRecord{Plan: plan, Rows: []string{"joker"}}
		}},
		{name: "plan carries no decision", wantErr: ErrNoApproval, setup: func(_ *harness, plan, _ tasks.RunID) *approvalRecord {
			return &approvalRecord{Plan: plan, Rows: []string{"joker"}}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			log := &applyLog{}
			h := newHarness(t, []tasks.Definition{fixerDef(log)}, withPrincipal)
			plan, other := h.plan(), h.plan()
			id := h.stageApply(plan, c.setup(h, plan, other))
			r := h.waitState(id, tasks.StateFailed, tasks.StateSucceeded)
			if r.State != tasks.StateFailed || !strings.Contains(r.Error, c.wantErr.Error()) {
				t.Fatalf("apply run: %s %q, want failed with %v", r.State, r.Error, c.wantErr)
			}
			if got := log.got(); len(got) != 0 {
				t.Fatalf("applied %v (fail open)", got)
			}
		})
	}
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

// TestApprove_DecidesAPlanOnce (5T.3.7): a plan is decided once. The
// decision is a move on the plan's record, so a second approve, a reject
// after an approve and a second reject are refused, and approves of one
// plan made at the same time start exactly one apply run.
func TestApprove_DecidesAPlanOnce(t *testing.T) {
	gordon := asUser("gordon", scopeApprov, scopeRun)
	t.Run("approve then approve or reject", func(t *testing.T) {
		log := &applyLog{}
		h := newHarness(t, []tasks.Definition{fixerDef(log)}, withPrincipal)
		plan := h.plan()
		if err := h.x.Approve(gordon, plan, tasks.ApproveRequest{Rows: []string{"joker"}}); err != nil {
			t.Fatalf("approve: %v", err)
		}
		if err := h.x.Approve(gordon, plan, tasks.ApproveRequest{Rows: inmates}); !isErr(err, ErrPlanDecided) {
			t.Fatalf("second approve: %v, want ErrPlanDecided", err)
		}
		if err := h.x.Approve(gordon, plan, tasks.ApproveRequest{Reject: true, Reason: "too late"}); !isErr(err, ErrPlanDecided) {
			t.Fatalf("reject after approve: %v, want ErrPlanDecided", err)
		}
		if _, rejected := h.rejectEvent(plan); rejected {
			t.Fatal("a refused reject was recorded")
		}
		r := h.waitState(h.onlyApplyRun(plan).ID, tasks.StateSucceeded, tasks.StateFailed)
		if r.State != tasks.StateSucceeded || !slices.Equal(log.got(), []string{"joker"}) {
			t.Fatalf("apply run %s (%q) applied %v, want succeeded with [joker]", r.State, r.Error, log.got())
		}
	})
	t.Run("reject then reject", func(t *testing.T) {
		h := newHarness(t, []tasks.Definition{fixerDef(&applyLog{})}, withPrincipal)
		plan := h.plan()
		if err := h.x.Approve(gordon, plan, tasks.ApproveRequest{Reject: true}); err != nil {
			t.Fatalf("reject: %v", err)
		}
		if err := h.x.Approve(gordon, plan, tasks.ApproveRequest{Reject: true}); !isErr(err, ErrPlanRejected) {
			t.Fatalf("second reject: %v, want ErrPlanRejected", err)
		}
	})
	t.Run("an approve that commits while another is staged", func(t *testing.T) {
		var fired atomic.Bool
		var during func()
		h := newHarness(t, []tasks.Definition{fixerDef(&applyLog{})}, func(o *Options) {
			withPrincipal(o)
			o.NewBatch = o.Stores.Runs.(batchOpener).NewBatch
			o.Stores.Runs = approveDuring{RunStore: o.Stores.Runs, fired: &fired, during: &during}
		})
		plan := h.plan()
		var first error
		during = func() { first = h.x.Approve(gordon, plan, tasks.ApproveRequest{Rows: []string{"penguin"}}) }
		second := h.x.Approve(gordon, plan, tasks.ApproveRequest{Rows: []string{"joker"}})
		if !fired.Load() {
			t.Fatal("no approve was staged (the interleaving did not happen)")
		}
		if first != nil {
			t.Fatalf("the approve that committed first: %v", first)
		}
		if !isErr(second, ErrPlanDecided) {
			t.Fatalf("the approve staged across another's commit: %v, want ErrPlanDecided", second)
		}
		if runs := h.applyRuns(plan); len(runs) != 1 {
			t.Fatalf("%d apply runs of one plan, want 1", len(runs))
		}
	})
	t.Run("concurrent approves", func(t *testing.T) {
		defs := []tasks.Definition{fixerDef(&applyLog{})}
		fs := vfs.NewCrashableMem()
		stores, ks := bootStores(t, fs)
		planning := onStores(t, fs, stores, ks, defs, withPrincipal, true)
		plan := planning.plan()
		if err := planning.x.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
		h := onStores(t, fs, stores, ks, defs, withPrincipal, false)
		const n = 8
		errs := make(chan error, n)
		var ready sync.WaitGroup
		ready.Add(n)
		gate := make(chan struct{})
		for range n {
			go func() {
				ready.Done()
				<-gate
				errs <- h.x.Approve(gordon, plan, tasks.ApproveRequest{Rows: []string{"penguin"}})
			}()
		}
		ready.Wait()
		close(gate)
		won := 0
		for range n {
			switch err := <-errs; {
			case err == nil:
				won++
			case !isErr(err, ErrPlanDecided):
				t.Errorf("a losing approve: %v, want ErrPlanDecided", err)
			}
		}
		if won != 1 {
			t.Fatalf("%d approves of one plan succeeded, want 1", won)
		}
		if runs := h.applyRuns(plan); len(runs) != 1 {
			t.Fatalf("%d apply runs of one plan, want 1", len(runs))
		}
	})
}

// approveDuring is a run store whose first AppendEvent of an approve runs
// during before staging the event: another approval of the plan commits
// while the first is staged, after its Transition and its re-read and
// before the event's read of the run row. Storage used to let that second
// read replace the Transition's precondition, so both approvals committed
// (found by the concurrent cell under -race; storage's
// TestGetForUpdateKeepsTheFirstReadsPrecondition pins the rule).
type approveDuring struct {
	tasks.RunStore
	fired  *atomic.Bool
	during *func()
}

func (s approveDuring) AppendEvent(ctx context.Context, b *storage.Batch, id tasks.RunID, seq uint64, e tasks.RunEvent) error {
	if e.Event == eventApprove && s.fired.CompareAndSwap(false, true) {
		(*s.during)()
	}
	return s.RunStore.AppendEvent(ctx, b, id, seq, e)
}

// Fold fixtures: gotham.lockdown is a Task kind with an Approval that does
// not require a plan, and a Dedupe policy that merges every start into a
// queued run of its kind and mode, so a fold is visible in params.
const (
	lockdown      = "gotham.lockdown"
	scopeGothamGo = rbac.Permission("gotham:run")
	scopeGothamOK = rbac.Permission("gotham:approve")
)

func lockdownDef() tasks.Definition {
	return taskDef(lockdown, okBody, func(c *tasks.Common) {
		c.Perms.Approve = rbac.Need(scopeGothamOK)
		c.Approval = &tasks.Approval{}
		c.Dedupe = tasks.DedupePolicy{Merge: func(_, incoming json.RawMessage) (json.RawMessage, bool, error) {
			return incoming, true, nil
		}}
	})
}

// commissioner holds the lockdown kind's start and approve scopes.
func commissioner() context.Context { return asUser("gordon", scopeGothamGo, scopeGothamOK) }

// startAs starts kind as ctx's caller and returns the run reference.
func (h *harness) startAs(ctx context.Context, kind string, req tasks.StartRequest) tasks.RunRef {
	h.t.Helper()
	ref, err := h.x.Start(ctx, tasks.Kind(kind), req)
	if err != nil {
		h.t.Fatalf("start %s: %v", kind, err)
	}
	return ref
}

// planned boots a running executor over fresh stores, has it finish one
// Preview run of kind per ctx (the plans), shuts it down and returns an
// executor over the same stores that is not dispatching, so every start
// and approve the test makes stays queued where a fold could reach it.
func planned(t *testing.T, defs []tasks.Definition, ctx context.Context, kind string, n int) (*harness, []tasks.RunID) {
	t.Helper()
	fs := vfs.NewCrashableMem()
	stores, ks := bootStores(t, fs)
	planning := onStores(t, fs, stores, ks, defs, withPrincipal, true)
	var plans []tasks.RunID
	for range n {
		ref := planning.startAs(ctx, kind, tasks.StartRequest{Mode: tasks.ModeRequestPreview})
		planning.waitState(ref.ID, tasks.StateSucceeded)
		plans = append(plans, ref.ID)
	}
	if err := planning.x.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	return onStores(t, fs, stores, ks, defs, withPrincipal, false), plans
}

// TestApprove_NeverFoldsIntoAnotherRun (5T.3.7, review of #4586): a plan's
// apply run is always a new run. An approve never folds into a queued run
// of its kind (Live or Preview), a later Start or plan run never folds into
// a queued apply run, and two plans approved one after the other on a
// folding Fixer each apply exactly their own rows. HEAD had a production
// incident of this shape on 2026-08-21: one plan's approval landed on
// another plan's run.
func TestApprove_NeverFoldsIntoAnotherRun(t *testing.T) {
	t.Run("approve beside queued runs of every mode", func(t *testing.T) {
		h, plans := planned(t, []tasks.Definition{lockdownDef()}, commissioner(), lockdown, 1)
		live := h.startAs(commissioner(), lockdown, tasks.StartRequest{Mode: tasks.ModeRequestLive, Params: mustParams(t, "narrows")})
		preview := h.startAs(commissioner(), lockdown, tasks.StartRequest{Mode: tasks.ModeRequestPreview, Params: mustParams(t, "docks")})
		if err := h.x.Approve(commissioner(), plans[0], tasks.ApproveRequest{}); err != nil {
			t.Fatalf("approve: %v", err)
		}
		apply := h.onlyApplyRun(plans[0])
		if apply.ID == live.ID || apply.ID == preview.ID {
			t.Fatalf("the approve folded into queued run %s", apply.ID)
		}
		for id, want := range map[tasks.RunID]string{live.ID: "narrows", preview.ID: "docks"} {
			if r := h.get(id); r.PlanRef != "" || string(r.Params) != string(mustParams(t, want)) {
				t.Fatalf("queued run %s became plan %q params %s", id, r.PlanRef, r.Params)
			}
		}
	})
	t.Run("later starts beside a queued apply run", func(t *testing.T) {
		h, plans := planned(t, []tasks.Definition{lockdownDef()}, commissioner(), lockdown, 1)
		if err := h.x.Approve(commissioner(), plans[0], tasks.ApproveRequest{}); err != nil {
			t.Fatalf("approve: %v", err)
		}
		apply := h.onlyApplyRun(plans[0])
		for _, mode := range []tasks.ModeRequest{tasks.ModeRequestLive, tasks.ModeRequestPreview} {
			ref := h.startAs(commissioner(), lockdown, tasks.StartRequest{Mode: mode, Params: mustParams(t, "bowery")})
			if ref.ID == apply.ID || ref.Existing && h.get(ref.ID).PlanRef != "" {
				t.Fatalf("a %v start folded into apply run %s", mode, apply.ID)
			}
		}
		if r := h.get(apply.ID); r.PlanRef != plans[0] || string(r.Params) != string(apply.Params) {
			t.Fatalf("apply run changed: plan %q params %s, was %q %s", r.PlanRef, r.Params, plans[0], apply.Params)
		}
	})
	t.Run("two plans on a folding Fixer", func(t *testing.T) {
		log := &applyLog{}
		folding := func(s *tasks.FixerSpec[noParams, string]) { s.Dedupe = tasks.DedupePolicy{AnyParams: true} }
		defs := []tasks.Definition{fixerDef(log, folding)}
		h, plans := planned(t, defs, planner(), relocate, 2)
		gordon := asUser("gordon", scopeApprov, scopeRun)
		if err := h.x.Approve(gordon, plans[0], tasks.ApproveRequest{Rows: []string{"joker"}}); err != nil {
			t.Fatalf("approve P1: %v", err)
		}
		if err := h.x.Approve(gordon, plans[1], tasks.ApproveRequest{Rows: []string{"penguin", "riddler"}}); err != nil {
			t.Fatalf("approve P2: %v", err)
		}
		if err := h.x.Approve(gordon, plans[1], tasks.ApproveRequest{Reject: true}); !isErr(err, ErrPlanDecided) {
			t.Fatalf("reject of the approved P2: %v, want ErrPlanDecided", err)
		}
		if err := h.x.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
		for i, want := range []int{1, 2} {
			r := h.waitState(h.onlyApplyRun(plans[i]).ID, tasks.StateSucceeded, tasks.StateFailed)
			if r.State != tasks.StateSucceeded || result(t, r).Applied != want {
				t.Fatalf("P%d's apply run: %s (%q) %+v, want succeeded with %d applied", i+1, r.State, r.Error, result(t, r), want)
			}
		}
		if got := log.got(); !slices.Equal(got, inmates) {
			t.Fatalf("applied %v, want each plan's own rows %v", got, inmates)
		}
	})
}
