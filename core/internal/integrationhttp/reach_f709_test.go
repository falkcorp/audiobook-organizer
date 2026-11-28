// file: core/internal/integrationhttp/reach_f709_test.go
// version: 1.0.0
// guid: e6b3116b-15ee-4fe2-a295-f11c6148b0f7
// last-edited: 2026-11-28

package integrationhttp

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/internal/authz"
	"github.com/falkcorp/mediacore/rbac"
)

// F7-09 W1/W4 and round 2: the reach cells. Each cell serves one request to a
// real route through the router's own stages 12 and 13 (Router.SetAuth) as a
// principal whose checker is the one rbac's compiler builds, so a refusal is the
// router's and the answer carries the scope it names (the first scope the caller
// lacks, in the requirement's order).
//
// The instance routes ask rbac.OperatorRequirement for every kind: the seven
// core scopes of HEAD's settings.manage atom. HEAD's integrations.manage atom
// maps to core:integrations.manage and books:integrations.manage only, and a
// holder of just that never reached instance CRUD or settings at HEAD (PUT
// /config needs settings.manage). The health and test routes mount the floor
// core:integrations.manage and answer a per-kind rule in the handler
// (probeReach); docs/plans/platform-core/f7-09-integrations-per-kind-reach.md is
// the table every cell here pins a row of.
//
// The atom rows are the same ones internal/books/books_test.go:199-202 pins for
// the cutover (the atom table the principals below are built from); a change
// to an atom there must change reachSettingsAtom and reachIntegrationsAtom here.

// reachCaller is who a cell calls as.
type reachCaller struct {
	name   string
	scopes []rbac.Permission
	// key makes the caller an API key: the scopes are the key's Scopes (the roles
	// grant the same), Method is auth.MethodAPIKey and the credential is not
	// interactive.
	key bool
	// authOff makes the caller the auth-disabled principal (no user, no scopes
	// named).
	authOff bool
}

var (
	// reachIntegrationsAtom is HEAD's integrations.manage atom after the
	// cutover: core:integrations.manage plus books:integrations.manage.
	reachIntegrationsAtom = reachCaller{name: "integrations.manage atom only",
		scopes: []rbac.Permission{"core:integrations.manage", "books:integrations.manage"}}
	// reachSettingsAtom is HEAD's settings.manage atom: its eight scopes
	// (seven core, one books), as internal/books/atoms.go lists them.
	reachSettingsAtom = reachCaller{name: "settings.manage atom (all eight scopes)",
		scopes: []rbac.Permission{"core:settings.manage", "books:settings.manage", "core:tasks.manage", "core:system.view",
			"core:system.manage", "core:library.manage", "core:library.browse", "core:integrations.manage"}}
	// reachSettingsScopeOnly holds one scope of the requirement alone.
	reachSettingsScopeOnly = reachCaller{name: "core:settings.manage alone", scopes: []rbac.Permission{"core:settings.manage"}}
	// reachNativeTwoScopes is a native role holding exactly the two scopes the
	// round-1 requirement asked, core:integrations.manage and core:settings.manage:
	// HEAD has no role of that shape, and it is refused now.
	reachNativeTwoScopes = reachCaller{name: "native role {core:integrations.manage, core:settings.manage}",
		scopes: []rbac.Permission{"core:integrations.manage", "core:settings.manage"}}
	// reachSettingsKey is the settings.manage atom held by an API key: the case
	// S4d proves non-secret writes for.
	reachSettingsKey = reachCaller{name: "settings.manage atom as an API key", key: true, scopes: reachSettingsAtom.scopes}
	// reachIntegrationsKey is the integrations.manage atom held by an API key.
	reachIntegrationsKey = reachCaller{name: "integrations.manage atom as an API key", key: true, scopes: reachIntegrationsAtom.scopes}
	// reachAdminOnly holds core:admin alone: it implies every scope (05-rbac 4.3)
	// for an interactive session.
	reachAdminOnly = reachCaller{name: "core:admin alone", scopes: []rbac.Permission{rbac.CoreAdmin}}
	// reachAuthOff is the auth-disabled principal.
	reachAuthOff = reachCaller{name: "auth disabled", authOff: true}
	// reachReadOnly holds core:integrations.read alone.
	reachReadOnly = reachCaller{name: "core:integrations.read alone", scopes: []rbac.Permission{permRead}}
)

// reachCatalog is core's scopes plus the books scopes the atoms above name.
func reachCatalog(t testing.TB) *rbac.Catalog {
	t.Helper()
	specs := rbac.CoreScopes()
	for _, n := range []string{"integrations.manage", "settings.manage"} {
		specs = append(specs, rbac.PermissionSpec{Name: rbac.Permission("books:" + n), Title: n, Description: "books " + n})
	}
	cat, err := rbac.NewCatalog(specs...)
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

func (c reachCaller) principal(cat *rbac.Catalog) *auth.Principal {
	if c.authOff {
		return &auth.Principal{Method: auth.MethodAuthDisabled, CredentialID: "cred-gotham", Perms: authz.AuthDisabled(cat)}
	}
	in := rbac.CompileInput{RolePatterns: slices.Clone(c.scopes)}
	method := auth.MethodSession
	in.Interactive = true
	if c.key {
		method = auth.MethodAPIKey
		in.Interactive = false
		in.Scopes = slices.Clone(c.scopes)
	}
	return &auth.Principal{
		User: &auth.User{ID: "01GOTHAMBRUCEWAYNE0000000", Username: "bruce.wayne"}, Method: method,
		CredentialID: "cred-gotham",
		Perms:        authz.Compile(in, cat),
	}
}

// reachStage12 binds one principal, as stage 12 does for a valid credential.
type reachStage12 struct{ p *auth.Principal }

func (s reachStage12) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), s.p)))
		})
	}
}

func (reachStage12) SessionCookie(auth.ClientInfo, string, time.Time) *http.Cookie {
	return &http.Cookie{Name: "hello_session", Path: "/", MaxAge: -1}
}

// reachNoScope is a signed-in session that holds core:self and nothing else.
var reachNoScope = reachCaller{name: "no scope (core:self)", scopes: []rbac.Permission{"core:self"}}

// reachRefusal is a caller turned away and how: at stage 13, whose 403 names the
// first scope the caller lacks in required_permission, or, when backstop is set,
// by the handler (api.guardPerm), whose 403 carries no required_permission and
// whose Detail names the requirement.
type reachRefusal struct {
	caller   reachCaller
	names    rbac.Permission
	backstop bool
}

// reachRoute is one changed route and the request that reaches its handler's
// success. The instance gcpd-roof (version 1) is stored before every cell.
type reachRoute struct {
	name, method, path string
	body               func() any
	status             int // the answer to a principal that passes stage 13 and the backstop
	allowed            []reachCaller
	refused            []reachRefusal
	// want is the requirement the handler's backstop asks, which the Detail of its
	// 403 names ("this needs " + want.String()).
	want rbac.Requirement
	// intact checks the refused request left the store as it was.
	intact func(t *testing.T, e *env)
}

const reachRoof = "/integrations/notify/instances/" + roofID

var reachRoofRef = integrations.Ref{Kind: integrations.KindNotify, Instance: roofID}

// roofIntact is the default: the stored instance is still there.
func roofIntact(t *testing.T, e *env) {
	t.Helper()
	if _, ok := e.reg.Config(reachRoofRef); !ok {
		t.Error("the instance is gone: the handler acted before the permission check")
	}
}

// createIntact: a refused create left no gcpd-annex behind, and the roof stands.
func createIntact(t *testing.T, e *env) {
	t.Helper()
	roofIntact(t, e)
	if _, ok := e.reg.Config(integrations.Ref{Kind: integrations.KindNotify, Instance: "gcpd-annex"}); ok {
		t.Error("gcpd-annex exists: a refused create was applied")
	}
}

// updateIntact: a refused update left the roof at version 1 and URL 192.0.2.20.
func updateIntact(t *testing.T, e *env) {
	t.Helper()
	cfg, ok := e.reg.Config(reachRoofRef)
	if !ok {
		t.Error("the instance is gone: the handler acted before the permission check")
		return
	}
	if cfg.Version != 1 {
		t.Errorf("version = %d, want 1: a refused update was applied", cfg.Version)
	}
	if got := cfg.Settings["url"]; got != "http://192.0.2.20/" {
		t.Errorf("url = %v, want http://192.0.2.20/: a refused update was applied", got)
	}
}

func reachRoutes() []reachRoute {
	// Every instance route is refused at stage 13, naming the first scope of the
	// requirement the caller lacks: core:settings.manage for a caller that holds
	// none of them or only the integrations atom, core:tasks.manage once the
	// first scope is held.
	settings := rbac.Permission("core:settings.manage")
	tasks := rbac.Permission("core:tasks.manage")
	instanceRefused := []reachRefusal{
		{caller: reachIntegrationsAtom, names: settings},
		{caller: reachIntegrationsKey, names: settings},
		{caller: reachSettingsScopeOnly, names: tasks},
		{caller: reachNativeTwoScopes, names: tasks},
		{caller: reachReadOnly, names: settings},
		{caller: reachNoScope, names: settings},
	}
	// Allowed: the settings atom as a session and as an API key (HEAD let a
	// settings-atom key write non-secret config, S4d), core:admin alone, and the
	// auth-disabled principal (HEAD admits it on every route: verdicts.json "authoff").
	instanceAllowed := []reachCaller{reachSettingsAtom, reachSettingsKey, reachAdminOnly, reachAuthOff}
	// Health and test: below the floor the router refuses (naming the floor);
	// at the floor the backstop does, for the per-kind requirement.
	floor := integrations.ManagePerm
	probeRefused := []reachRefusal{
		{caller: reachReadOnly, names: floor},
		{caller: reachSettingsScopeOnly, names: floor},
		{caller: reachNoScope, names: floor},
		{caller: reachIntegrationsAtom, backstop: true},
		{caller: reachIntegrationsKey, backstop: true},
		{caller: reachNativeTwoScopes, backstop: true},
	}
	want := instancePerm()
	return []reachRoute{
		{name: "listIntegrationInstances", method: http.MethodGet, path: "/integrations/notify/instances", status: http.StatusOK,
			refused: instanceRefused, allowed: instanceAllowed, want: want, intact: roofIntact},
		{name: "getIntegrationInstance", method: http.MethodGet, path: reachRoof, status: http.StatusOK,
			refused: instanceRefused, allowed: instanceAllowed, want: want, intact: roofIntact},
		{name: "createIntegrationInstance", method: http.MethodPost, path: "/integrations/notify/instances", status: http.StatusCreated,
			body:    func() any { return signalBody("gcpd-annex", "http://192.0.2.21/", "") },
			refused: instanceRefused, allowed: instanceAllowed, want: want, intact: createIntact},
		{name: "updateIntegrationInstance", method: http.MethodPut, path: reachRoof, status: http.StatusOK,
			body: func() any {
				b := signalBody("", "http://192.0.2.22/", "")
				b.Version = 1
				return replaceOf(b)
			},
			refused: instanceRefused, allowed: instanceAllowed, want: want, intact: updateIntact},
		{name: "deleteIntegrationInstance", method: http.MethodDelete, path: reachRoof, status: http.StatusNoContent,
			refused: instanceRefused, allowed: instanceAllowed, want: want, intact: roofIntact},
		{name: "releaseIntegrationInstanceHold", method: http.MethodDelete, path: reachRoof + "/hold", status: http.StatusNoContent,
			refused: instanceRefused, allowed: instanceAllowed, want: want, intact: roofIntact},
		// Health of a notify instance (HEAD GET /plugins/:id/health, settings.manage):
		// the integrations atom, which HEAD never gave a notify route, is refused
		// by the backstop; the settings atom reaches it.
		{name: "getIntegrationInstanceHealth", method: http.MethodGet, path: reachRoof + "/health", status: http.StatusOK,
			refused: probeRefused, allowed: instanceAllowed, want: want, intact: roofIntact},
		// Test of a notify instance and of an unsaved notify body: HEAD has no
		// webhook test route, so the integrations atom gains none and the
		// settings atom's reach equals its notify config writes.
		{name: "testIntegrationInstance", method: http.MethodPost, path: reachRoof + "/test", status: http.StatusOK,
			body:    func() any { return signalBody("", "http://192.0.2.20/", "") },
			refused: probeRefused, allowed: instanceAllowed, want: want, intact: roofIntact},
		{name: "testIntegration", method: http.MethodPost, path: "/integrations/notify/test", status: http.StatusOK,
			body:    func() any { return signalBody("", "http://192.0.2.20/", "alfred-1") },
			refused: probeRefused, allowed: instanceAllowed, want: want, intact: roofIntact},
	}
}

// reachServe serves one request to the registered routes as c over a fresh env
// holding the instance gcpd-roof.
func reachServe(t *testing.T, rt reachRoute, c reachCaller) *httptest.ResponseRecorder {
	t.Helper()
	rec, _ := reachServeWith(t, rt, c, nil)
	return rec
}

// reachRouter is the router the cells serve through: stages 12 and 13 for c,
// the routes mounted by mountFn (nil is Register) over e.
func reachRouter(t *testing.T, e *env, c reachCaller, mountFn func(*httpapi.Group, *api)) http.Handler {
	t.Helper()
	cat := reachCatalog(t)
	r := httpapi.NewRouter(httpapi.Options{OpenAPI: httpapi.OpenAPIOptions{Version: "1.0.0", CookieName: "hello_session"}},
		httpapi.Deps{Log: slog.New(slog.DiscardHandler)})
	if err := r.SetAuth(httpapi.AuthWiring{
		Service:     reachStage12{p: c.principal(cat)},
		ResolveAtom: func(string, bool) (rbac.Requirement, bool) { return rbac.Requirement{}, false },
		Catalog:     cat,
	}); err != nil {
		t.Fatal(err)
	}
	if mountFn == nil {
		Register(r.CoreGroup(), e.reg, e.holds, e.log)
	} else {
		mountFn(r.CoreGroup(), &api{reg: e.reg, holds: e.holds, audit: e.log, base: r.CoreGroup().Base()})
	}
	h, err := r.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return h
}

// reachDo serves one request to h.
func reachDo(t *testing.T, h http.Handler, method, path string, body func() any) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		if raw, err = json.Marshal(body()); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, "/api/core/v1"+path, bytes.NewReader(raw))
	req.RemoteAddr = "192.0.2.10:4000"
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// reachServeWith is reachServe with the routes mounted by mountFn instead of
// Register (nil is Register), and the env returned so a cell can look at what
// the request left behind.
func reachServeWith(t *testing.T, rt reachRoute, c reachCaller, mountFn func(*httpapi.Group, *api)) (*httptest.ResponseRecorder, *env) {
	t.Helper()
	e := newEnv(t)
	e.create(t, adminCtx(), kindNotify, signalBody(roofID, "http://192.0.2.20/", "alfred-1"))
	h := reachRouter(t, e, c, mountFn)
	return reachDo(t, h, rt.method, rt.path, rt.body), e
}

// problemOf decodes a refusal's problem document.
func problemOf(t *testing.T, rec *httptest.ResponseRecorder) httpapi.Problem {
	t.Helper()
	var p httpapi.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("problem %q: %v", rec.Body.String(), err)
	}
	return p
}

// TestReach_CallersHoldTheScopesTheyAreNamedFor guards the fixture: a catalog
// that lacks a scope compiles to a checker that silently grants nothing, which
// would turn every "allowed" cell below into a refusal and every refusal into a
// pass.
func TestReach_CallersHoldTheScopesTheyAreNamedFor(t *testing.T) {
	cat := reachCatalog(t)
	for _, c := range []reachCaller{reachIntegrationsAtom, reachSettingsAtom, reachSettingsScopeOnly, reachNativeTwoScopes,
		reachSettingsKey, reachIntegrationsKey, reachReadOnly, reachNoScope} {
		p := c.principal(cat)
		for _, s := range c.scopes {
			if !p.Perms.Has(s) {
				t.Errorf("%s does not hold %s", c.name, s)
			}
		}
		if p.Interactive() == c.key {
			t.Errorf("%s: Interactive() = %v, want %v", c.name, p.Interactive(), !c.key)
		}
	}
	for _, c := range []reachCaller{reachIntegrationsAtom, reachIntegrationsKey, reachSettingsScopeOnly, reachNativeTwoScopes} {
		if p := c.principal(cat); p.Perms.Satisfies(instancePerm()) {
			t.Errorf("%s satisfies the instance requirement: it must not", c.name)
		}
	}
	for _, c := range []reachCaller{reachSettingsAtom, reachSettingsKey, reachAdminOnly} {
		if p := c.principal(cat); !p.Perms.Satisfies(instancePerm()) {
			t.Errorf("%s does not satisfy the instance requirement", c.name)
		}
	}
	if got := len(reachSettingsAtom.scopes); got != 8 {
		t.Errorf("the settings.manage atom fixture has %d scopes, want 8", got)
	}
	// The seven core scopes of the atom are exactly OperatorRequirement's.
	core := slices.DeleteFunc(slices.Clone(reachSettingsAtom.scopes), func(s rbac.Permission) bool { return s == "books:settings.manage" })
	if !slices.Equal(slices.Sorted(slices.Values(core)), slices.Sorted(slices.Values(operatorScopes()))) {
		t.Errorf("the settings atom's core scopes %v differ from OperatorRequirement's %v", core, operatorScopes())
	}
}

// TestReach_InstanceAndProbeRoutes is the per-route reach table for a notify
// instance: every changed route, and callers of every shape (the atoms as a
// session and as an API key, a native role, core:admin alone, the auth-disabled
// principal). Each refusal is checked for who wrote it: the router names a scope
// in required_permission, the backstop names the requirement in Detail. The
// instance routes are refused at the router; health and test are refused at
// the router below the floor and by the backstop at it (the integrations atom
// there, which HEAD never gave a notify route).
func TestReach_InstanceAndProbeRoutes(t *testing.T) {
	for _, rt := range reachRoutes() {
		t.Run(rt.name, func(t *testing.T) {
			for _, c := range rt.allowed {
				t.Run("allowed/"+c.name, func(t *testing.T) {
					rec := reachServe(t, rt, c)
					if rec.Code != rt.status {
						t.Errorf("%s %s as %s = %d %s, want %d", rt.method, rt.path, c.name, rec.Code, rec.Body.String(), rt.status)
					}
				})
			}
			for _, f := range rt.refused {
				t.Run("refused/"+f.caller.name, func(t *testing.T) {
					rec, e := reachServeWith(t, rt, f.caller, nil)
					if rec.Code != http.StatusForbidden {
						t.Fatalf("%s %s as %s = %d %s, want 403", rt.method, rt.path, f.caller.name, rec.Code, rec.Body.String())
					}
					p := problemOf(t, rec)
					if f.backstop {
						wantBackstop(t, p, rt.want)
					} else if p.Code != "permission_denied" || p.Required != string(f.names) {
						t.Errorf("problem = code %q required %q, want the router's permission_denied naming %s", p.Code, p.Required, f.names)
					}
					rt.intact(t, e)
				})
			}
		})
	}
}

// wantBackstop fails t unless p is the handler's backstop refusal for want: code
// permission_denied, no required_permission (that member is the router's) and
// Detail "this needs <want>" (N2: a refusal that fell back to forbidden(nil),
// whose text names core:integrations.manage and Local, fails here).
func wantBackstop(t *testing.T, p httpapi.Problem, want rbac.Requirement) {
	t.Helper()
	if p.Code != "permission_denied" || p.Required != "" {
		t.Errorf("problem = code %q required %q, want the handler's permission_denied (the router names required_permission)", p.Code, p.Required)
	}
	if wantDetail := "this needs " + want.String(); p.Detail != wantDetail {
		t.Errorf("detail = %q, want %q", p.Detail, wantDetail)
	}
}

// TestReach_RouteTableAsksWhatTheCellsAssume ties the table above to the
// registered requirements, so a cell that drifts from the routes fails by name
// instead of passing for the wrong reason.
func TestReach_RouteTableAsksWhatTheCellsAssume(t *testing.T) {
	operator := rbac.OperatorRequirement()
	floor := rbac.Need(integrations.ManagePerm)
	want := map[string]rbac.Requirement{
		"listIntegrationInstances":       operator,
		"getIntegrationInstance":         operator,
		"createIntegrationInstance":      operator,
		"updateIntegrationInstance":      operator,
		"deleteIntegrationInstance":      operator,
		"releaseIntegrationInstanceHold": operator,
		// Health and the two test routes mount the floor; the per-kind rule is the
		// handler's (probeReach), pinned by the per-kind cells.
		"getIntegrationInstanceHealth": floor,
		"testIntegration":              floor,
		"testIntegrationInstance":      floor,
		// Unchanged: the adapters list stays on read.
		"listIntegrationAdapters": rbac.Need(permRead),
	}
	seen := 0
	for ri := range registered(t).Routes() {
		w, ok := want[ri.Name]
		if !ok {
			t.Errorf("route %s is not in the reach table", ri.Name)
			continue
		}
		seen++
		if ri.Perm.String() != w.String() || ri.Perm.Kind() != w.Kind() {
			t.Errorf("%s requires %s (kind %d), want %s (kind %d)", ri.Name, ri.Perm, ri.Perm.Kind(), w, w.Kind())
		}
	}
	if seen != len(want) {
		t.Errorf("%d of %d routes seen", seen, len(want))
	}
	// The instance requirement is the one shared definition, not a copy.
	if got, want := instancePerm().String(), rbac.OperatorRequirement().String(); got != want {
		t.Errorf("instancePerm = %s, want rbac.OperatorRequirement %s", got, want)
	}
}

// TestReach_BackstopHoldsWhenTheRouteIsMountedWeaker mounts every changed route
// with a deliberately weaker Perm (rbac.AnyAuthenticated: a signed-in caller
// passes stage 13) and serves each caller the real rule refuses. The handler's
// own backstop (api.guardPerm, with the requirement its route documents:
// instancePerm, or probeReach(kind).req for health and test) must still turn
// that caller away, as a 403 the handler writes (no required_permission: that
// member is the router's) whose Detail names the requirement, before the
// instance is read or changed. It is what stops a mounting mistake from
// widening access, and it is what the route-only cells above cannot see.
func TestReach_BackstopHoldsWhenTheRouteIsMountedWeaker(t *testing.T) {
	weak := func(g *httpapi.Group, a *api) {
		mount(g, a, routePerms{read: rbac.AnyAuthenticated, instance: rbac.AnyAuthenticated, floor: rbac.AnyAuthenticated})
	}
	for _, rt := range reachRoutes() {
		t.Run(rt.name, func(t *testing.T) {
			for _, f := range rt.refused {
				t.Run(f.caller.name, func(t *testing.T) {
					rec, e := reachServeWith(t, rt, f.caller, weak)
					if rec.Code != http.StatusForbidden {
						t.Fatalf("%s %s as %s = %d %s, want the backstop's 403", rt.method, rt.path, f.caller.name, rec.Code, rec.Body.String())
					}
					wantBackstop(t, problemOf(t, rec), rt.want)
					rt.intact(t, e)
				})
			}
		})
	}
}
