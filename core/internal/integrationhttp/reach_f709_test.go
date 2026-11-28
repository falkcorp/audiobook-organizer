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

// F7-09 W1/W4: the reach cells. Each cell serves one request to a real route
// through the router's own stages 12 and 13 (Router.SetAuth) as a principal
// whose checker is the one rbac's compiler builds, so a refusal is the
// router's and the answer carries the scope it names.
//
// The instance routes ask AllOf(core:integrations.manage, core:settings.manage)
// for every kind. HEAD's integrations.manage atom maps to core:integrations.manage
// and books:integrations.manage only, and a holder of just that never reached
// instance CRUD or settings at HEAD (PUT /config needs settings.manage); the
// settings.manage atom maps to the eight scopes in reachSettingsAtom, which
// include both core scopes of the requirement.

// reachCaller is who a cell calls as.
type reachCaller struct {
	name   string
	scopes []rbac.Permission
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
	// reachSettingsScopeOnly holds the other half of the requirement alone.
	reachSettingsScopeOnly = reachCaller{name: "core:settings.manage alone", scopes: []rbac.Permission{"core:settings.manage"}}
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
	return &auth.Principal{
		User: &auth.User{ID: "01GOTHAMBRUCEWAYNE0000000", Username: "bruce.wayne"}, Method: auth.MethodSession,
		CredentialID: "cred-gotham",
		Perms:        authz.Compile(rbac.CompileInput{Interactive: true, RolePatterns: slices.Clone(c.scopes)}, cat),
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

// reachRefusal is a caller turned away at stage 13 and the scope the 403 names.
type reachRefusal struct {
	caller reachCaller
	names  rbac.Permission
}

// reachRoute is one changed route and the request that reaches its handler's
// success. The instance gcpd-roof (version 1) is stored before every cell.
type reachRoute struct {
	name, method, path string
	body               func() any
	status             int // the answer to a principal that passes stage 13
	allowed            []reachCaller
	refused            []reachRefusal
}

const reachRoof = "/integrations/notify/instances/" + roofID

func reachRoutes() []reachRoute {
	// Every instance route: the integrations atom lacks core:settings.manage,
	// and each of the other callers lacks core:integrations.manage.
	instanceRefused := []reachRefusal{
		{reachIntegrationsAtom, permSettingsManage},
		{reachSettingsScopeOnly, integrations.ManagePerm},
		{reachReadOnly, integrations.ManagePerm},
		{reachNoScope, integrations.ManagePerm},
	}
	instanceAllowed := []reachCaller{reachSettingsAtom}
	return []reachRoute{
		{name: "listIntegrationInstances", method: http.MethodGet, path: "/integrations/notify/instances", status: http.StatusOK,
			refused: instanceRefused, allowed: instanceAllowed},
		{name: "getIntegrationInstance", method: http.MethodGet, path: reachRoof, status: http.StatusOK,
			refused: instanceRefused, allowed: instanceAllowed},
		{name: "createIntegrationInstance", method: http.MethodPost, path: "/integrations/notify/instances", status: http.StatusCreated,
			body:    func() any { return signalBody("gcpd-annex", "http://192.0.2.21/", "") },
			refused: instanceRefused, allowed: instanceAllowed},
		{name: "updateIntegrationInstance", method: http.MethodPut, path: reachRoof, status: http.StatusOK,
			body: func() any {
				b := signalBody("", "http://192.0.2.20/", "")
				b.Version = 1
				return replaceOf(b)
			},
			refused: instanceRefused, allowed: instanceAllowed},
		{name: "deleteIntegrationInstance", method: http.MethodDelete, path: reachRoof, status: http.StatusNoContent,
			refused: instanceRefused, allowed: instanceAllowed},
		{name: "releaseIntegrationInstanceHold", method: http.MethodDelete, path: reachRoof + "/hold", status: http.StatusNoContent,
			refused: instanceRefused, allowed: instanceAllowed},
		// W4: health asks core:integrations.manage alone. A read-only holder is
		// refused (it was allowed while the route asked read); the integrations
		// atom's holder and the settings atom's holder both reach it.
		{name: "getIntegrationInstanceHealth", method: http.MethodGet, path: reachRoof + "/health", status: http.StatusOK,
			refused: []reachRefusal{
				{reachReadOnly, integrations.ManagePerm},
				{reachSettingsScopeOnly, integrations.ManagePerm},
				{reachNoScope, integrations.ManagePerm},
			},
			allowed: []reachCaller{reachIntegrationsAtom, reachSettingsAtom}},
	}
}

// reachServe serves one request to the registered routes as c over a fresh env
// holding the instance gcpd-roof.
func reachServe(t *testing.T, rt reachRoute, c reachCaller) *httptest.ResponseRecorder {
	t.Helper()
	rec, _ := reachServeWith(t, rt, c, nil)
	return rec
}

// reachServeWith is reachServe with the routes mounted by mountFn instead of
// Register (nil is Register), and the env returned so a cell can look at what
// the request left behind.
func reachServeWith(t *testing.T, rt reachRoute, c reachCaller, mountFn func(*httpapi.Group, *api)) (*httptest.ResponseRecorder, *env) {
	t.Helper()
	e := newEnv(t)
	e.create(t, adminCtx(), kindNotify, signalBody(roofID, "http://192.0.2.20/", "alfred-1"))
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
	var raw []byte
	if rt.body != nil {
		var err error
		if raw, err = json.Marshal(rt.body()); err != nil {
			t.Fatal(err)
		}
	}
	body := bytes.NewReader(raw)
	req := httptest.NewRequest(rt.method, "/api/core/v1"+rt.path, body)
	req.RemoteAddr = "192.0.2.10:4000"
	if rt.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec, e
}

// TestReach_CallersHoldTheScopesTheyAreNamedFor guards the fixture: a catalog
// that lacks a scope compiles to a checker that silently grants nothing, which
// would turn every "allowed" cell below into a refusal and every refusal into a
// pass.
func TestReach_CallersHoldTheScopesTheyAreNamedFor(t *testing.T) {
	cat := reachCatalog(t)
	for _, c := range []reachCaller{reachIntegrationsAtom, reachSettingsAtom, reachSettingsScopeOnly, reachReadOnly, reachNoScope} {
		p := c.principal(cat)
		for _, s := range c.scopes {
			if !p.Perms.Has(s) {
				t.Errorf("%s does not hold %s", c.name, s)
			}
		}
	}
	if p := reachIntegrationsAtom.principal(cat); p.Perms.Has(permSettingsManage) {
		t.Errorf("%s holds %s: it must not", reachIntegrationsAtom.name, permSettingsManage)
	}
	if got := len(reachSettingsAtom.scopes); got != 8 {
		t.Errorf("the settings.manage atom fixture has %d scopes, want 8", got)
	}
}

// TestReach_InstanceAndHealthRoutes is the per-route reach table: every changed
// route, three principals and more. A principal holding only the integrations
// atom is refused on every instance route (naming core:settings.manage), the
// settings atom's holder reaches every route, and the health route needs
// core:integrations.manage.
func TestReach_InstanceAndHealthRoutes(t *testing.T) {
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
					rec := reachServe(t, rt, f.caller)
					if rec.Code != http.StatusForbidden {
						t.Fatalf("%s %s as %s = %d %s, want 403", rt.method, rt.path, f.caller.name, rec.Code, rec.Body.String())
					}
					var p httpapi.Problem
					if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
						t.Fatalf("problem %q: %v", rec.Body.String(), err)
					}
					if p.Code != "permission_denied" || p.Required != string(f.names) {
						t.Errorf("problem = code %q required %q, want permission_denied naming %s", p.Code, p.Required, f.names)
					}
				})
			}
		})
	}
}

// TestReach_RouteTableAsksWhatTheCellsAssume ties the table above to the
// registered requirements, so a cell that drifts from the routes fails by name
// instead of passing for the wrong reason.
func TestReach_RouteTableAsksWhatTheCellsAssume(t *testing.T) {
	both := rbac.AllOf(integrations.ManagePerm, permSettingsManage)
	want := map[string]rbac.Requirement{
		"listIntegrationInstances":       both,
		"getIntegrationInstance":         both,
		"createIntegrationInstance":      both,
		"updateIntegrationInstance":      both,
		"deleteIntegrationInstance":      both,
		"releaseIntegrationInstanceHold": both,
		"getIntegrationInstanceHealth":   rbac.Need(integrations.ManagePerm),
		// Unchanged by F7-09 W1/W4: the adapters list stays on read and the two
		// test routes on manage.
		"listIntegrationAdapters": rbac.Need(permRead),
		"testIntegration":         rbac.Need(integrations.ManagePerm),
		"testIntegrationInstance": rbac.Need(integrations.ManagePerm),
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
}

// TestReach_BackstopHoldsWhenTheRouteIsMountedWeaker mounts every changed route
// with a deliberately weaker Perm (rbac.AnyAuthenticated: a signed-in caller
// passes stage 13) and serves each caller the real Perm refuses. The handler's
// own backstop (api.guardPerm, with the same instancePerm/managePerm value
// Register hands the route) must still turn that caller away, as a 403 the
// handler writes (no required_permission: that member is the router's), before
// the instance is read or changed. It is what stops a mounting mistake from
// widening access, and it is what the route-only cells above cannot see.
func TestReach_BackstopHoldsWhenTheRouteIsMountedWeaker(t *testing.T) {
	weak := func(g *httpapi.Group, a *api) {
		mount(g, a, routePerms{read: rbac.AnyAuthenticated, instance: rbac.AnyAuthenticated, manage: rbac.AnyAuthenticated})
	}
	for _, rt := range reachRoutes() {
		t.Run(rt.name, func(t *testing.T) {
			for _, f := range rt.refused {
				t.Run(f.caller.name, func(t *testing.T) {
					rec, e := reachServeWith(t, rt, f.caller, weak)
					if rec.Code != http.StatusForbidden {
						t.Fatalf("%s %s as %s = %d %s, want the backstop's 403", rt.method, rt.path, f.caller.name, rec.Code, rec.Body.String())
					}
					var p httpapi.Problem
					if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
						t.Fatalf("problem %q: %v", rec.Body.String(), err)
					}
					if p.Code != "permission_denied" || p.Required != "" {
						t.Errorf("problem = code %q required %q, want the handler's permission_denied (the router names required_permission)", p.Code, p.Required)
					}
					if _, ok := e.reg.Config(integrations.Ref{Kind: integrations.KindNotify, Instance: roofID}); !ok {
						t.Error("the instance is gone: the handler acted before the backstop")
					}
				})
			}
		})
	}
}
