// file: core/internal/authhttp/routes_test.go
// version: 1.0.1
// guid: 3a7e19c5-d204-4f8b-86a1-0b5c7d92e418
// last-edited: 2026-11-11

package authhttp

import (
	"errors"
	"net/http"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
)

const headBase = "/api/v1/auth/oauth"

// wantRoute is one row of 05-authn §4.13.
type wantRoute struct {
	method, pattern string
	perm            rbac.Requirement
	interactive     bool
	rate            httpapi.RateClass
}

const core = "/api/core/v1"

// table is §4.13 row by row (plus GET /users/invites, which the table does not
// list and seams.d/F4-05.md records).
var table = []wantRoute{
	{"GET", core + "/auth/status", rbac.Public, false, httpapi.RateDefault},
	{"POST", core + "/auth/setup", rbac.Public, false, httpapi.RateAuth},
	{"POST", core + "/auth/bootstrap", rbac.Public, false, httpapi.RateAuth},
	{"POST", core + "/auth/login", rbac.Public, false, httpapi.RateAuth},
	{"POST", core + "/auth/logout", rbac.AnyAuthenticated, false, httpapi.RateAuth},
	{"GET", core + "/auth/providers", rbac.Public, false, httpapi.RateAuth},
	{"GET", core + "/auth/oidc/{provider}/start", rbac.Public, false, httpapi.RateAuth},
	{"GET", headBase + "/{provider}/start", rbac.Public, false, httpapi.RateAuth},
	{"GET", headBase + "/{provider}/callback", rbac.Public, false, httpapi.RateAuth},
	{"GET", core + "/me", rbac.Need(scopeSelf), false, httpapi.RateDefault},
	{"PATCH", core + "/me", rbac.Need(scopeSelf), true, httpapi.RateDefault},
	{"PUT", core + "/me/password", rbac.Need(scopeSelf), true, httpapi.RateDefault},
	{"GET", core + "/me/sessions", rbac.Need(scopeSelf), false, httpapi.RateDefault},
	{"DELETE", core + "/me/sessions/{publicID}", rbac.Need(scopeSelf), true, httpapi.RateDefault},
	{"GET", core + "/me/api-keys", rbac.Need(scopeSelf), false, httpapi.RateDefault},
	{"POST", core + "/me/api-keys", rbac.Need(scopeSelf), false, httpapi.RateDefault},
	{"PATCH", core + "/me/api-keys/{id}", rbac.Need(scopeSelf), false, httpapi.RateDefault},
	{"DELETE", core + "/me/api-keys/{id}", rbac.Need(scopeSelf), false, httpapi.RateDefault},
	{"GET", core + "/api-keys", rbac.Need(scopeUsersManage), false, httpapi.RateDefault},
	{"GET", core + "/api-keys/{id}", rbac.Need(scopeSelf), false, httpapi.RateDefault},
	{"PATCH", core + "/api-keys/{id}", rbac.Need(scopeSelf), false, httpapi.RateDefault},
	{"DELETE", core + "/api-keys/{id}", rbac.Need(scopeSelf), false, httpapi.RateDefault},
	{"GET", core + "/users", rbac.Need(scopeUsersView), false, httpapi.RateDefault},
	{"GET", core + "/users/{id}", rbac.Need(scopeUsersView), false, httpapi.RateDefault},
	{"GET", core + "/users/invites", rbac.Need(scopeUsersManage), false, httpapi.RateDefault},
	{"POST", core + "/users/invites", rbac.Need(scopeUsersManage), true, httpapi.RateDefault},
	{"DELETE", core + "/users/invites/{id}", rbac.Need(scopeUsersManage), true, httpapi.RateDefault},
	{"POST", core + "/users/{id}/disable", rbac.Need(scopeUsersManage), true, httpapi.RateDefault},
	{"POST", core + "/users/{id}/enable", rbac.Need(scopeUsersManage), true, httpapi.RateDefault},
	{"DELETE", core + "/users/{id}", rbac.Need(scopeUsersManage), true, httpapi.RateDefault},
	{"POST", core + "/users/{id}/reset-password", rbac.Need(scopeUsersManage), true, httpapi.RateDefault},
	{"PUT", core + "/users/{id}/roles", rbac.Need(scopeUsersManage), true, httpapi.RateDefault},
	{"GET", core + "/users/{id}/sessions", rbac.Need(scopeUsersManage), false, httpapi.RateDefault},
	{"DELETE", core + "/users/{id}/sessions/{publicID}", rbac.Need(scopeUsersManage), true, httpapi.RateDefault},
	{"GET", core + "/users/{id}/api-keys", rbac.Need(scopeUsersManage), false, httpapi.RateDefault},
	{"POST", core + "/users/{id}/api-keys", rbac.Need(scopeUsersManage), true, httpapi.RateDefault},
	{"POST", core + "/users/{id}/temp-login", rbac.Need(scopeUsersManage), true, httpapi.RateDefault},
	{"POST", core + "/auth/invites/lookup", rbac.Public, false, httpapi.RateAuth},
	{"POST", core + "/auth/invites/accept", rbac.Public, false, httpapi.RateAuth},
	{"POST", core + "/auth/temp-login/consume", rbac.Public, false, httpapi.RateAuth},
}

// registered is the routes Register adds to a fresh router.
func registered(t testing.TB, e *env, base string) (*httpapi.Router, []httpapi.RouteInfo) {
	t.Helper()
	r := httpapi.NewRouter(httpapi.Options{}, httpapi.Deps{})
	d := FromService(e.svc, e.rb)
	d.OIDCCallbackBase = base
	if err := Register(r, d); err != nil {
		t.Fatalf("Register: %v", err)
	}
	var infos []httpapi.RouteInfo
	for ri := range r.Routes() {
		infos = append(infos, ri)
	}
	return r, infos
}

// Every row of 05-authn §4.13 is registered with its permission, its
// Interactive guard and its rate class, and nothing else is. Guards.Owner is
// on no identity route.
func TestRouteTable_MatchesSection413(t *testing.T) {
	_, infos := registered(t, newEnv(t), headBase)
	if len(infos) != len(table) {
		t.Errorf("%d routes registered, the table has %d", len(infos), len(table))
	}
	got := map[string]httpapi.RouteInfo{}
	for _, ri := range infos {
		got[ri.Method+" "+ri.Pattern] = ri
		if ri.Owner != "core" {
			t.Errorf("%s %s owned by %q, want core", ri.Method, ri.Pattern, ri.Owner)
		}
		if ri.Guards.Owner {
			t.Errorf("%s %s carries the Owner guard: no identity route is owner-only", ri.Method, ri.Pattern)
		}
	}
	for _, w := range table {
		ri, ok := got[w.method+" "+w.pattern]
		if !ok {
			t.Errorf("missing %s %s", w.method, w.pattern)
			continue
		}
		if ri.Perm.String() != w.perm.String() {
			t.Errorf("%s %s Perm %s, want %s", w.method, w.pattern, ri.Perm, w.perm)
		}
		if ri.Guards.Interactive != w.interactive {
			t.Errorf("%s %s Interactive %v, want %v", w.method, w.pattern, ri.Guards.Interactive, w.interactive)
		}
		if ri.RateClass != w.rate {
			t.Errorf("%s %s RateClass %v, want %v", w.method, w.pattern, ri.RateClass, w.rate)
		}
	}
}

// With no callback base (hello) the callback is core's own, and HEAD's two
// paths are not registered.
func TestRouteTable_NativeCallbackWithoutBase(t *testing.T) {
	_, infos := registered(t, newEnv(t), "")
	var patterns []string
	for _, ri := range infos {
		patterns = append(patterns, ri.Method+" "+ri.Pattern)
	}
	if !slices.Contains(patterns, "GET "+core+"/auth/oidc/{provider}/callback") {
		t.Errorf("no native callback: %v", patterns)
	}
	for _, p := range patterns {
		if strings.Contains(p, headBase) {
			t.Errorf("HEAD's path %s registered with no base", p)
		}
	}
	if len(patterns) != len(table)-1 {
		t.Errorf("%d routes, want %d (the table minus HEAD's two paths plus the native callback)", len(patterns), len(table)-1)
	}
}

// A one-time token never travels in a path (05-authn §4.13, SEC-21).
func TestRoutes_NoTokenInAPath(t *testing.T) {
	credential := regexp.MustCompile(`(?i)\{[^}]*(token|secret|key)[^}]*\}`)
	_, infos := registered(t, newEnv(t), headBase)
	for _, ri := range infos {
		if credential.MatchString(strings.ReplaceAll(ri.Pattern, "{id}", "")) {
			t.Errorf("%s %s has a credential-shaped path value", ri.Method, ri.Pattern)
		}
	}
}

// Every route that changes state is Interactive, or is on this list with its
// reason: it establishes a credential (the caller has none yet), is a Public
// call whose one-time token is the proof, or is a key route the Service gates
// by key ownership and scope containment (05-authn §4.4, §4.13).
func TestRoutes_EveryStateChangeIsInteractiveOrAccountedFor(t *testing.T) {
	exempt := map[string]string{
		"POST " + core + "/auth/setup":                   "creates the first admin",
		"POST " + core + "/auth/bootstrap":               "redeems the bootstrap token",
		"POST " + core + "/auth/login":                   "establishes a session",
		"POST " + core + "/auth/logout":                  "ends the caller's own session",
		"POST " + core + "/auth/invites/lookup":          "a read despite POST: the token is in the body",
		"POST " + core + "/auth/invites/accept":          "a one-time token is the proof",
		"POST " + core + "/auth/temp-login/consume":      "a one-time token is the proof",
		"POST " + core + "/me/api-keys":                  "a key mints only for itself, within its own scopes (Service)",
		"PATCH " + core + "/me/api-keys/{id}":            "a key manages only keys it may create (Service); re-enable is interactive (Service)",
		"DELETE " + core + "/me/api-keys/{id}":           "a restriction widens nothing",
		"PATCH " + core + "/api-keys/{id}":               "as /me/api-keys/{id}; another user's key needs an interactive manager (Service)",
		"DELETE " + core + "/api-keys/{id}":              "as /me/api-keys/{id}",
		"GET " + headBase + "/{provider}/callback":       "a GET that opens a session",
		"GET " + core + "/auth/oidc/{provider}/start":    "a GET that redirects",
		"GET " + headBase + "/{provider}/start":          "a GET that redirects",
		"GET " + core + "/auth/oidc/{provider}/callback": "a GET that opens a session",
	}
	_, infos := registered(t, newEnv(t), headBase)
	for _, ri := range infos {
		key := ri.Method + " " + ri.Pattern
		if ri.Method == http.MethodGet && exempt[key] == "" {
			continue
		}
		if ri.Guards.Interactive {
			continue
		}
		if exempt[key] == "" {
			t.Errorf("%s changes state and is not Interactive", key)
		}
	}
}

// Before F4-08 mounts stage 13, Build refuses every non-Public native route
// and nothing else: no pattern conflict, no bad schema, no ErrOutsideClaim.
func TestRegister_BuildFailsOnlyForWantOfAuth(t *testing.T) {
	r, infos := registered(t, newEnv(t), headBase)
	_, err := r.Build()
	if err == nil {
		t.Fatal("Build succeeded with non-Public routes and no auth service")
	}
	var parts []error
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		parts = j.Unwrap()
	} else {
		parts = []error{err}
	}
	nonPublic := 0
	for _, ri := range infos {
		if ri.Perm.Kind() != rbac.KindPublic {
			nonPublic++
		}
	}
	if len(parts) != nonPublic {
		t.Errorf("Build returned %d errors, want one per non-Public route (%d): %v", len(parts), nonPublic, err)
	}
	for _, p := range parts {
		if !errors.Is(p, httpapi.ErrNoAuthService) {
			t.Errorf("Build error is not ErrNoAuthService: %v", p)
		}
	}
}

// The Public routes, the two on the fixed group included, build and serve
// through the real chain.
func TestRegister_PublicSubsetBuildsAndServes(t *testing.T) {
	e := newEnv(t)
	a := e.api(func(d *Deps) { d.OIDCCallbackBase = headBase })
	h := publicHandler(t, a, httpapi.LimitOptions{})
	w := serve(h, "GET", core+"/auth/status", nil)
	if w.Code != 200 || !contains(w.Body.String(), `"setup_required":true`) {
		t.Fatalf("status: %d %s", w.Code, w.Body.String())
	}
	w = serve(h, "GET", core+"/auth/providers", nil)
	if w.Code != 200 || !contains(w.Body.String(), `"providers":[]`) {
		t.Fatalf("providers: %d %s", w.Code, w.Body.String())
	}
	// No Flow: both start paths and the fixed callback answer as a provider
	// that does not exist.
	for _, p := range []string{core + "/auth/oidc/penguin/start", headBase + "/penguin/start", headBase + "/penguin/callback"} {
		wantProblem(t, serve(h, "GET", p, nil), 404, "not_found")
	}
	// A route outside the core claim and the fixed pattern is not served.
	if w := serve(h, "GET", "/api/v1/auth/oauth/penguin/other", nil); w.Code != 404 {
		t.Fatalf("an unregistered path under the base answered %d", w.Code)
	}
}

func TestRegister_RefusesMissingDependencies(t *testing.T) {
	e := newEnv(t)
	r := httpapi.NewRouter(httpapi.Options{}, httpapi.Deps{})
	if err := Register(nil, FromService(e.svc, e.rb)); !errors.Is(err, errNoRouter) {
		t.Fatalf("nil router: %v", err)
	}
	d := FromService(e.svc, e.rb)
	d.Dominance = nil
	d.Self = nil
	err := Register(r, d)
	if !errors.Is(err, errNoDep) || !contains(err.Error(), "Deps.Dominance, Deps.Self") {
		t.Fatalf("missing dependencies: %v", err)
	}
	for range r.Routes() {
		t.Fatal("a route was registered although a dependency was missing")
	}
}

// Every request and response type uses snake_case JSON names (05-http-api
// §3.1): a camelCase tag would be a Build error on the route that used it.
func TestDTOs_SnakeCaseJSON(t *testing.T) {
	snake := regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)
	for _, v := range []any{
		Status{}, User{}, Me{}, LoginRequest{}, SessionOpened{}, SetupRequest{}, BootstrapRequest{}, BootstrapResult{},
		Provider{}, Providers{}, InviteLookup{}, InviteAccept{}, InvitePreview{}, TempLoginConsume{}, ProfilePatch{},
		PasswordChange{}, Session{}, APIKey{}, KeyCreate{}, KeyCreated{}, KeyStatus{}, Invite{}, InviteCreate{},
		InviteCreated{}, LinkCreated{}, RolesReplace{},
	} {
		rt := reflect.TypeOf(v)
		for f := range rt.Fields() {
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "" || !snake.MatchString(name) {
				t.Errorf("%s.%s json tag %q is not snake_case", rt.Name(), f.Name, f.Tag.Get("json"))
			}
		}
	}
}

// No response type has a field that could carry a stored secret, and the
// password change has no target-user field.
func TestDTOs_NoSecretFieldsAndNoTargetOnPasswordChange(t *testing.T) {
	banned := regexp.MustCompile(`(?i)hash|salt|secret|mask|epoch`)
	for _, v := range []any{User{}, Me{}, Session{}, APIKey{}, Invite{}, InvitePreview{}, Providers{}, Provider{}, SessionOpened{}} {
		rt := reflect.TypeOf(v)
		for field := range rt.Fields() {
			if banned.MatchString(field.Name) {
				t.Errorf("%s.%s names a stored secret", rt.Name(), field.Name)
			}
		}
	}
	pc := reflect.TypeFor[PasswordChange]()
	var names []string
	for field := range pc.Fields() {
		names = append(names, field.Tag.Get("json"))
	}
	if !slices.Equal(names, []string{"current", "new"}) {
		t.Fatalf("PasswordChange fields %v: PUT /me/password is self-only, {current, new}", names)
	}
}

// TestModuleGroup_IsAuthenticated: the Public routes are exactly the sign-in
// surface (status, setup, bootstrap, login, the provider list, both OIDC starts
// and callbacks, the invite and temp-login token calls); every other route
// needs a credential, and each of those not Public requires one by Perm.
func TestModuleGroup_IsAuthenticated(t *testing.T) {
	_, infos := registered(t, newEnv(t), headBase)
	var public []string
	for _, ri := range infos {
		if ri.Perm.Kind() == rbac.KindPublic {
			public = append(public, ri.Method+" "+ri.Pattern)
		}
	}
	slices.Sort(public)
	want := []string{
		"GET " + core + "/auth/oidc/{provider}/start", "GET " + core + "/auth/providers", "GET " + core + "/auth/status",
		"GET " + headBase + "/{provider}/callback", "GET " + headBase + "/{provider}/start",
		"POST " + core + "/auth/bootstrap", "POST " + core + "/auth/invites/accept", "POST " + core + "/auth/invites/lookup",
		"POST " + core + "/auth/login", "POST " + core + "/auth/setup", "POST " + core + "/auth/temp-login/consume",
	}
	slices.Sort(want)
	if !slices.Equal(public, want) {
		t.Fatalf("public routes %v, want %v", public, want)
	}
}
