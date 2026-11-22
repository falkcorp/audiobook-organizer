// file: core/internal/updaterhttp/routes_test.go
// version: 1.1.0
// guid: 9a4f6b12-5c3e-4d70-8b21-e6f0a1c2d3b4
// last-edited: 2026-11-18

package updaterhttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/updater"
)

const (
	uhBase  = "/api/core/v1"
	uhBruce = "01GOTHAMBRUCEWAYNE0000000"
)

// uhChecker holds exactly its permissions; core:admin holds every one, as the
// real checker's does for an interactive principal.
type uhChecker []rbac.Permission

func (c uhChecker) Has(p rbac.Permission) bool {
	return slices.Contains(c, p) || slices.Contains(c, rbac.CoreAdmin)
}

func (c uhChecker) Satisfies(r rbac.Requirement) bool {
	ps := r.Permissions()
	switch r.Kind() {
	case rbac.KindPublic, rbac.KindAuthenticated:
		return true
	case rbac.KindAllOf:
		return len(ps) > 0 && !slices.ContainsFunc(ps, func(p rbac.Permission) bool { return !c.Has(p) })
	case rbac.KindAnyOf:
		return slices.ContainsFunc(ps, c.Has)
	}
	return false
}
func (uhChecker) Confined() bool                { return false }
func (c uhChecker) Concrete() []rbac.Permission { return slices.Clone(c) }

func uhAs(m auth.Method, perms ...rbac.Permission) *auth.Principal {
	return &auth.Principal{Method: m, Perms: uhChecker(perms), CredentialID: "cred-gotham", ClientAddr: netip.MustParseAddr("192.0.2.7"),
		User: &auth.User{ID: auth.UserID(uhBruce), Username: "bruce.wayne"}}
}

// uhUpdater is the Updater seam: a fixed status, and Prepare's answer.
type uhUpdater struct {
	st      updater.Status
	prepErr error
	mu      sync.Mutex
	prepped []string
}

func (u *uhUpdater) Status() updater.Status { return u.st }

func (u *uhUpdater) Prepare(version string) (updater.Target, error) {
	u.mu.Lock()
	u.prepped = append(u.prepped, version)
	u.mu.Unlock()
	return updater.Target{Version: version}, u.prepErr
}

// uhStarter records what the routes enqueued.
type uhStarter struct {
	mu    sync.Mutex
	calls []string
	by    []string
	apply []updater.ApplyParams
	err   error
}

func (s *uhStarter) rec(kind, by string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls, s.by = append(s.calls, kind), append(s.by, by)
	return "run-" + kind + "-gotham", s.err
}

func (s *uhStarter) StartCheck(_ context.Context, by string) (string, error) {
	return s.rec("check", by)
}
func (s *uhStarter) StartRollback(_ context.Context, by string) (string, error) {
	return s.rec("rollback", by)
}
func (s *uhStarter) StartApply(_ context.Context, p updater.ApplyParams, by string) (string, error) {
	s.mu.Lock()
	s.apply = append(s.apply, p)
	s.mu.Unlock()
	return s.rec("apply", by)
}

func (s *uhStarter) n() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

type uhRig struct {
	t    *testing.T
	mux  *http.ServeMux
	upd  *uhUpdater
	st   *uhStarter
	logs *strings.Builder
}

func newRig(t *testing.T, tweak ...func(*Deps)) *uhRig {
	t.Helper()
	r := &uhRig{t: t, upd: &uhUpdater{st: updater.Status{Mode: updater.ModeCheck, Channel: updater.ChannelStable, Current: "v0.223.0",
		Latest: "0.224.0", Available: true, LastChecked: time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)}},
		st: &uhStarter{}, logs: &strings.Builder{}}
	d := Deps{Updater: r.upd, Starter: r.st, Log: slog.New(slog.NewTextHandler(r.logs, nil))}
	for _, f := range tweak {
		f(&d)
	}
	r.mux = http.NewServeMux()
	for _, def := range (&api{upd: d.Updater, st: d.Starter, log: d.Log}).routeTable() {
		r.mux.Handle(def.method+" "+uhBase+def.path, def.handler)
	}
	return r
}

type uhReply struct {
	status int
	body   []byte
}

func (r *uhRig) do(p *auth.Principal, method, target, contentType, body string) uhReply {
	r.t.Helper()
	req := httptest.NewRequest(method, uhBase+target, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if p != nil {
		req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	}
	rec := httptest.NewRecorder()
	r.mux.ServeHTTP(rec, req)
	res := rec.Result()
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		r.t.Fatal(err)
	}
	return uhReply{status: res.StatusCode, body: b}
}

func problemCode(t *testing.T, rep uhReply) string {
	t.Helper()
	var pb httpapi.Problem
	if err := json.Unmarshal(rep.body, &pb); err != nil {
		t.Fatalf("body is not a problem: %v: %s", err, rep.body)
	}
	return pb.Code
}

type uhCase struct {
	name, method, path, perm string
	interactive              bool
	body                     string
}

func uhCases() []uhCase {
	return []uhCase{
		{"getUpdater", "GET", "/updater", "core:system.manage", false, ""},
		{"checkUpdater", "POST", "/updater/check", "core:system.manage", false, ""},
		{"applyUpdate", "POST", "/updater/apply", "core:admin", true, ""},
		{"rollbackUpdate", "POST", "/updater/rollback", "core:admin", true, ""},
	}
}

// TestUpdaterRoutes_Permissions: every route refuses a missing principal, a
// principal without its scope and, for the two that replace the binary, a
// credential that is not an interactive session; none of the refusals reaches
// the updater or enqueues anything.
func TestUpdaterRoutes_Permissions(t *testing.T) {
	for _, c := range uhCases() {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			if rep := r.do(nil, c.method, c.path, "", c.body); rep.status != 401 {
				t.Errorf("no principal: %d, want 401", rep.status)
			}
			rep := r.do(uhAs(auth.MethodSession, "core:users.view"), c.method, c.path, "", c.body)
			if rep.status != 403 || problemCode(t, rep) != "permission_denied" {
				t.Errorf("lacking the scope: %d %s, want 403 permission_denied", rep.status, rep.body)
			}
			if c.interactive {
				rep := r.do(uhAs(auth.MethodAPIKey, rbac.Permission(c.perm)), c.method, c.path, "", c.body)
				if rep.status != 403 || problemCode(t, rep) != "interactive_required" {
					t.Errorf("an API key: %d %s, want 403 interactive_required", rep.status, rep.body)
				}
				// Stage 13's order: a key lacking the scope fails the
				// interactive guard before its scopes are read.
				rep = r.do(uhAs(auth.MethodAPIKey, "core:users.view"), c.method, c.path, "", c.body)
				if rep.status != 403 || problemCode(t, rep) != "interactive_required" {
					t.Errorf("an API key lacking the scope: %d %s, want 403 interactive_required", rep.status, rep.body)
				}
			}
			// The auth-disabled principal holding the scope: a guarded route
			// refuses it as the router does; an unguarded one, which the
			// router admits, meets authorise's own rule that a route names an
			// actor; both answer 403 interactive_required, never the
			// session-ending 401 (F4-08 ruling G5(a)).
			off := uhAs(auth.MethodAuthDisabled, rbac.Permission(c.perm))
			off.User = nil // the auth-disabled principal has no user
			rep = r.do(off, c.method, c.path, "", c.body)
			if rep.status != 403 || problemCode(t, rep) != "interactive_required" {
				t.Errorf("auth disabled: %d %s, want 403 interactive_required", rep.status, rep.body)
			}
			if n := r.st.n(); n != 0 || len(r.upd.prepped) != 0 {
				t.Errorf("a refused request enqueued %d tasks and asked Prepare %d times", n, len(r.upd.prepped))
			}
		})
	}

	t.Run("core:system.manage does not start an apply or a rollback", func(t *testing.T) {
		r := newRig(t)
		for _, c := range uhCases()[2:] {
			rep := r.do(uhAs(auth.MethodSession, "core:system.manage"), c.method, c.path, "", "")
			if rep.status != 403 {
				t.Errorf("%s with core:system.manage: %d, want 403", c.name, rep.status)
			}
		}
		if r.st.n() != 0 {
			t.Error("a manage-only principal enqueued a task")
		}
	})
}

func TestUpdaterRoutes_Status(t *testing.T) {
	r := newRig(t)
	rep := r.do(uhAs(auth.MethodAPIKey, "core:system.manage"), "GET", "/updater", "", "")
	if rep.status != 200 {
		t.Fatalf("status = %d: %s", rep.status, rep.body)
	}
	var v updater.StatusView
	if err := json.Unmarshal(rep.body, &v); err != nil {
		t.Fatal(err)
	}
	if v.Mode != "check" || v.Channel != "stable" || v.Current != "v0.223.0" || v.Latest != "0.224.0" || !v.Available {
		t.Errorf("view = %+v", v)
	}
}

func TestUpdaterRoutes_CheckAndRollbackStartATask(t *testing.T) {
	r := newRig(t)
	for _, c := range []struct{ path, kind string }{{"/updater/check", "check"}, {"/updater/rollback", "rollback"}} {
		rep := r.do(uhAs(auth.MethodSession, rbac.CoreAdmin), "POST", c.path, "", "")
		if rep.status != 202 {
			t.Fatalf("%s = %d: %s", c.path, rep.status, rep.body)
		}
		var got startResponse
		if err := json.Unmarshal(rep.body, &got); err != nil || got.TaskID != "run-"+c.kind+"-gotham" {
			t.Errorf("%s body = %s (%v), want the run's ID", c.path, rep.body, err)
		}
	}
	if !slices.Equal(r.st.calls, []string{"check", "rollback"}) || !slices.Equal(r.st.by, []string{uhBruce, uhBruce}) {
		t.Errorf("enqueued %v by %v, want check and rollback by the principal's user ID", r.st.calls, r.st.by)
	}

	// An enqueue failure is a 500 whose cause stays out of the body.
	r.st.err = errors.New("the batcave's task table is full at /var/lib/gotham")
	rep := r.do(uhAs(auth.MethodSession, rbac.CoreAdmin), "POST", "/updater/check", "", "")
	if rep.status != 500 || strings.Contains(string(rep.body), "batcave") || strings.Contains(string(rep.body), "/var/lib") {
		t.Errorf("enqueue failure = %d %s, want a 500 that withholds the cause", rep.status, rep.body)
	}
	if !strings.Contains(r.logs.String(), "batcave") {
		t.Error("the cause was not logged")
	}
}

func TestUpdaterRoutes_Apply(t *testing.T) {
	admin := uhAs(auth.MethodSession, rbac.CoreAdmin)

	t.Run("a named version and an empty body both enqueue and carry the version", func(t *testing.T) {
		r := newRig(t)
		if rep := r.do(admin, "POST", "/updater/apply", "application/json", `{"version":" v0.224.0 "}`); rep.status != 202 {
			t.Fatalf("named = %d: %s", rep.status, rep.body)
		}
		if rep := r.do(admin, "POST", "/updater/apply", "", ""); rep.status != 202 {
			t.Fatalf("empty body = %d: %s", rep.status, rep.body)
		}
		if len(r.st.apply) != 2 || r.st.apply[0].Version != "v0.224.0" || r.st.apply[1].Version != "" {
			t.Errorf("enqueued %+v, want the trimmed tag and then the empty one", r.st.apply)
		}
		if !slices.Equal(r.upd.prepped, []string{"v0.224.0", ""}) {
			t.Errorf("Prepare saw %q", r.upd.prepped)
		}
	})

	t.Run("Prepare's refusals are answered and nothing is enqueued", func(t *testing.T) {
		for _, c := range []struct {
			err    error
			status int
			code   string
		}{
			{updater.ErrInvalidVersion, 422, "validation_failed"},
			{updater.ErrDisabled, 409, "updater_off"},
			{updater.ErrUnversioned, 409, "unversioned_build"},
			{updater.ErrLocalBuildNeedsVersion, 409, "local_build_needs_version"},
			{updater.ErrNoUpdate, 409, "no_update"},
			{updater.ErrNotNewer, 409, "not_newer"},
			{fmt.Errorf("wrapped: %w", updater.ErrNotNewer), 409, "not_newer"},
			{errors.New("unexpected at /srv/gotham"), 500, ""},
		} {
			r := newRig(t)
			r.upd.prepErr = c.err
			rep := r.do(admin, "POST", "/updater/apply", "application/json", `{"version":"v0.1.0"}`)
			if rep.status != c.status || (c.code != "" && problemCode(t, rep) != c.code) {
				t.Errorf("%v: %d %s, want %d %s", c.err, rep.status, rep.body, c.status, c.code)
			}
			if strings.Contains(string(rep.body), "/srv/gotham") {
				t.Errorf("an unexpected error's text reached the body: %s", rep.body)
			}
			if r.st.n() != 0 {
				t.Errorf("%v: a refused apply was enqueued", c.err)
			}
		}
	})

	t.Run("the body is strict", func(t *testing.T) {
		for _, c := range []struct {
			name, ct, body string
			status         int
			code           string
		}{
			{"unknown member", "application/json", `{"version":"v1","force":true}`, 400, "unknown_field"},
			{"not JSON", "application/json", `version=v1`, 400, "malformed_json"},
			{"trailing data", "application/json", `{"version":"v1"} {}`, 400, "malformed_json"},
			{"wrong type", "application/json", `{"version":7}`, 422, "validation_failed"},
			{"wrong content type", "text/plain", `{"version":"v1"}`, 415, "unsupported_media_type"},
			{"too large", "application/json", `{"version":"` + strings.Repeat("a", 2<<10) + `"}`, 413, "body_too_large"},
		} {
			r := newRig(t)
			rep := r.do(admin, "POST", "/updater/apply", c.ct, c.body)
			if rep.status != c.status || problemCode(t, rep) != c.code {
				t.Errorf("%s: %d %s, want %d %s", c.name, rep.status, rep.body, c.status, c.code)
			}
			if r.st.n() != 0 || len(r.upd.prepped) != 0 {
				t.Errorf("%s: a malformed request reached Prepare or the starter", c.name)
			}
		}
	})
}

// TestUpdaterRoutes_UnavailableOnlyAfterAuthorisation: a collaborator that is
// not wired answers 503, but only to a caller the route's checks let through.
func TestUpdaterRoutes_UnavailableOnlyAfterAuthorisation(t *testing.T) {
	for _, c := range uhCases() {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, func(d *Deps) { d.Updater, d.Starter = nil, nil })
			if rep := r.do(nil, c.method, c.path, "", c.body); rep.status != 401 {
				t.Errorf("no principal: %d, want 401 (nothing about availability)", rep.status)
			}
			if rep := r.do(uhAs(auth.MethodSession, "core:users.view"), c.method, c.path, "", c.body); rep.status != 403 {
				t.Errorf("without the scope: %d, want 403", rep.status)
			}
			rep := r.do(uhAs(auth.MethodSession, rbac.CoreAdmin), c.method, c.path, "", c.body)
			if rep.status != 503 || problemCode(t, rep) != "unavailable" {
				t.Errorf("authorised: %d %s, want 503 unavailable", rep.status, rep.body)
			}
		})
	}
}

func uhRegistered(t testing.TB) *httpapi.Router {
	t.Helper()
	r := httpapi.NewRouter(httpapi.Options{}, httpapi.Deps{Log: slog.New(slog.DiscardHandler)})
	Register(r.CoreGroup(), Deps{})
	return r
}

// TestUpdaterRegisterRouteTable pins 05-updater §5U.10 as the router sees it:
// method, path, name, permission and guards, and that nothing but the four
// routes is registered.
func TestUpdaterRegisterRouteTable(t *testing.T) {
	var got []httpapi.RouteInfo
	for ri := range uhRegistered(t).Routes() {
		got = append(got, ri)
	}
	cases := uhCases()
	if len(got) != len(cases) {
		t.Fatalf("%d routes registered, want %d", len(got), len(cases))
	}
	for _, c := range cases {
		i := slices.IndexFunc(got, func(ri httpapi.RouteInfo) bool { return ri.Method == c.method && ri.Pattern == uhBase+c.path })
		if i < 0 {
			t.Errorf("no route %s %s", c.method, uhBase+c.path)
			continue
		}
		ri := got[i]
		if ri.Owner != "core" || ri.Kind != httpapi.RouteKindRaw || ri.Name != c.name {
			t.Errorf("%s = owner %s kind %d name %s, want core raw %s", c.path, ri.Owner, ri.Kind, ri.Name, c.name)
		}
		if ri.Perm.IsZero() || ri.Perm.String() != c.perm {
			t.Errorf("%s requires %s, want %s", c.name, ri.Perm, c.perm)
		}
		if want := (httpapi.Guards{Interactive: c.interactive}); ri.Guards != want {
			t.Errorf("%s guards = %+v, want %+v", c.name, ri.Guards, want)
		}
	}
	// The handler's table and Register's are the one table.
	for _, def := range (&api{}).routeTable() {
		i := slices.IndexFunc(got, func(ri httpapi.RouteInfo) bool { return ri.Name == def.name })
		if i < 0 || got[i].Guards != (httpapi.Guards{Interactive: def.gate.interactive}) || got[i].Perm.String() != string(def.gate.perm) {
			t.Errorf("%s: handler gate %+v differs from the registered route", def.name, def.gate)
		}
	}
}

// TestUpdaterRegisterBuildFailsOnlyForWantOfAuth: until F4-08 a router refuses
// every non-Public native route with ErrNoAuthService; anything else Build
// checks would be a further error here.
func TestUpdaterRegisterBuildFailsOnlyForWantOfAuth(t *testing.T) {
	_, err := uhRegistered(t).Build()
	if err == nil {
		t.Fatal("Build succeeded without an auth service")
	}
	joined, ok := err.(interface{ Unwrap() []error })
	if !ok {
		t.Fatalf("Build error %T does not join its problems", err)
	}
	problems := joined.Unwrap()
	if len(problems) != len(uhCases()) {
		t.Errorf("Build reported %d problems, want %d: %v", len(problems), len(uhCases()), err)
	}
	for _, p := range problems {
		if !errors.Is(p, httpapi.ErrNoAuthService) {
			t.Errorf("Build reported %v, want only ErrNoAuthService", p)
		}
	}
}

// uhHasButNotSatisfied holds the scope but the checker refuses its
// requirement, as core:admin does for a credential that cannot hold a
// Sensitive scope.
type uhHasButNotSatisfied struct{ uhChecker }

func (uhHasButNotSatisfied) Satisfies(rbac.Requirement) bool { return false }

func TestUpdaterRoutes_AuthorisationReadsBothCheckerAnswers(t *testing.T) {
	for _, c := range uhCases() {
		r := newRig(t)
		p := uhAs(auth.MethodSession, rbac.CoreAdmin)
		p.Perms = uhHasButNotSatisfied{uhChecker{rbac.CoreAdmin}}
		if rep := r.do(p, c.method, c.path, "", c.body); rep.status != 403 {
			t.Errorf("%s with a scope held but its requirement unsatisfied: %d, want 403", c.name, rep.status)
		}
		// The auth-disabled principal has no user to name as the actor: 403
		// interactive_required, whether the route's interactive guard or the
		// no-user rule refuses it (F4-08 ruling G5(a): never the
		// session-ending 401).
		p = uhAs(auth.MethodAuthDisabled, rbac.CoreAdmin)
		p.User = nil
		if rep := r.do(p, c.method, c.path, "", c.body); rep.status != 403 || problemCode(t, rep) != "interactive_required" {
			t.Errorf("%s with no user: %d %s, want 403 interactive_required", c.name, rep.status, rep.body)
		}
		if r.st.n() != 0 {
			t.Errorf("%s: a refused request enqueued a task", c.name)
		}
	}
}

// TestUpdaterRoutes_ApplyNeedsBothCollaborators: apply asks the updater before
// it enqueues, so either missing is a 503 and nothing is enqueued.
func TestUpdaterRoutes_ApplyNeedsBothCollaborators(t *testing.T) {
	r := newRig(t, func(d *Deps) { d.Updater = nil })
	if rep := r.do(uhAs(auth.MethodSession, rbac.CoreAdmin), "POST", "/updater/apply", "", ""); rep.status != 503 {
		t.Errorf("no updater: %d, want 503", rep.status)
	}
	r = newRig(t, func(d *Deps) { d.Starter = nil })
	if rep := r.do(uhAs(auth.MethodSession, rbac.CoreAdmin), "POST", "/updater/apply", "", ""); rep.status != 503 {
		t.Errorf("no starter: %d, want 503", rep.status)
	}
}

// TestUpdaterTaskStartGatesMatchTheRoutes: each route that starts a task
// carries a gate the generic task API cannot state, so the task's definition
// must not be startable there. A route that needs an interactive session maps
// to a kind with Perms.OwnerRouteOnly set; the check route has no guard beyond
// its permission, and its kind stays startable through the task API under that
// same permission. The routes' permissions equal the definitions' Start scopes.
func TestUpdaterTaskStartGatesMatchTheRoutes(t *testing.T) {
	kindOf := map[string]tasks.Definition{
		"checkUpdater":   updater.CheckTask(nil),
		"applyUpdate":    updater.ApplyTask(nil),
		"rollbackUpdate": updater.RollbackTask(nil),
	}
	seen := 0
	for _, def := range (&api{}).routeTable() {
		d, starts := kindOf[def.name]
		if !starts {
			continue
		}
		seen++
		perms := tasks.CommonOf(d).Perms
		if got, want := perms.Start.String(), rbac.Need(def.gate.perm).String(); got != want {
			t.Errorf("%s: route needs %s but the task's Start is %s", def.name, want, got)
		}
		if ownerOnly := perms.OwnerRouteOnly != ""; ownerOnly != def.gate.interactive {
			t.Errorf("%s: route interactive = %v but the task's OwnerRouteOnly is set = %v; the generic task API would start it without the route's gate",
				def.name, def.gate.interactive, ownerOnly)
		}
	}
	if seen != len(kindOf) {
		t.Fatalf("matched %d of %d task-starting routes", seen, len(kindOf))
	}
}
