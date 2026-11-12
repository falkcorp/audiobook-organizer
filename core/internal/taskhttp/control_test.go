// file: core/internal/taskhttp/control_test.go
// version: 1.0.2
// guid: 3b59f28b-7bb8-4285-b43e-2e7ed3a77861
// last-edited: 2026-11-11

package taskhttp

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/internal/executor"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

func approveReq(id, body string, as *auth.Principal) req {
	return req{method: "POST", path: "/runs/" + id + "/approve", body: body, as: as}
}

func decideReq(id, decision string, as *auth.Principal) req {
	return req{method: "POST", path: "/runs/" + id + "/decide", body: `{"decision": "` + decision + `"}`, as: as}
}

// planRun seeds a plan run of the approval-gated kind.
func planRun(g *rig, id string) {
	g.client.add(view(id, kindRelocate, tasks.StateSucceeded, "bruce"))
}

// TestApprove_RequiresVerifiedMethodAndPermission: approve needs the
// definition's Perms.Approve AND a verified method (SEC-24). The matrix is
// every credential class against both rules. The cells over real core auth
// principals (the interactive extension, the key naming the scope, core:admin
// implication, a module method, a forwarded header) are
// TestApprove_OverCoreAuth in realauth_test.go.
func TestApprove_RequiresVerifiedMethodAndPermission(t *testing.T) {
	cells := []struct {
		name     string
		as       *auth.Principal
		verified func(*auth.Principal, rbac.Requirement) bool
		status   int
		code     string
		approved bool
	}{
		{name: "interactive session holding the scope", as: session("lucius", scopeApprove), status: 200, approved: true},
		{name: "interactive session without the scope", as: session("lucius", scopeSee), status: 403, code: "permission_denied"},
		{name: "api key holding the scope", as: who("lucius", auth.MethodAPIKey, scopeApprove), status: 403, code: "tasks.verified_method_required"},
		{name: "delegated session holding the scope", as: who("lucius", auth.MethodSessionDelegated, scopeApprove), status: 403, code: "tasks.verified_method_required"},
		{name: "auth-disabled principal holding the scope", as: who("lucius", auth.MethodAuthDisabled, scopeApprove), status: 403, code: "tasks.verified_method_required"},
		{name: "api key accepted by the kernel's widened rule", as: who("lucius", auth.MethodAPIKey, scopeApprove), verified: func(*auth.Principal, rbac.Requirement) bool { return true }, status: 200, approved: true},
		{name: "session refused by a stricter rule", as: session("lucius", scopeApprove), verified: func(*auth.Principal, rbac.Requirement) bool { return false }, status: 403, code: "tasks.verified_method_required"},
	}
	for _, c := range cells {
		t.Run(c.name, func(t *testing.T) {
			g := newRig(t, func(d *Deps) { d.VerifiedMethod = c.verified })
			planRun(g, "run-plan")
			rec := g.do(approveReq("run-plan", `{"rows": ["cell-7", "cell-9"], "reason": "Gordon signed off"}`, c.as))
			if c.code != "" {
				wantProblem(t, rec, c.status, c.code)
			} else if rec.Code != c.status {
				t.Fatalf("= %d, want %d\n%s", rec.Code, c.status, rec.Body.String())
			}
			if got := len(g.appr.calls) == 1; got != c.approved {
				t.Fatalf("Approve called = %v (calls %d), want %v", got, len(g.appr.calls), c.approved)
			}
			if c.approved {
				call := g.appr.calls[0]
				if call.ID != "run-plan" || strings.Join(call.Req.Rows, ",") != "cell-7,cell-9" || call.Req.Reject || call.Req.Reason != "Gordon signed off" {
					t.Fatalf("Approve call = %+v", call)
				}
				if len(g.aud.recorded) != 1 {
					t.Fatalf("recorded = %d, want 1", len(g.aud.recorded))
				}
				e := g.aud.recorded[0]
				if e.Action != "core.task.approve" || e.Outcome != audit.Success || e.Target.ID != "run-plan" || e.Actor.UserID != "lucius" ||
					e.Route != "POST "+BasePath+"/runs/{id}/approve" || e.Details["rows"] != 2 || e.Details["kind"] != string(kindRelocate) {
					t.Fatalf("audit record = %+v", e)
				}
				return
			}
			if len(g.aud.recorded) != 0 {
				t.Fatalf("a refused approval recorded a success: %+v", g.aud.recorded)
			}
			if len(g.aud.emitted) != 1 || g.aud.emitted[0].Outcome != audit.Denied || g.aud.emitted[0].Action != "core.task.approve" {
				t.Fatalf("denial emitted = %+v, want one Denied core.task.approve", g.aud.emitted)
			}
		})
	}
}

// TestApprove_RejectAndBody: a rejection records core.task.reject and names no rows;
// bodies the route refuses reach neither the executor nor the audit log.
func TestApprove_RejectAndBody(t *testing.T) {
	as := session("lucius", scopeApprove)
	t.Run("reject records core.task.reject", func(t *testing.T) {
		g := newRig(t)
		planRun(g, "run-plan")
		rec := g.do(approveReq("run-plan", `{"reject": true, "reason": "wrong cells"}`, as))
		if rec.Code != 200 {
			t.Fatalf("= %d\n%s", rec.Code, rec.Body.String())
		}
		if !g.appr.calls[0].Req.Reject || len(g.aud.recorded) != 1 || g.aud.recorded[0].Action != "core.task.reject" {
			t.Fatalf("calls %+v recorded %+v", g.appr.calls, g.aud.recorded)
		}
		if g.aud.recorded[0].Details["reason"] != "wrong cells" {
			t.Fatalf("details = %+v", g.aud.recorded[0].Details)
		}
	})
	t.Run("refused bodies", func(t *testing.T) {
		for _, c := range []struct {
			body, code string
			status     int
		}{
			{`{"reject": true, "rows": ["cell-1"]}`, "validation_failed", 422},
			{`{"reason": "` + strings.Repeat("r", 1025) + `"}`, "validation_failed", 422},
			{`{"approver": "bruce"}`, "bad_request", 400},
			{`[`, "bad_request", 400},
		} {
			g := newRig(t)
			planRun(g, "run-plan")
			wantProblem(t, g.do(approveReq("run-plan", c.body, as)), c.status, c.code)
			if len(g.appr.calls) != 0 || len(g.aud.recorded) != 0 {
				t.Fatalf("body %q reached the executor or the audit log", c.body)
			}
		}
	})
	t.Run("unknown or unviewable run is 404", func(t *testing.T) {
		g := newRig(t)
		wantProblem(t, g.do(approveReq("run-none", `{}`, as)), 404, "tasks.run_not_found")
		planRun(g, "run-plan")
		g.client.hidden["run-plan"] = true
		wantProblem(t, g.do(approveReq("run-plan", `{}`, as)), 404, "tasks.run_not_found")
		if len(g.appr.calls) != 0 {
			t.Fatal("approve reached the executor for a run the caller cannot see")
		}
	})
	t.Run("executor refusals map", func(t *testing.T) {
		for _, c := range []struct {
			err    error
			status int
			code   string
		}{
			{executor.ErrHeld, 409, "tasks.held"},
			{executor.ErrPlanTooOld, 409, "tasks.plan_too_old"},
			{executor.ErrPlanDecided, 409, "tasks.plan_decided"},
			{executor.ErrRowNotInPlan, 422, "tasks.row_not_in_plan"},
		} {
			g := newRig(t)
			planRun(g, "run-plan")
			g.appr.err = c.err
			wantProblem(t, g.do(approveReq("run-plan", `{"rows":["cell-1"]}`, as)), c.status, c.code)
			if len(g.aud.recorded) != 0 {
				t.Fatalf("%v: a failed approval was recorded as a success", c.err)
			}
		}
	})
	t.Run("audit failure after the action", func(t *testing.T) {
		g := newRig(t)
		planRun(g, "run-plan")
		g.aud.recordErr = errors.New("audit sink down")
		wantProblem(t, g.do(approveReq("run-plan", `{}`, as)), 500, "tasks.audit_failed")
		if len(g.appr.calls) != 1 {
			t.Fatal("the approval is taken before its record; the route says so rather than hiding it")
		}
	})
	t.Run("definition not loaded still needs a verified method", func(t *testing.T) {
		g := newRig(t)
		g.client.add(view("run-ghost", tasks.Kind("gotham.retired"), tasks.StateSucceeded, "bruce"))
		wantProblem(t, g.do(approveReq("run-ghost", `{}`, who("lucius", auth.MethodAPIKey, scopeApprove))), 403, "tasks.verified_method_required")
		g.appr.err = executor.ErrHeld
		wantProblem(t, g.do(approveReq("run-ghost", `{}`, session("lucius", scopeApprove))), 409, "tasks.held")
	})
}

// TestDecide covers resume, finish and drop: the same gate as approve over
// the definition's Approve scope (else Start), the audit action by decision.
func TestDecide(t *testing.T) {
	t.Run("decisions and their audit actions", func(t *testing.T) {
		for _, c := range []struct {
			word   string
			want   tasks.Decision
			action string
		}{
			{"resume", tasks.DecideResume, "core.task.approve"},
			{"finish", tasks.DecideFinish, "core.task.approve"},
			{"DROP", tasks.DecideDrop, "core.task.reject"},
		} {
			g := newRig(t)
			g.client.add(view("run-sweep", kindSweep, tasks.StateAwaitingDecision, "bruce"))
			rec := g.do(decideReq("run-sweep", c.word, session("lucius", scopeRun)))
			if rec.Code != 200 {
				t.Fatalf("%s = %d\n%s", c.word, rec.Code, rec.Body.String())
			}
			if len(g.client.decides) != 1 || g.client.decides[0].D != c.want {
				t.Fatalf("%s: decides = %+v, want %v", c.word, g.client.decides, c.want)
			}
			if len(g.aud.recorded) != 1 || g.aud.recorded[0].Action != c.action {
				t.Fatalf("%s: recorded = %+v, want %s", c.word, g.aud.recorded, c.action)
			}
		}
	})
	t.Run("uses Approve when the definition has one, else Start", func(t *testing.T) {
		g := newRig(t)
		planRun(g, "run-plan")
		// relocate: Start batcave:run alone is not enough, Approve arkham:approve is.
		wantProblem(t, g.do(decideReq("run-plan", "resume", session("lucius", scopeRun))), 403, "permission_denied")
		if rec := g.do(decideReq("run-plan", "resume", session("lucius", scopeApprove))); rec.Code != 200 {
			t.Fatalf("approver = %d\n%s", rec.Code, rec.Body.String())
		}
		g.client.add(view("run-sweep", kindSweep, tasks.StateAwaitingDecision, "bruce"))
		wantProblem(t, g.do(decideReq("run-sweep", "resume", session("vicki", scopeSee))), 403, "permission_denied")
	})
	t.Run("verified method and body", func(t *testing.T) {
		g := newRig(t)
		g.client.add(view("run-sweep", kindSweep, tasks.StateAwaitingDecision, "bruce"))
		wantProblem(t, g.do(decideReq("run-sweep", "resume", who("lucius", auth.MethodAPIKey, scopeRun))), 403, "tasks.verified_method_required")
		wantProblem(t, g.do(decideReq("run-sweep", "maybe", session("lucius", scopeRun))), 422, "validation_failed")
		wantProblem(t, g.do(req{method: "POST", path: "/runs/run-sweep/decide", body: `{"decision":"resume","by":"bane"}`, as: session("lucius", scopeRun)}), 400, "bad_request")
		if len(g.client.decides) != 0 {
			t.Fatalf("refused decisions reached the executor: %+v", g.client.decides)
		}
	})
	t.Run("executor refusal", func(t *testing.T) {
		g := newRig(t)
		g.client.add(view("run-sweep", kindSweep, tasks.StateRunning, "bruce"))
		g.client.failWith("Decide", executor.ErrNotAwaitingDecision)
		wantProblem(t, g.do(decideReq("run-sweep", "resume", session("lucius", scopeRun))), 409, "tasks.not_awaiting_decision")
		if len(g.aud.recorded) != 0 {
			t.Fatal("a refused decision was recorded as a success")
		}
	})
}

// TestCancel: the executor decides who may cancel; this route records a
// cancel made by someone other than the run's own actor.
func TestCancel(t *testing.T) {
	cancel := func(id string, as *auth.Principal) req {
		return req{method: "POST", path: "/runs/" + id + "/cancel", as: as}
	}
	t.Run("own run is not recorded", func(t *testing.T) {
		g := newRig(t)
		g.client.add(view("run-1", kindSweep, tasks.StateRunning, "lucius"))
		if rec := g.do(cancel("run-1", session("lucius", scopeRun))); rec.Code != 200 {
			t.Fatalf("= %d\n%s", rec.Code, rec.Body.String())
		}
		if len(g.client.cancels) != 1 || len(g.aud.recorded) != 0 {
			t.Fatalf("cancels %v recorded %+v", g.client.cancels, g.aud.recorded)
		}
	})
	t.Run("another's run is recorded as core.task.cancel", func(t *testing.T) {
		g := newRig(t)
		g.client.add(view("run-1", kindSweep, tasks.StateRunning, "bruce"))
		if rec := g.do(cancel("run-1", session("lucius", scopeRun, scopeManage))); rec.Code != 200 {
			t.Fatalf("= %d\n%s", rec.Code, rec.Body.String())
		}
		if len(g.aud.recorded) != 1 {
			t.Fatalf("recorded = %+v", g.aud.recorded)
		}
		e := g.aud.recorded[0]
		if e.Action != "core.task.cancel" || e.Actor.UserID != "lucius" || e.Details["run_actor"] != "bruce" || e.Details["kind"] != string(kindSweep) {
			t.Fatalf("record = %+v", e)
		}
	})
	t.Run("a system run names its system", func(t *testing.T) {
		g := newRig(t)
		v := view("run-1", kindSweep, tasks.StateRunning, "")
		v.Actor = tasks.Actor{System: "scheduler", AuthMethod: "system"}
		g.client.add(v)
		g.do(cancel("run-1", session("lucius", scopeManage)))
		if len(g.aud.recorded) != 1 || g.aud.recorded[0].Details["run_actor_system"] != "scheduler" {
			t.Fatalf("recorded = %+v", g.aud.recorded)
		}
		if _, has := g.aud.recorded[0].Details["run_actor"]; has {
			t.Fatalf("recorded = %+v", g.aud.recorded)
		}
	})
	t.Run("a run the caller may not view is cancelled and recorded", func(t *testing.T) {
		g := newRig(t)
		g.client.add(view("run-1", kindSweep, tasks.StateRunning, "bruce"))
		g.client.hidden["run-1"] = true
		rec := g.do(cancel("run-1", session("lucius", scopeManage)))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("= %d, want 204 (the re-read is refused too)\n%s", rec.Code, rec.Body.String())
		}
		if len(g.client.cancels) != 1 || len(g.aud.recorded) != 1 || g.aud.recorded[0].Action != "core.task.cancel" {
			t.Fatalf("cancels %v recorded %+v", g.client.cancels, g.aud.recorded)
		}
		if _, has := g.aud.recorded[0].Details["run_actor"]; has {
			t.Fatalf("the record names the actor of a run the caller may not see: %+v", g.aud.recorded[0].Details)
		}
	})
	t.Run("audit failure", func(t *testing.T) {
		g := newRig(t)
		g.client.add(view("run-1", kindSweep, tasks.StateRunning, "bruce"))
		g.aud.recordErr = errors.New("audit sink down")
		wantProblem(t, g.do(cancel("run-1", session("lucius", scopeManage))), 500, "tasks.audit_failed")
	})
	t.Run("executor refusal", func(t *testing.T) {
		g := newRig(t)
		g.client.add(view("run-1", kindSweep, tasks.StateRunning, "bruce"))
		g.client.failWith("Cancel", executor.ErrNotPermitted)
		wantProblem(t, g.do(cancel("run-1", session("vicki", scopeSee))), 403, "tasks.actor_not_permitted")
		if len(g.aud.recorded) != 0 {
			t.Fatal("a refused cancel was recorded as a success")
		}
		g = newRig(t)
		g.client.failWith("Cancel", tasks.ErrRunNotFound)
		wantProblem(t, g.do(cancel("run-zz", session("vicki", scopeSee))), 404, "tasks.run_not_found")
	})
}

// TestHeld_MutationsRefused409 is the HTTP half of 5T.3.11: a run whose
// definition is held (its module is not loaded) refuses every mutation with
// 409 tasks.held, and the refusal is never recorded as a success.
func TestHeld_MutationsRefused409(t *testing.T) {
	as := session("lucius", scopeRun, scopeApprove, scopeManage)
	mutations := []struct {
		name string
		r    req
		set  func(*rig)
	}{
		{"start", startReq(``, ""), func(g *rig) { g.client.failWith("Start", executor.ErrHeld) }},
		{"cancel", req{method: "POST", path: "/runs/run-held/cancel"}, func(g *rig) { g.client.failWith("Cancel", executor.ErrHeld) }},
		{"discard", req{method: "DELETE", path: "/runs/run-held"}, func(g *rig) { g.client.failWith("Discard", executor.ErrHeld) }},
		{"decide", req{method: "POST", path: "/runs/run-held/decide", body: `{"decision":"resume"}`}, func(g *rig) { g.client.failWith("Decide", executor.ErrHeld) }},
		{"approve", req{method: "POST", path: "/runs/run-held/approve", body: `{}`}, func(g *rig) { g.appr.err = executor.ErrHeld }},
	}
	for _, m := range mutations {
		t.Run(m.name, func(t *testing.T) {
			g := newRig(t)
			held := view("run-held", kindRelocate, tasks.StateInterrupted, "bruce")
			held.Held = true
			g.client.add(held)
			m.set(g)
			r := m.r
			r.as = as
			wantProblem(t, g.do(r), http.StatusConflict, "tasks.held")
			if len(g.aud.recorded) != 0 {
				t.Fatalf("a held run's refused %s was recorded as a success", m.name)
			}
		})
	}
	// The wrapped sentinel maps the same: the executor wraps with the kind.
	g := newRig(t)
	g.client.failWith("Cancel", errors.Join(errors.New("executor: batcave.sweep"), executor.ErrHeld))
	g.client.add(view("run-held", kindSweep, tasks.StateInterrupted, "bruce"))
	wantProblem(t, g.do(req{method: "POST", path: "/runs/run-held/cancel", as: as}), 409, "tasks.held")
}

// TestTimelineLogDiscard: the core half of the timeline, log and discard
// cells (the ABO parity case is F5's).
func TestTimelineLogDiscard(t *testing.T) {
	viewer := session("alfred", scopeView, scopeUsers)

	t.Run("timeline passes its query and redacts", func(t *testing.T) {
		g := newRig(t)
		g.client.timeline = []tasks.RunView{view("run-1", kindSweep, tasks.StateSucceeded, "bruce"), view("run-2", kindScan, tasks.StateRunning, "bruce")}
		rec := g.do(req{method: "GET", path: "/timeline?kind=batcave.sweep&state=running&state=succeeded&from=2026-11-11T00:00:00Z&to=2026-11-12T00:00:00Z&limit=25", as: session("alfred", scopeView)})
		if rec.Code != 200 {
			t.Fatalf("= %d\n%s", rec.Code, rec.Body.String())
		}
		var body struct{ Runs []runDTO }
		decode(t, rec, &body)
		if len(body.Runs) != 2 || body.Runs[0].ID != "run-1" {
			t.Fatalf("runs = %+v", body.Runs)
		}
		for _, r := range body.Runs {
			if r.Current != "" {
				t.Fatalf("a caller without core:users.view saw Current %q", r.Current)
			}
		}
		q := g.client.timelineQ
		if len(q.Kinds) != 1 || q.Kinds[0] != kindSweep || len(q.States) != 2 || q.Limit != 25 || q.From.IsZero() || q.To.IsZero() {
			t.Fatalf("TimelineQuery = %+v", q)
		}
		withUsers := g.do(req{method: "GET", path: "/timeline", as: viewer})
		decode(t, withUsers, &body)
		if body.Runs[0].Current != "Harvey Dent's case file" {
			t.Fatalf("a holder of core:users.view saw Current %q", body.Runs[0].Current)
		}
	})
	t.Run("timeline bad query", func(t *testing.T) {
		g := newRig(t)
		for _, q := range []string{"kind=Not%20A%20Kind", "state=asleep", "from=yesterday", "to=tomorrow", "limit=0", "limit=1001", "limit=x"} {
			wantProblem(t, g.do(req{method: "GET", path: "/timeline?" + q, as: viewer}), 422, "validation_failed")
		}
	})
	t.Run("log pages and gates", func(t *testing.T) {
		g := newRig(t)
		g.client.add(view("run-1", kindSweep, tasks.StateRunning, "bruce"))
		for _, m := range []string{"dusted the trophy case", "polished the cowl", "found a riddle"} {
			g.client.logLines = append(g.client.logLines, tasks.LogLine{Level: "info", Message: m, Fields: map[string]string{"case": "riddler"}})
		}
		rec := g.do(req{method: "GET", path: "/runs/run-1/log?limit=2", as: viewer})
		if rec.Code != 200 {
			t.Fatalf("= %d\n%s", rec.Code, rec.Body.String())
		}
		var body struct{ Lines []logLineDTO }
		decode(t, rec, &body)
		if len(body.Lines) != 2 || body.Lines[0].Message != "dusted the trophy case" || body.Lines[0].Fields["case"] != "riddler" {
			t.Fatalf("lines = %+v", body.Lines)
		}
		if g.client.logQ.Limit != 2 || g.client.logQ.Page != tasks.PageLog {
			t.Fatalf("PageQuery = %+v", g.client.logQ)
		}
		wantProblem(t, g.do(req{method: "GET", path: "/runs/run-zz/log", as: viewer}), 404, "tasks.run_not_found")
		wantProblem(t, g.do(req{method: "GET", path: "/runs/run-1/log?limit=0", as: viewer}), 422, "validation_failed")
	})
	t.Run("discard", func(t *testing.T) {
		g := newRig(t)
		g.client.add(view("run-1", kindSweep, tasks.StateSucceeded, "bruce"))
		rec := g.do(req{method: "DELETE", path: "/runs/run-1", as: session("lucius", scopeManage)})
		if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
			t.Fatalf("= %d %q, want 204 and no body", rec.Code, rec.Body.String())
		}
		if len(g.client.discards) != 1 {
			t.Fatalf("discards = %v", g.client.discards)
		}
		wantProblem(t, g.do(req{method: "DELETE", path: "/runs/run-1", as: session("lucius", scopeManage)}), 404, "tasks.run_not_found")
		g.client.add(view("run-2", kindSweep, tasks.StateRunning, "bruce"))
		g.client.failWith("Discard", tasks.ErrRunActive)
		wantProblem(t, g.do(req{method: "DELETE", path: "/runs/run-2", as: session("lucius", scopeManage)}), 409, "tasks.run_active")
	})
}
