// file: core/internal/taskhttp/runs_test.go
// version: 1.0.0
// guid: 3e096d62-c3b4-483f-a43d-5760e209fdbd
// last-edited: 2026-11-11

package taskhttp

import (
	"fmt"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/tasks"
)

// TestRunView_RedactedOnRESTRoutes: every route that serves a RunView passes
// it through tasks.RedactRunView. Current (free text that can name a person)
// is blank unless the caller holds core:users.view; a start, a replay, a
// cancel, an approve and a decide answer with the same view.
func TestRunView_RedactedOnRESTRoutes(t *testing.T) {
	plain := session("alfred", scopeView, scopeRun, scopeApprove, scopeManage)
	named := session("alfred", scopeView, scopeRun, scopeApprove, scopeManage, scopeUsers)

	seed := func(g *rig) {
		g.client.add(view("run-1", kindSweep, tasks.StateRunning, "alfred"))
		g.client.add(view("run-2", kindRelocate, tasks.StateSucceeded, "alfred"))
		g.client.add(view("run-3", kindSweep, tasks.StateAwaitingDecision, "alfred"))
	}
	routes := []struct {
		name string
		r    req
		one  bool // the body is one run, else {"runs": [...]}
	}{
		{"get run", req{method: "GET", path: "/runs/run-1"}, true},
		{"list runs", req{method: "GET", path: "/runs"}, false},
		{"timeline", req{method: "GET", path: "/timeline"}, false},
		{"start", req{method: "POST", path: "/definitions/batcave.sweep/runs"}, true},
		{"cancel", req{method: "POST", path: "/runs/run-1/cancel"}, true},
		{"approve", req{method: "POST", path: "/runs/run-2/approve", body: `{}`}, true},
		{"decide", req{method: "POST", path: "/runs/run-3/decide", body: `{"decision":"resume"}`}, true},
	}
	for _, rt := range routes {
		for _, c := range []struct {
			who   string
			as    *auth.Principal
			shown bool
		}{{"without core:users.view", plain, false}, {"with core:users.view", named, true}} {
			t.Run(rt.name+" "+c.who, func(t *testing.T) {
				g := newRig(t)
				seed(g)
				g.client.timeline = []tasks.RunView{view("run-1", kindSweep, tasks.StateRunning, "alfred")}
				r := rt.r
				r.as = c.as
				rec := g.do(r)
				if rec.Code != 200 && rec.Code != 202 {
					t.Fatalf("= %d\n%s", rec.Code, rec.Body.String())
				}
				var runs []runDTO
				if rt.one {
					var v runDTO
					decode(t, rec, &v)
					runs = []runDTO{v}
				} else {
					var b struct{ Runs []runDTO }
					decode(t, rec, &b)
					runs = b.Runs
				}
				if len(runs) == 0 {
					t.Fatal("no run in the response")
				}
				for _, v := range runs {
					if v.ID == "" {
						t.Fatalf("empty run: %+v", v)
					}
					if c.shown != (v.Current != "") {
						t.Fatalf("Current = %q, shown want %v", v.Current, c.shown)
					}
				}
				if !c.shown && (strings.Contains(rec.Body.String(), "Dent") || strings.Contains(rec.Body.String(), "Selina")) {
					t.Fatalf("the redacted text is in the raw body: %s", rec.Body.String())
				}
			})
		}
	}
}

func TestRunsList(t *testing.T) {
	as := session("alfred", scopeView)
	g := newRig(t)
	for i := 1; i <= 5; i++ {
		g.client.add(view(fmt.Sprintf("run-%d", i), kindSweep, tasks.StateSucceeded, "bruce"))
	}
	rec := g.do(req{method: "GET", path: "/runs?kind=batcave.sweep&state=succeeded&parent=run-0&after=run-0&since=2026-11-01T00:00:00Z&limit=2", as: as})
	var b struct {
		Runs      []runDTO
		NextAfter string `json:"next_after"`
	}
	decode(t, rec, &b)
	if len(b.Runs) != 2 || b.NextAfter != "run-2" {
		t.Fatalf("page = %+v next %q, want two runs and next_after run-2", b.Runs, b.NextAfter)
	}
	q := g.client.runsQ
	if len(q.Kinds) != 1 || q.Kinds[0] != kindSweep || q.States[0] != tasks.StateSucceeded || q.Parent != "run-0" || q.After != "run-0" || q.Since.IsZero() || q.Limit != 2 {
		t.Fatalf("RunQuery = %+v", q)
	}
	rec = g.do(req{method: "GET", path: "/runs?limit=100", as: as})
	b.Runs, b.NextAfter = nil, ""
	decode(t, rec, &b)
	if len(b.Runs) != 5 || b.NextAfter != "" {
		t.Fatalf("short page = %d runs next %q, want 5 and no cursor", len(b.Runs), b.NextAfter)
	}
	for _, q := range []string{"limit=0", "limit=1001", "kind=NOPE", "state=asleep", "since=soon"} {
		wantProblem(t, g.do(req{method: "GET", path: "/runs?" + q, as: as}), 422, "validation_failed")
	}
	g.client.failWith("Runs", fmt.Errorf("boom: disk path /var/lib/x"))
	rec = g.do(req{method: "GET", path: "/runs", as: as})
	wantProblem(t, rec, 500, "internal")
	if strings.Contains(rec.Body.String(), "/var/lib") {
		t.Fatalf("an internal error's detail leaked: %s", rec.Body.String())
	}
}

func TestRunGet(t *testing.T) {
	g := newRig(t)
	v := view("run-1", kindSweep, tasks.StateSucceeded, "bruce")
	v.Parent, v.PlanRef, v.Held, v.Reason, v.Done, v.Total = "run-0", "run-p", true, "module missing", 3, 9
	g.client.add(v)
	rec := g.do(req{method: "GET", path: "/runs/run-1", as: session("alfred", scopeView)})
	var got runDTO
	decode(t, rec, &got)
	if got.ID != "run-1" || got.Kind != "batcave.sweep" || got.Mode != "live" || got.State != "succeeded" || !got.Held || got.Reason != "module missing" ||
		got.Parent != "run-0" || got.PlanRef != "run-p" || got.Done != 3 || got.Total != 9 || got.Trigger != "manual" || got.Actor.UserID != "bruce" {
		t.Fatalf("run = %+v", got)
	}
	wantProblem(t, g.do(req{method: "GET", path: "/runs/run-none", as: session("alfred", scopeView)}), 404, "tasks.run_not_found")
}

func TestRunRows(t *testing.T) {
	as := session("alfred", scopeView)
	g := newRig(t)
	planRun(g, "run-plan")
	for _, id := range []string{"cell-1", "cell-2", "cell-3"} {
		g.runs.rows = append(g.runs.rows, tasks.Row{
			ID: "plan:" + id, Title: "Move " + id, Reason: "Joker escaped", Risk: tasks.RiskHigh, Class: "transfer",
			Current: map[string]string{"wing": "A"}, Proposed: map[string]string{"wing": "B"}, Evidence: []string{"guard report"},
			Subjects: []entity.Ref{{Module: "arkham", Kind: "cell", ID: id}},
			Inputs:   map[string]string{"secret": "bookkeeping"},
		})
	}
	g.runs.rows = append(g.runs.rows, tasks.Row{ID: "not-a-plan-row"})

	rec := g.do(req{method: "GET", path: "/runs/run-plan/rows?limit=2", as: as})
	var b struct {
		Rows      []rowDTO
		NextAfter string `json:"next_after"`
	}
	decode(t, rec, &b)
	if len(b.Rows) != 2 || b.Rows[0].ID != "cell-1" || b.NextAfter != "cell-2" {
		t.Fatalf("page 1 = %+v next %q", b.Rows, b.NextAfter)
	}
	r0 := b.Rows[0]
	if r0.Title != "Move cell-1" || r0.Risk != "high" || r0.Current["wing"] != "A" || r0.Proposed["wing"] != "B" || len(r0.Subjects) != 1 || r0.Subjects[0].ID != "cell-1" {
		t.Fatalf("row = %+v", r0)
	}
	if strings.Contains(rec.Body.String(), "bookkeeping") || strings.Contains(rec.Body.String(), "plan:") {
		t.Fatalf("the response carries the stored prefix or the definition's inputs: %s", rec.Body.String())
	}
	rec = g.do(req{method: "GET", path: "/runs/run-plan/rows?limit=2&after=cell-2", as: as})
	b.Rows, b.NextAfter = nil, ""
	decode(t, rec, &b)
	if len(b.Rows) != 1 || b.Rows[0].ID != "cell-3" || b.NextAfter != "" {
		t.Fatalf("page 2 = %+v next %q", b.Rows, b.NextAfter)
	}
	g.client.hidden["run-plan"] = true
	wantProblem(t, g.do(req{method: "GET", path: "/runs/run-plan/rows", as: as}), 404, "tasks.run_not_found")
}

func TestRunItems(t *testing.T) {
	as := session("alfred", scopeView)
	g := newRig(t)
	g.client.add(view("run-1", kindSweep, tasks.StateFailed, "bruce"))
	for i := 1; i <= 3; i++ {
		g.runs.items = append(g.runs.items, tasks.PageEntry{Key: fmt.Sprintf("k%d", i), Item: tasks.ItemOutcome{Key: fmt.Sprintf("trophy-%d", i), Outcome: "failed", Error: "dusty", Chunk: i}})
	}
	g.runs.items = append(g.runs.items, tasks.PageEntry{Key: "k4", Item: tasks.ItemOutcome{Key: "trophy-4", Outcome: "skipped:gone"}})

	rec := g.do(req{method: "GET", path: "/runs/run-1/items?limit=2", as: as})
	var b struct {
		Items     []itemDTO
		NextAfter string `json:"next_after"`
	}
	decode(t, rec, &b)
	if len(b.Items) != 2 || b.Items[0].Key != "trophy-1" || b.Items[0].Error != "dusty" || b.NextAfter != "k2" {
		t.Fatalf("page = %+v next %q", b.Items, b.NextAfter)
	}
	if g.runs.pageQ.Page != tasks.PageItems {
		t.Fatalf("PageQuery = %+v, want the items page", g.runs.pageQ)
	}
	rec = g.do(req{method: "GET", path: "/runs/run-1/items?outcome=skipped&after=k0", as: as})
	b.Items, b.NextAfter = nil, ""
	decode(t, rec, &b)
	if len(b.Items) != 1 || b.Items[0].Outcome != "skipped:gone" {
		t.Fatalf("outcome filter = %+v", b.Items)
	}
	g.client.hidden["run-1"] = true
	wantProblem(t, g.do(req{method: "GET", path: "/runs/run-1/items", as: as}), 404, "tasks.run_not_found")
}

func TestCountsAndPause(t *testing.T) {
	view1 := session("alfred", scopeView)
	manage := session("lucius", scopeManage)
	g := newRig(t)
	g.runs.counts = map[tasks.RunState]int64{tasks.StateRunning: 2, tasks.StateSucceeded: 40}
	rec := g.do(req{method: "GET", path: "/counts", as: view1})
	var c struct {
		Counts map[string]int64
		Total  int64
	}
	decode(t, rec, &c)
	if c.Counts["running"] != 2 || c.Counts["succeeded"] != 40 || c.Total != 42 {
		t.Fatalf("counts = %+v", c)
	}

	var p pauseDTO
	decode(t, g.do(req{method: "GET", path: "/pause", as: view1}), &p)
	if p.Paused {
		t.Fatalf("pause = %+v before any pause", p)
	}
	rec = g.do(req{method: "POST", path: "/pause", body: `{"reason": "Bane is in the cave"}`, as: manage})
	decode(t, rec, &p)
	if rec.Code != 200 || !p.Paused || p.Reason != "Bane is in the cave" || p.By == nil || p.By.UserID != "bruce" {
		t.Fatalf("pause = %d %+v", rec.Code, p)
	}
	decode(t, g.do(req{method: "GET", path: "/pause", as: view1}), &p)
	if !p.Paused {
		t.Fatal("the pause does not read back")
	}
	decode(t, g.do(req{method: "POST", path: "/unpause", as: manage}), &p)
	if p.Paused || g.client.unpauses != 1 {
		t.Fatalf("after unpause %+v, unpauses %d", p, g.client.unpauses)
	}
	wantProblem(t, g.do(req{method: "POST", path: "/pause", body: `{"reason": "` + strings.Repeat("r", 513) + `"}`, as: manage}), 422, "validation_failed")
	wantProblem(t, g.do(req{method: "POST", path: "/pause", body: `{"why": "x"}`, as: manage}), 400, "bad_request")
	if len(g.client.pauses) != 1 {
		t.Fatalf("pauses = %v", g.client.pauses)
	}
}

func TestDefinitions(t *testing.T) {
	g := newRig(t)
	// A caller holding only the start scope may not view batcave.scan (View batcave:view).
	rec := g.do(req{method: "GET", path: "/definitions", as: session("vicki", scopeView, scopeRun)})
	var b struct{ Definitions []definitionDTO }
	decode(t, rec, &b)
	got := map[string]definitionDTO{}
	for _, d := range b.Definitions {
		got[d.Kind] = d
	}
	if len(got) != 2 || got["batcave.scan"].Kind != "" {
		t.Fatalf("listing = %v, want sweep and relocate but not the view-gated scan", got)
	}
	sw := got["batcave.sweep"]
	if sw.Module != "batcave" || sw.Title != "Sweep the Batcave" || sw.Shape != "task" || sw.ReadOnly || !sw.CanStart || sw.CanApprove || sw.ScheduleKey != sweepKey || sw.Approval != nil {
		t.Fatalf("sweep = %+v", sw)
	}
	rel := got["arkham.relocate"]
	if rel.Approval == nil || !rel.Approval.PlanRequired || rel.Approval.MaxPlanAgeSecs <= 0 || rel.CanApprove {
		t.Fatalf("relocate = %+v", rel)
	}

	rec = g.do(req{method: "GET", path: "/definitions", as: session("vicki", scopeView, scopeSee, scopeApprove)})
	decode(t, rec, &b)
	got = map[string]definitionDTO{}
	for _, d := range b.Definitions {
		got[d.Kind] = d
	}
	if len(got) != 3 || !got["batcave.scan"].ReadOnly || !got["arkham.relocate"].CanApprove || got["batcave.scan"].CanStart {
		t.Fatalf("full listing = %v", got)
	}

	var one definitionDTO
	rec = g.do(req{method: "GET", path: "/definitions/batcave.sweep", as: session("vicki", scopeView)})
	decode(t, rec, &one)
	if rec.Code != 200 || one.Kind != "batcave.sweep" || one.CanStart {
		t.Fatalf("get = %d %+v", rec.Code, one)
	}
	wantProblem(t, g.do(req{method: "GET", path: "/definitions/batcave.scan", as: session("vicki", scopeView)}), 404, "tasks.unknown_kind")
	wantProblem(t, g.do(req{method: "GET", path: "/definitions/gotham.riddle", as: session("vicki", scopeView)}), 404, "tasks.unknown_kind")
	wantProblem(t, g.do(req{method: "GET", path: "/definitions/NOT-A-KIND", as: session("vicki", scopeView)}), 404, "tasks.unknown_kind")
}
