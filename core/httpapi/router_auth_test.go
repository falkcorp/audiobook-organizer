// file: core/httpapi/router_auth_test.go
// version: 0.1.0
// guid: 3d4a95c2-1b7e-4f60-9a8d-6c2e0b715f43
// last-edited: 2026-11-21

package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// These tests build a real Router, call SetAuth, Build, and serve requests
// through the handler Build returns, so a wiring that is not in the chain (or
// a relaxation without the wiring) fails here rather than passing a unit test
// of Stage13 alone.

// raFakeAuthn is stage 12 for the router tests: it binds the principal the
// X-Test-As header names and counts how often it ran, so a route that must
// not meet stage 12 can be told from one that does.
type raFakeAuthn struct {
	cat   *rbac.Catalog
	calls atomic.Int64
}

func (f *raFakeAuthn) principal(name string) *auth.Principal {
	switch name {
	case "viewer":
		return s13Session(f.cat, "core:users.view")
	case "nobody":
		return s13Session(f.cat, "core:self")
	case "key":
		return s13Key(f.cat, []rbac.Permission{"core:users.view"}, "core:users.view")
	case "admin":
		return s13Session(f.cat, rbac.CoreAdmin)
	case "owner":
		return s13Owner(f.cat, rbac.CoreAdmin)
	}
	return nil
}

func (f *raFakeAuthn) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			f.calls.Add(1)
			if p := f.principal(r.Header.Get("X-Test-As")); p != nil {
				r = r.WithContext(auth.WithPrincipal(r.Context(), p))
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (f *raFakeAuthn) SessionCookie(_ auth.ClientInfo, _ string, _ time.Time) *http.Cookie {
	return &http.Cookie{Name: "hello_session", Path: "/", MaxAge: -1}
}

// raAudit records the events Emit received.
type raAudit struct {
	mu     sync.Mutex
	events []audit.Event
}

func (a *raAudit) Record(context.Context, audit.Event) error { return errors.New("not used") }
func (a *raAudit) RecordIn(context.Context, *storage.Batch, audit.Event) error {
	return errors.New("not used")
}
func (a *raAudit) Emit(_ context.Context, e audit.Event) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, e)
}
func (a *raAudit) all() []audit.Event {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]audit.Event(nil), a.events...)
}

type raOut struct {
	OK bool `json:"ok"`
}

// raRig is a router with one route of each kind stages 12 and 13 treat
// differently.
type raRig struct {
	h       http.Handler
	authn   *raFakeAuthn
	audit   *raAudit
	reached map[string]*atomic.Int64
}

func (rg *raRig) hit(name string) int64 { return rg.reached[name].Load() }

// raBuild registers the rig's routes on a fresh router, wires auth when wire is
// non-nil, and builds.
func raBuild(t *testing.T, wire func(*raFakeAuthn, *raAudit) *AuthWiring) (*raRig, error) {
	t.Helper()
	cat := s13Catalog(t)
	rg := &raRig{authn: &raFakeAuthn{cat: cat}, audit: &raAudit{}, reached: map[string]*atomic.Int64{}}
	counter := func(name string) *atomic.Int64 {
		c := new(atomic.Int64)
		rg.reached[name] = c
		return c
	}
	r := NewRouter(Options{OpenAPI: OpenAPIOptions{Version: "1.0.0", CookieName: "hello_session"}}, Deps{})
	if wire != nil {
		if w := wire(rg.authn, rg.audit); w != nil {
			if err := r.SetAuth(*w); err != nil {
				t.Fatalf("SetAuth: %v", err)
			}
		}
	}
	g := r.CoreGroup()
	ok := func(c *atomic.Int64) func(context.Context, *In[struct{}]) (raOut, error) {
		return func(context.Context, *In[struct{}]) (raOut, error) { c.Add(1); return raOut{OK: true}, nil }
	}
	Handle(g, Route{Method: http.MethodGet, Path: "/widgets", Name: "listWidgets", Perm: rbac.Need("core:users.view")}, ok(counter("native")))
	Handle(g, Route{Method: http.MethodPost, Path: "/widgets", Name: "makeWidget", Perm: rbac.Need("core:users.view"),
		Guards: Guards{Interactive: true}}, ok(counter("interactive")))
	Handle(g, Route{Method: http.MethodPost, Path: "/vault", Name: "openVault", Perm: rbac.Need(rbac.CoreAdmin),
		Guards: Guards{Owner: true}}, ok(counter("owner")))
	Handle(g, Route{Method: http.MethodGet, Path: "/open", Name: "getOpen", Perm: rbac.Public}, ok(counter("public")))
	rawc := counter("raw")
	HandleRaw(g, Route{Method: http.MethodGet, Path: "/raw", Name: "getRaw", Perm: rbac.Need("core:users.view")}, RawDoc{},
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { rawc.Add(1); w.WriteHeader(http.StatusOK) }))
	ic := counter("infra")
	r.Infra("GET /debugz", rbac.Need(rbac.CoreAdmin), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { ic.Add(1); w.WriteHeader(http.StatusOK) }))
	ipc := counter("infra-public")
	r.Infra("GET /livez", rbac.Public, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { ipc.Add(1); w.WriteHeader(http.StatusOK) }))
	h, err := r.Build()
	rg.h = h
	return rg, err
}

func raWire(owner func() bool) func(*raFakeAuthn, *raAudit) *AuthWiring {
	return func(a *raFakeAuthn, au *raAudit) *AuthWiring {
		return &AuthWiring{Service: a, OwnerRequired: owner, ResolveAtom: s13Resolver, Catalog: a.cat, Audit: au}
	}
}

func (rg *raRig) do(method, path, as string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	r.RemoteAddr = "192.0.2.10:4000"
	if as != "" {
		r.Header.Set("X-Test-As", as)
	}
	rec := httptest.NewRecorder()
	rg.h.ServeHTTP(rec, r)
	return rec
}

// TestRouterAuth_SwitchOffRefusesAndMountsNothing: without SetAuth a non-Public
// native route and a LegacyAuthCore mount are still refused at Build, so the
// router never serves a route it cannot authorize.
func TestRouterAuth_SwitchOffRefusesAndMountsNothing(t *testing.T) {
	if _, err := raBuild(t, nil); !errors.Is(err, ErrNoAuthService) {
		t.Fatalf("native non-Public routes with no auth = %v, want ErrNoAuthService", err)
	}
	r := NewRouter(Options{}, Deps{})
	r.Infra("GET /debugz", rbac.Need(rbac.CoreAdmin), http.NotFoundHandler())
	if _, err := r.Build(); !errors.Is(err, ErrNoAuthService) {
		t.Fatalf("non-Public Infra route with no auth = %v, want ErrNoAuthService", err)
	}
	r = NewRouter(Options{}, Deps{})
	if err := r.Claim("gcpd", "/api/v1/"); err != nil {
		t.Fatal(err)
	}
	if err := r.MountLegacy(LegacyMount{Owner: "gcpd", Handler: http.NotFoundHandler(), Auth: LegacyAuthCore,
		Routes: manifestOf(gothamRoute(http.MethodGet, "/api/v1/cases"))}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Build(); !errors.Is(err, ErrNoAuthService) {
		t.Fatalf("LegacyAuthCore mount with no auth = %v, want ErrNoAuthService", err)
	}
	// A Public-only router builds as it always did, and runs no stage 12.
	r = NewRouter(Options{}, Deps{})
	called := false
	Handle(r.CoreGroup(), Route{Method: http.MethodGet, Path: "/open", Name: "getOpen", Perm: rbac.Public},
		func(context.Context, *In[struct{}]) (raOut, error) { called = true; return raOut{OK: true}, nil })
	h, err := r.Build()
	if err != nil {
		t.Fatalf("Public-only router: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/core/v1/open", nil))
	if rec.Code != http.StatusOK || !called {
		t.Fatalf("Public route: status %d called %v", rec.Code, called)
	}
}

// TestRouterAuth_Refusals serves each kind of route through the built chain.
func TestRouterAuth_Refusals(t *testing.T) {
	rg, err := raBuild(t, raWire(func() bool { return true }))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	const base = "/api/core/v1"
	for _, c := range []struct {
		name         string
		method, path string
		as           string
		status       int
		code         string
		reached      string // counter that must have moved when admitted
	}{
		{"native, no principal", "GET", base + "/widgets", "", 401, "authentication_required", ""},
		{"native, wrong scope", "GET", base + "/widgets", "nobody", 403, "permission_denied", ""},
		{"native, admitted", "GET", base + "/widgets", "viewer", 200, "", "native"},
		{"interactive route, key", "POST", base + "/widgets", "key", 403, "interactive_required", ""},
		{"interactive route, session", "POST", base + "/widgets", "viewer", 200, "", "interactive"},
		{"owner route, admin session, owner required", "POST", base + "/vault", "admin", 403, "owner_required", ""},
		{"owner route, owner", "POST", base + "/vault", "owner", 200, "", "owner"},
		{"public, anonymous", "GET", base + "/open", "", 200, "", "public"},
		{"raw, no principal", "GET", base + "/raw", "", 401, "authentication_required", ""},
		{"raw, wrong scope", "GET", base + "/raw", "nobody", 403, "permission_denied", ""},
		{"raw, admitted", "GET", base + "/raw", "viewer", 200, "", "raw"},
		{"infra non-Public, no principal", "GET", "/debugz", "", 401, "authentication_required", ""},
		{"infra non-Public, wrong scope", "GET", "/debugz", "viewer", 403, "permission_denied", ""},
		{"infra non-Public, admin", "GET", "/debugz", "admin", 200, "", "infra"},
		{"infra Public", "GET", "/livez", "", 200, "", "infra-public"},
	} {
		t.Run(c.name, func(t *testing.T) {
			before := map[string]int64{}
			for n := range rg.reached {
				before[n] = rg.hit(n)
			}
			rec := rg.do(c.method, c.path, c.as)
			if rec.Code != c.status {
				t.Fatalf("status %d, want %d (body %s)", rec.Code, c.status, rec.Body)
			}
			if c.code != "" {
				if got := s13Problem(t, rec).Code; got != c.code {
					t.Fatalf("code %q, want %q", got, c.code)
				}
			}
			for n := range rg.reached {
				moved := rg.hit(n) != before[n]
				if moved != (n == c.reached) {
					t.Errorf("handler %q moved=%v, want %v: a refused request reached its handler, or an admitted one did not", n, moved, n == c.reached)
				}
			}
		})
	}
}

// TestRouterAuth_Stage12SkipsPublicInfra: stage 12 runs on every native and raw
// route and on a non-Public Infra route, and not on a Public one.
func TestRouterAuth_Stage12SkipsPublicInfra(t *testing.T) {
	rg, err := raBuild(t, raWire(func() bool { return true }))
	if err != nil {
		t.Fatal(err)
	}
	rg.do("GET", "/livez", "")
	if n := rg.authn.calls.Load(); n != 0 {
		t.Fatalf("stage 12 ran %d times on a Public Infra route, want 0", n)
	}
	rg.do("GET", "/api/core/v1/open", "")
	if n := rg.authn.calls.Load(); n != 1 {
		t.Fatalf("stage 12 ran %d times after a Public native route, want 1 (an invalid credential is still a 401 there)", n)
	}
	rg.do("GET", "/debugz", "admin")
	if n := rg.authn.calls.Load(); n != 2 {
		t.Fatalf("stage 12 ran %d times after a non-Public Infra route, want 2", n)
	}
}

// TestRouterAuth_OwnerRequiredFalse: with core.auth.owner.required false the
// Owner guard becomes interactive plus core:admin, never anyone.
func TestRouterAuth_OwnerRequiredFalse(t *testing.T) {
	rg, err := raBuild(t, raWire(func() bool { return false }))
	if err != nil {
		t.Fatal(err)
	}
	if rec := rg.do("POST", "/api/core/v1/vault", "admin"); rec.Code != http.StatusOK {
		t.Fatalf("interactive admin with owner.required=false: %d %s", rec.Code, rec.Body)
	}
	if rec := rg.do("POST", "/api/core/v1/vault", "key"); rec.Code != http.StatusForbidden || s13Problem(t, rec).Code != "interactive_required" {
		t.Fatalf("key with owner.required=false: %d %s", rec.Code, rec.Body)
	}
	if rec := rg.do("POST", "/api/core/v1/vault", "viewer"); rec.Code != http.StatusForbidden || s13Problem(t, rec).Code != "permission_denied" {
		t.Fatalf("non-admin with owner.required=false: %d %s", rec.Code, rec.Body)
	}
	// nil is the refusing side.
	rg, err = raBuild(t, raWire(nil))
	if err != nil {
		t.Fatal(err)
	}
	if rec := rg.do("POST", "/api/core/v1/vault", "admin"); rec.Code != http.StatusForbidden || s13Problem(t, rec).Code != "owner_required" {
		t.Fatalf("nil OwnerRequired: %d %s", rec.Code, rec.Body)
	}
}

// TestRouterAuth_AuditsEach403: a 403 stage 13 writes is recorded as
// core.permission.deny with the route, the missing scope and the reason; a 401
// is not recorded.
func TestRouterAuth_AuditsEach403(t *testing.T) {
	rg, err := raBuild(t, raWire(func() bool { return true }))
	if err != nil {
		t.Fatal(err)
	}
	rg.do("GET", "/api/core/v1/widgets", "")
	if n := len(rg.audit.all()); n != 0 {
		t.Fatalf("a 401 recorded %d audit events, want 0", n)
	}
	rg.do("GET", "/api/core/v1/widgets", "nobody")
	rg.do("POST", "/api/core/v1/widgets", "key")
	ev := rg.audit.all()
	if len(ev) != 2 {
		t.Fatalf("recorded %d events, want 2: %+v", len(ev), ev)
	}
	want := []audit.Event{
		{Action: "core.permission.deny", Outcome: audit.Denied, Permission: "core:users.view", Route: "GET /api/core/v1/widgets", Reason: "permission_denied"},
		{Action: "core.permission.deny", Outcome: audit.Denied, Route: "POST /api/core/v1/widgets", Reason: "interactive_required"},
	}
	for i := range want {
		if ev[i].Action != want[i].Action || ev[i].Outcome != want[i].Outcome || ev[i].Permission != want[i].Permission ||
			ev[i].Route != want[i].Route || ev[i].Reason != want[i].Reason {
			t.Errorf("event %d = %+v, want %+v", i, ev[i], want[i])
		}
	}
}

// TestRouterAuth_Legacy: a LegacyAuthCore mount's manifest routes meet stages
// 12 and 13, an OwnAuth route meets neither, and a LegacyAuthGin mount's routes
// meet neither.
func TestRouterAuth_Legacy(t *testing.T) {
	build := func(t *testing.T, mode LegacyAuth) (http.Handler, *raFakeAuthn) {
		t.Helper()
		a := &raFakeAuthn{cat: s13Catalog(t)}
		r := NewRouter(Options{}, Deps{})
		if err := r.SetAuth(AuthWiring{Service: a, OwnerRequired: func() bool { return true }, ResolveAtom: s13Resolver, Catalog: a.cat}); err != nil {
			t.Fatal(err)
		}
		if err := r.Claim("gcpd", "/api/v1/"); err != nil {
			t.Fatal(err)
		}
		r.AllowAuthMethods("gcpd", "gcpd.signal")
		routes := []LegacyRoute{
			{Method: http.MethodGet, GinPath: "/api/v1/cases", Perm: rbac.LegacyAtom("library.edit_metadata")},
			{Method: http.MethodGet, GinPath: "/api/v1/status", Perm: rbac.Public, Public: true},
		}
		if mode == LegacyAuthCore {
			routes = append(routes, LegacyRoute{Method: http.MethodGet, GinPath: "/api/v1/signal", Perm: rbac.Delegated("gcpd.signal"),
				OwnAuth: true, AuthMethod: "gcpd.signal"})
		}
		ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
		if err := r.MountLegacy(LegacyMount{Owner: "gcpd", Handler: ok, Auth: mode, Routes: manifestOf(routes...)}); err != nil {
			t.Fatal(err)
		}
		h, err := r.Build()
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		return h, a
	}
	serve := func(h http.Handler, path, as string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.RemoteAddr = "192.0.2.10:4000"
		if as != "" {
			r.Header.Set("X-Test-As", as)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	t.Run("LegacyAuthCore", func(t *testing.T) {
		h, a := build(t, LegacyAuthCore)
		if rec := serve(h, "/api/v1/cases", ""); rec.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous: %d %s", rec.Code, rec.Body)
		}
		if rec := serve(h, "/api/v1/cases", "viewer"); rec.Code != http.StatusForbidden {
			t.Fatalf("wrong scope for the atom: %d %s", rec.Code, rec.Body)
		}
		if rec := serve(h, "/api/v1/cases", "admin"); rec.Code != http.StatusOK {
			t.Fatalf("core:admin holds every scope an atom resolves to: %d %s", rec.Code, rec.Body)
		}
		if rec := serve(h, "/api/v1/status", ""); rec.Code != http.StatusOK {
			t.Fatalf("Public legacy route: %d %s", rec.Code, rec.Body)
		}
		before := a.calls.Load()
		if rec := serve(h, "/api/v1/signal", ""); rec.Code != http.StatusOK {
			t.Fatalf("OwnAuth route is its handler's to authenticate: %d %s", rec.Code, rec.Body)
		}
		if a.calls.Load() != before {
			t.Fatal("stage 12 ran on an OwnAuth route")
		}
	})
	t.Run("LegacyAuthGin", func(t *testing.T) {
		h, a := build(t, LegacyAuthGin)
		if rec := serve(h, "/api/v1/cases", ""); rec.Code != http.StatusOK {
			t.Fatalf("a LegacyAuthGin route authenticates in gin, not here: %d %s", rec.Code, rec.Body)
		}
		if a.calls.Load() != 0 {
			t.Fatal("stage 12 ran on a LegacyAuthGin route")
		}
	})
}

// TestRouterAuth_SetAuthRefusals: SetAuth refuses a nil Service, a second call
// and a call after Build, and changes nothing.
func TestRouterAuth_SetAuthRefusals(t *testing.T) {
	a := &raFakeAuthn{cat: s13Catalog(t)}
	r := NewRouter(Options{}, Deps{})
	if err := r.SetAuth(AuthWiring{}); !errors.Is(err, ErrAuthWiring) {
		t.Fatalf("nil Service = %v, want ErrAuthWiring", err)
	}
	if err := r.SetAuth(AuthWiring{Service: a}); err != nil {
		t.Fatal(err)
	}
	if err := r.SetAuth(AuthWiring{Service: a}); !errors.Is(err, ErrAuthWiring) {
		t.Fatalf("second SetAuth = %v, want ErrAuthWiring", err)
	}
	r = NewRouter(Options{}, Deps{})
	if _, err := r.Build(); err != nil {
		t.Fatal(err)
	}
	if err := r.SetAuth(AuthWiring{Service: a}); !errors.Is(err, errBuilt) {
		t.Fatalf("SetAuth after Build = %v, want errBuilt", err)
	}
}
