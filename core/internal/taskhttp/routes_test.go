// file: core/internal/taskhttp/routes_test.go
// version: 1.0.0
// guid: ef0e55e6-6121-4380-9813-14e66641663b
// last-edited: 2026-11-11

package taskhttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/internal/executor"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// concretePath fills a route's path parameters with fixture values.
func concretePath(path string) string {
	path = strings.ReplaceAll(path, "{kind}", string(kindSweep))
	return strings.ReplaceAll(path, "{id}", "run-1")
}

// TestRoutes_Table pins the route table to 05-tasks 5T.3.10: paths, methods
// and floors, and that every route has a distinct operation name.
func TestRoutes_Table(t *testing.T) {
	want := []struct{ method, path, floor string }{
		{"GET", "/definitions", "authenticated"},
		{"GET", "/definitions/{kind}", "authenticated"},
		{"POST", "/definitions/{kind}/runs", "authenticated"},
		{"GET", "/runs", "authenticated"},
		{"GET", "/runs/{id}", "authenticated"},
		{"GET", "/runs/{id}/rows", "authenticated"},
		{"GET", "/runs/{id}/items", "authenticated"},
		{"GET", "/runs/{id}/log", "authenticated"},
		{"POST", "/runs/{id}/cancel", "authenticated"},
		{"DELETE", "/runs/{id}", "authenticated"},
		{"POST", "/runs/{id}/approve", "authenticated"},
		{"POST", "/runs/{id}/decide", "authenticated"},
		{"GET", "/counts", "core:tasks.view"},
		{"GET", "/timeline", "core:tasks.view"},
		{"GET", "/schedules", "core:tasks.view"},
		{"PATCH", "/schedules/{kind}", "core:tasks.manage"},
		{"GET", "/pause", "core:tasks.view"},
		{"POST", "/pause", "core:tasks.manage"},
		{"POST", "/unpause", "core:tasks.manage"},
	}
	routes := newRig(t).srv.Routes()
	if len(routes) != len(want) {
		t.Fatalf("routes = %d, want %d", len(routes), len(want))
	}
	names := map[string]bool{}
	for i, w := range want {
		r := routes[i]
		if r.Method != w.method || r.Path != w.path {
			t.Errorf("route %d = %s %s, want %s %s", i, r.Method, r.Path, w.method, w.path)
		}
		if w.floor == "authenticated" {
			if r.Perm.Kind() != rbac.KindAuthenticated {
				t.Errorf("%s %s floor = %s, want any authenticated", r.Method, r.Path, r.Perm)
			}
		} else if r.Perm.String() != w.floor {
			t.Errorf("%s %s floor = %s, want %s", r.Method, r.Path, r.Perm, w.floor)
		}
		if r.Name == "" || names[r.Name] || r.Summary == "" || r.Handler == nil {
			t.Errorf("route %s %s: name %q summary %q handler set %v", r.Method, r.Path, r.Name, r.Summary, r.Handler != nil)
		}
		names[r.Name] = true
		if r.Pattern() != r.Method+" "+BasePath+r.Path {
			t.Errorf("Pattern = %q", r.Pattern())
		}
	}
}

// TestRoutes_PermissionTable: every route against every credential class.
// No principal is 401; a principal short of the floor is 403 naming it; a
// principal at the floor passes the front door. The session and key cells
// need core authentication and are F4-05's.
func TestRoutes_PermissionTable(t *testing.T) {
	g := newRig(t)
	g.client.add(view("run-1", kindSweep, tasks.StateSucceeded, "bruce"))
	for _, r := range g.srv.Routes() {
		name := r.Method + " " + r.Path
		t.Run(name, func(t *testing.T) {
			rq := req{method: r.Method, path: concretePath(r.Path)}

			rec := g.do(rq)
			wantProblem(t, rec, 401, "unauthenticated")

			rq.as = who("nobody", auth.MethodSession) // signed in, holds nothing
			wantProblem(t, g.do(rq), 403, "permission_denied")

			rq.as = &auth.Principal{User: &auth.User{ID: "nobody"}, Method: auth.MethodSession} // no checker at all
			wantProblem(t, g.do(rq), 403, "permission_denied")

			if r.Perm.Kind() != rbac.KindAuthenticated {
				rq.as = session("lucius", scopeRun, scopeSee) // holds scopes, not the floor
				rec = g.do(rq)
				wantProblem(t, rec, 403, "permission_denied")
				if !strings.Contains(rec.Body.String(), r.Perm.String()) {
					t.Fatalf("the refusal does not name %s: %s", r.Perm, rec.Body.String())
				}
				rq.as = session("lucius", r.Perm.Permissions()[0])
			} else {
				rq.as = session("lucius", scopeRun, scopeView, scopeManage, scopeApprove)
			}
			if rec = g.do(rq); rec.Code == 401 || (rec.Code == 403 && code(t, rec) == "permission_denied" && strings.Contains(rec.Body.String(), r.Perm.String()) && r.Perm.Kind() != rbac.KindAuthenticated) {
				t.Fatalf("a caller holding the floor was refused: %d %s", rec.Code, rec.Body.String())
			}
		})
	}
	t.Run("session cells", func(t *testing.T) { t.Skip("needs core auth (F4-05)") })
	t.Run("api key cells", func(t *testing.T) { t.Skip("needs core auth (F4-05)") })
}

func TestNew_RefusesAMissingDependency(t *testing.T) {
	ok := func() Deps {
		g := newRig(t)
		return Deps{Client: g.client, Approver: g.appr, Runs: g.runs, Definitions: defs(), Config: g.cfg, Audit: g.aud}
	}
	for _, c := range []struct {
		name string
		edit func(*Deps)
		want error
	}{
		{"client", func(d *Deps) { d.Client = nil }, errNoClient},
		{"approver", func(d *Deps) { d.Approver = nil }, errNoApprover},
		{"runs", func(d *Deps) { d.Runs = nil }, errNoRuns},
		{"config", func(d *Deps) { d.Config = nil }, errNoConfig},
		{"audit", func(d *Deps) { d.Audit = nil }, errNoAudit},
		{"nil definition", func(d *Deps) { d.Definitions = append(d.Definitions, nil) }, errNilDef},
		{"duplicate kind", func(d *Deps) { d.Definitions = append(d.Definitions, d.Definitions[0]) }, errDuplicateOf},
	} {
		d := ok()
		c.edit(&d)
		if _, err := New(d); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
	d := ok()
	d.Client, d.Audit = nil, nil
	_, err := New(d)
	if !errors.Is(err, errNoClient) || !errors.Is(err, errNoAudit) {
		t.Errorf("two missing dependencies: err = %v, want both reported", err)
	}
	if _, err := New(ok()); err != nil {
		t.Errorf("a complete Deps: %v", err)
	}
}

func TestPrincipal(t *testing.T) {
	if _, _, ok := Principal(context.Background()); ok {
		t.Fatal("a context with no principal resolved one")
	}
	ctx := auth.WithPrincipal(context.Background(), who("lucius", auth.MethodAPIKey, scopeRun))
	a, who, ok := Principal(ctx)
	if !ok || a.UserID != "lucius" || a.AuthMethod != "token" || !who.Has(scopeRun) {
		t.Fatalf("api key: %+v %v %v", a, who, ok)
	}
	a, _, _ = Principal(auth.WithPrincipal(context.Background(), session("lucius")))
	if a.AuthMethod != "session" {
		t.Fatalf("session actor method = %q", a.AuthMethod)
	}
	// A principal that carries no checker still resolves, to one that holds
	// nothing: ok must not turn false (the executor reads that as internal).
	ctx = auth.WithPrincipal(context.Background(), &auth.Principal{User: &auth.User{ID: "oswald"}, Method: auth.MethodSession})
	_, who2, ok := Principal(ctx)
	if !ok || who2 == nil || who2.Has(scopeRun) || who2.Satisfies(rbac.AnyAuthenticated) || who2.Satisfies(rbac.Need(scopeRun)) || !who2.Confined() || len(who2.Concrete()) != 0 {
		t.Fatalf("no-checker principal resolved to %v ok=%v", who2, ok)
	}
}

func TestDefaultVerifiedMethod(t *testing.T) {
	need := rbac.Need(scopeApprove)
	for _, c := range []struct {
		p    *auth.Principal
		want bool
	}{
		{nil, false},
		{who("a", auth.MethodSession), true},
		{who("a", auth.MethodSessionDelegated), false},
		{who("a", auth.MethodAPIKey), false},
		{who("a", auth.MethodAuthDisabled), false},
		{who("a", auth.Method("ext:cfaccess")), false},
		{who("a", auth.MethodNone), false},
	} {
		method := auth.MethodNone
		if c.p != nil {
			method = c.p.Method
		}
		if got := DefaultVerifiedMethod(c.p, need); got != c.want {
			t.Errorf("%q verified = %v, want %v", method, got, c.want)
		}
	}
}

func TestMapError(t *testing.T) {
	for _, c := range []struct {
		err    error
		status int
		code   string
	}{
		{executor.ErrUnknownKind, 404, "tasks.unknown_kind"},
		{tasks.ErrRunNotFound, 404, "tasks.run_not_found"},
		{executor.ErrHeld, 409, "tasks.held"},
		{executor.ErrNotPermitted, 403, "tasks.actor_not_permitted"},
		{tasks.ErrRunActive, 409, "tasks.run_active"},
		{tasks.ErrRunConflict, 409, "tasks.run_changed"},
		{executor.ErrInvalidPriority, 422, "tasks.invalid_priority"},
		{executor.ErrPlanRequired, 422, "tasks.plan_required"},
		{executor.ErrApprovalRequired, 422, "tasks.approval_required"},
		{executor.ErrPlanInvalid, 422, "tasks.plan_invalid"},
		{executor.ErrRowNotInPlan, 422, "tasks.row_not_in_plan"},
		{executor.ErrPlanTooOld, 409, "tasks.plan_too_old"},
		{executor.ErrPlanRejected, 409, "tasks.plan_rejected"},
		{executor.ErrPlanDecided, 409, "tasks.plan_decided"},
		{executor.ErrNoApproval, 409, "tasks.no_approval"},
		{executor.ErrNotAwaitingDecision, 409, "tasks.not_awaiting_decision"},
		{executor.ErrShuttingDown, 503, "unavailable"},
		{tasks.ErrModeUnset, 400, "tasks.mode_unset"},
		{config.ErrLocked, 409, "tasks.schedule_locked"},
		{config.ErrInvalid, 422, "validation_failed"},
		{config.ErrProtected, 403, "permission_denied"},
		{config.ErrForbidden, 403, "permission_denied"},
		{fmt.Errorf("decode params: %w", syntaxError()), 422, "tasks.invalid_params"},
	} {
		p, ok := mapError(fmt.Errorf("wrapped: %w", c.err))
		if !ok || p.Status != c.status || p.Code != c.code {
			t.Errorf("%v -> %d %s ok=%v, want %d %s", c.err, p.Status, p.Code, ok, c.status, c.code)
		}
	}
	if _, ok := mapError(errors.New("pebble: corrupt")); ok {
		t.Error("an unmapped error mapped")
	}
}

// syntaxError is a decoder error of the kind a definition's params produce.
func syntaxError() error {
	var v any
	return json.Unmarshal([]byte("{"), &v)
}
