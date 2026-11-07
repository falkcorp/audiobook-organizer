// file: core/httpapi/route_test.go
// version: 1.0.2
// guid: cb942937-3f29-482d-8361-0af61a978c2c
// last-edited: 2026-11-07

package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/rbac"
)

// Gadget is the fixture resource of module "batcave".
type Gadget struct {
	Name  string `json:"name"`
	Owner string `json:"owner"`
}

type getGadgetReq struct {
	ID string `path:"id"`
}

type createGadgetReq struct {
	Body Gadget `body:"json"`
}

// newRouter returns a router with module batcave's group, the route names in
// public allowed to use rbac.Public (what the kernel hands over from the
// manifest's PublicRoutes).
func newRouter(t *testing.T, public ...string) (*Router, *Group) {
	t.Helper()
	r := NewRouter(Options{OpenAPI: OpenAPIOptions{Version: "1.0.0", CookieName: "hello_session"}},
		Deps{Log: slog.New(slog.DiscardHandler)})
	r.AllowPublic("batcave", public...)
	return r, r.ModuleGroup("batcave")
}

func mustBuild(t *testing.T, r *Router) http.Handler {
	t.Helper()
	h, err := r.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return h
}

func getGadget(_ context.Context, in *In[getGadgetReq]) (Gadget, error) {
	if in.Req.ID != "batarang" {
		return Gadget{}, NotFound("gadget", in.Req.ID)
	}
	return Gadget{Name: "batarang", Owner: "Bruce Wayne"}, nil
}

// noop is a handler for routes whose request binds nothing.
func noop(context.Context, *In[struct{}]) (Gadget, error) { return Gadget{}, nil }

func TestHandle_RejectsZeroPermission(t *testing.T) {
	r, g := newRouter(t, "getGadget")
	Handle(g, Route{Method: http.MethodGet, Path: "/gadgets/{id}", Name: "getGadget"}, getGadget)
	r.Infra("GET /livez", rbac.Requirement{}, http.NotFoundHandler())
	_, err := r.Build()
	if !errors.Is(err, ErrNoPermission) {
		t.Fatalf("Build error = %v, want ErrNoPermission", err)
	}
	// Both the native route and the infra route are named: R6 has no exempt kind.
	for _, want := range []string{"/api/batcave/v1/gadgets/{id}", "/livez"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Build error does not name %s: %v", want, err)
		}
	}
}

func TestHandle_RejectsForeignScope(t *testing.T) {
	cases := []struct {
		name    string
		perm    rbac.Requirement
		foreign bool
	}{
		{"other module", rbac.Need("joker:read"), true},
		{"any of with one foreign", rbac.AnyOf("batcave:read", "arkham:read"), true},
		{"delegated is legacy-only", rbac.Delegated("gin decides"), true},
		{"legacy atom is legacy-only", rbac.LegacyAtom("download"), true},
		{"own namespace", rbac.Need("batcave:read"), false},
		{"core admin", rbac.Need(rbac.CoreAdmin), false},
		{"any authenticated", rbac.AnyAuthenticated, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, g := newRouter(t)
			Handle(g, Route{Method: http.MethodGet, Path: "/gadgets/{id}", Name: "getGadget", Perm: tc.perm}, getGadget)
			_, err := r.Build()
			if got := errors.Is(err, ErrForeignScope); got != tc.foreign {
				t.Fatalf("errors.Is(ErrForeignScope) = %v, want %v (err: %v)", got, tc.foreign, err)
			}
			// No auth service exists at R1, so every non-Public route also fails
			// closed with ErrNoAuthService (05-http-api §3).
			if !errors.Is(err, ErrNoAuthService) {
				t.Fatalf("Build error = %v, want ErrNoAuthService too", err)
			}
		})
	}
}

func TestHandle_PublicNeedsManifestEntry(t *testing.T) {
	r, g := newRouter(t) // getGadget not in PublicRoutes
	Handle(g, Route{Method: http.MethodGet, Path: "/gadgets/{id}", Name: "getGadget", Perm: rbac.Public}, getGadget)
	if _, err := r.Build(); !errors.Is(err, ErrPublicNotAllowed) {
		t.Fatalf("Build error = %v, want ErrPublicNotAllowed", err)
	}
	r, g = newRouter(t, "getGadget")
	Handle(g, Route{Method: http.MethodGet, Path: "/gadgets/{id}", Name: "getGadget", Perm: rbac.Public}, getGadget)
	mustBuild(t, r)
}

func TestBuild_PatternConflictIsError(t *testing.T) {
	r, g := newRouter(t, "byOwner", "byName")
	// Both match /api/batcave/v1/x/x and neither is more specific: ServeMux
	// panics on the second registration; Build must return an error instead.
	Handle(g, Route{Method: http.MethodGet, Path: "/{owner}/x", Name: "byOwner", Perm: rbac.Public}, noop)
	Handle(g, Route{Method: http.MethodGet, Path: "/x/{id}", Name: "byName", Perm: rbac.Public}, noop)
	_, err := r.Build()
	if !errors.Is(err, ErrPatternConflict) {
		t.Fatalf("Build error = %v, want ErrPatternConflict", err)
	}
}

func TestBuild_DuplicateRoute(t *testing.T) {
	r, g := newRouter(t, "getGadget", "fetchGadget")
	Handle(g, Route{Method: http.MethodGet, Path: "/gadgets/{id}", Name: "getGadget", Perm: rbac.Public}, getGadget)
	Handle(g, Route{Method: http.MethodGet, Path: "/gadgets/{id}", Name: "fetchGadget", Perm: rbac.Public}, getGadget)
	if _, err := r.Build(); !errors.Is(err, ErrDuplicateRoute) {
		t.Fatalf("Build error = %v, want ErrDuplicateRoute", err)
	}
}

// TestClaim_PrefixCollision is the prefix-collision rule (05-http-api §4.9,
// SIMP-22: one rule, no claim kinds). The facade form of it,
// TestBuild_PrefixCollision_FacadeVsFacade, is a deferred-build test of the
// deferred FacadeGroup member (§3.6, §12); this drives the same Claim rule
// a facade will go through.
func TestClaim_PrefixCollision(t *testing.T) {
	r := NewRouter(Options{}, Deps{})
	if err := r.Claim("batcave", "/api/v3/"); err != nil {
		t.Fatal(err)
	}
	if err := r.Claim("arkham", "/api/v3/"); !errors.Is(err, ErrPrefixCollision) {
		t.Fatalf("second /api/v3/ claim = %v, want ErrPrefixCollision", err)
	}
	if _, err := r.Build(); !errors.Is(err, ErrPrefixCollision) {
		t.Fatalf("Build error = %v, want ErrPrefixCollision", err)
	}

	ok := NewRouter(Options{}, Deps{})
	for _, c := range []struct{ owner, prefix string }{{"batcave", "/tv/api/v3/"}, {"arkham", "/movies/api/v3/"}} {
		if err := ok.Claim(c.owner, c.prefix); err != nil {
			t.Fatalf("Claim(%s, %s) = %v", c.owner, c.prefix, err)
		}
	}
	mustBuild(t, ok)

	// A claim inside another owner's module group collides too.
	r = NewRouter(Options{}, Deps{})
	r.ModuleGroup("batcave")
	if err := r.Claim("arkham", "/api/batcave/v1/cells"); !errors.Is(err, ErrPrefixCollision) {
		t.Fatalf("claim inside /api/batcave/ = %v, want ErrPrefixCollision", err)
	}
}

type camelResp struct {
	GadgetName string `json:"gadgetName"`
}

type camelQuery struct {
	SortBy string `query:"sortBy"`
}

type camelPayload struct {
	Inner camelResp `json:"inner"`
}

type camelBody struct {
	Body camelPayload `body:"json"`
}

func TestBuild_RejectsCamelCaseJSON(t *testing.T) {
	cases := map[string]func(g *Group){
		"response property": func(g *Group) {
			Handle(g, Route{Method: http.MethodGet, Path: "/a", Name: "a", Perm: rbac.Public},
				func(context.Context, *In[getGadgetReq]) (camelResp, error) { return camelResp{}, nil })
		},
		"query parameter": func(g *Group) {
			Handle(g, Route{Method: http.MethodGet, Path: "/a", Name: "a", Perm: rbac.Public},
				func(context.Context, *In[camelQuery]) (Gadget, error) { return Gadget{}, nil })
		},
		"nested body property": func(g *Group) {
			Handle(g, Route{Method: http.MethodPost, Path: "/a", Name: "a", Perm: rbac.Public},
				func(context.Context, *In[camelBody]) (Gadget, error) { return Gadget{}, nil })
		},
	}
	for name, register := range cases {
		t.Run(name, func(t *testing.T) {
			r, g := newRouter(t, "a")
			register(g)
			if _, err := r.Build(); !errors.Is(err, ErrJSONName) {
				t.Fatalf("Build error = %v, want ErrJSONName", err)
			}
		})
	}
}

type badQueryType struct {
	Filter map[string]string `query:"filter"`
}

type xmlBody struct {
	Body Gadget `body:"xml"`
}

type wrongPathName struct {
	ID string `path:"gadget"`
}

type twoTags struct {
	ID string `path:"id" query:"id"`
}

func TestBind_TagsCompiledAtRegistration(t *testing.T) {
	called := false
	cases := map[string]func(g *Group){
		"unsupported query type": func(g *Group) {
			Handle(g, Route{Method: http.MethodGet, Path: "/{id}", Name: "a", Perm: rbac.Public},
				func(context.Context, *In[badQueryType]) (Gadget, error) { called = true; return Gadget{}, nil })
		},
		"body other than json": func(g *Group) {
			Handle(g, Route{Method: http.MethodPost, Path: "/{id}", Name: "a", Perm: rbac.Public},
				func(context.Context, *In[xmlBody]) (Gadget, error) { called = true; return Gadget{}, nil })
		},
		"body on GET": func(g *Group) {
			Handle(g, Route{Method: http.MethodGet, Path: "/{id}", Name: "a", Perm: rbac.Public},
				func(context.Context, *In[createGadgetReq]) (Gadget, error) { called = true; return Gadget{}, nil })
		},
		"path tag names no wildcard": func(g *Group) {
			Handle(g, Route{Method: http.MethodGet, Path: "/{id}", Name: "a", Perm: rbac.Public},
				func(context.Context, *In[wrongPathName]) (Gadget, error) { called = true; return Gadget{}, nil })
		},
		"two binding tags": func(g *Group) {
			Handle(g, Route{Method: http.MethodGet, Path: "/{id}", Name: "a", Perm: rbac.Public},
				func(context.Context, *In[twoTags]) (Gadget, error) { called = true; return Gadget{}, nil })
		},
		"request is not a struct": func(g *Group) {
			Handle(g, Route{Method: http.MethodGet, Path: "/{id}", Name: "a", Perm: rbac.Public},
				func(context.Context, *In[string]) (Gadget, error) { called = true; return Gadget{}, nil })
		},
	}
	for name, register := range cases {
		t.Run(name, func(t *testing.T) {
			r, g := newRouter(t, "a")
			register(g)
			// Build is the boot step: no request has been served.
			if _, err := r.Build(); !errors.Is(err, ErrBadBinding) {
				t.Fatalf("Build error = %v, want ErrBadBinding", err)
			}
		})
	}
	if called {
		t.Fatal("a handler ran for a route refused at registration")
	}
}

// TestHandle_UnboundedJSONBodyRefused: MaxBody -1 means a streamed body the
// handler bounds itself; a body:"json" body is decoded into memory, so a
// negative MaxBody on such a route is a boot error, not an unbounded read.
func TestHandle_UnboundedJSONBodyRefused(t *testing.T) {
	for _, maxBody := range []int64{-1, -5} {
		r, g := newRouter(t, "createGadget")
		Handle(g, Route{Method: http.MethodPost, Path: "/gadgets", Name: "createGadget", Perm: rbac.Public, MaxBody: maxBody},
			func(_ context.Context, in *In[createGadgetReq]) (Gadget, error) { return in.Req.Body, nil })
		if _, err := r.Build(); !errors.Is(err, ErrBadBinding) {
			t.Fatalf("MaxBody %d on a body:\"json\" route: Build error = %v, want ErrBadBinding", maxBody, err)
		}
	}
	// A route with no JSON body may stream: MaxBody -1 builds.
	r, g := newRouter(t, "uploadGadget")
	Handle(g, Route{Method: http.MethodPut, Path: "/gadgets/{id}/blueprint", Name: "uploadGadget", Perm: rbac.Public, MaxBody: -1},
		func(context.Context, *In[getGadgetReq]) (NoContent, error) { return NoContent{}, nil })
	mustBuild(t, r)
}

func TestHandle_RouteShape(t *testing.T) {
	cases := map[string]struct {
		rt   Route
		want error
	}{
		"lowercase method":  {Route{Method: "get", Path: "/a", Name: "a", Perm: rbac.Public}, ErrSchema},
		"name not camel":    {Route{Method: http.MethodGet, Path: "/a", Name: "get_a", Perm: rbac.Public}, ErrSchema},
		"dot-dot in path":   {Route{Method: http.MethodGet, Path: "/../arkham", Name: "a", Perm: rbac.Public}, ErrOutsideClaim},
		"empty segment":     {Route{Method: http.MethodGet, Path: "//a", Name: "a", Perm: rbac.Public}, ErrOutsideClaim},
		"path without root": {Route{Method: http.MethodGet, Path: "a", Name: "a", Perm: rbac.Public}, ErrOutsideClaim},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r, g := newRouter(t, "a", "get_a")
			Handle(g, tc.rt, noop)
			if _, err := r.Build(); !errors.Is(err, tc.want) {
				t.Fatalf("Build error = %v, want %v", err, tc.want)
			}
		})
	}
	r, g := newRouter(t, "a")
	Handle(g.Version(1), Route{Method: http.MethodGet, Path: "/a", Name: "a", Perm: rbac.Public}, noop)
	if _, err := r.Build(); !errors.Is(err, ErrOutsideClaim) {
		t.Fatalf("Version(1) Build error = %v, want ErrOutsideClaim", err)
	}
	for _, bad := range []string{"cells", "/cells/", "/../arkham", "/{id}"} {
		r, g = newRouter(t, "a")
		Handle(g.Sub(bad), Route{Method: http.MethodGet, Path: "/a", Name: "a", Perm: rbac.Public}, noop)
		if _, err := r.Build(); !errors.Is(err, ErrOutsideClaim) {
			t.Fatalf("Sub(%q) Build error = %v, want ErrOutsideClaim", bad, err)
		}
	}
	r, g = newRouter(t, "a", "b")
	Handle(g.Sub("/cells"), Route{Method: http.MethodGet, Path: "/a", Name: "a", Perm: rbac.Public}, noop)
	Handle(g.Version(2), Route{Method: http.MethodGet, Path: "/a", Name: "b", Perm: rbac.Public}, noop)
	mustBuild(t, r)
	var patterns []string
	for ri := range r.Routes() {
		patterns = append(patterns, ri.Pattern)
	}
	if strings.Join(patterns, " ") != "/api/batcave/v1/cells/a /api/batcave/v2/a" {
		t.Fatalf("patterns = %v", patterns)
	}
}

func TestHandle_ServesTypedRoute(t *testing.T) {
	r, g := newRouter(t, "getGadget", "createGadget", "deleteGadget")
	Handle(g, Route{Method: http.MethodGet, Path: "/gadgets/{id}", Name: "getGadget", Perm: rbac.Public}, getGadget)
	Handle(g, Route{Method: http.MethodPost, Path: "/gadgets", Name: "createGadget", Perm: rbac.Public},
		func(_ context.Context, in *In[createGadgetReq]) (Created[Gadget], error) {
			return Created[Gadget]{Location: g.Base() + "/gadgets/" + in.Req.Body.Name, Body: in.Req.Body}, nil
		})
	Handle(g, Route{Method: http.MethodDelete, Path: "/gadgets/{id}", Name: "deleteGadget", Perm: rbac.Public},
		func(context.Context, *In[getGadgetReq]) (NoContent, error) { return NoContent{}, nil })
	h := mustBuild(t, r)

	rec := serve(h, http.MethodGet, "/api/batcave/v1/gadgets/batarang", "")
	if rec.Code != http.StatusOK || rec.Body.String() != `{"name":"batarang","owner":"Bruce Wayne"}`+"\n" {
		t.Fatalf("GET = %d %q", rec.Code, rec.Body.String())
	}
	rec = serve(h, http.MethodPost, "/api/batcave/v1/gadgets", `{"name":"grapnel","owner":"Dick Grayson"}`)
	if rec.Code != http.StatusCreated || rec.Header().Get("Location") != "/api/batcave/v1/gadgets/grapnel" {
		t.Fatalf("POST = %d Location %q body %q", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	rec = serve(h, http.MethodDelete, "/api/batcave/v1/gadgets/grapnel", "")
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("DELETE = %d %q", rec.Code, rec.Body.String())
	}
}

// serve runs one request through h; a non-empty body is sent as JSON.
func serve(h http.Handler, method, target, body string) *httptest.ResponseRecorder {
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestHandle_TrailingSlashRefused is the review's M2: ServeMux serves a
// pattern ending in "/" as a subtree and 307s the slashless path to it;
// native patterns are exact (05-http-api §4.8), "/{$}" is the one form.
func TestHandle_TrailingSlashRefused(t *testing.T) {
	for _, p := range []string{"/gadgets/", "/"} {
		r, g := newRouter(t, "listGadgets")
		Handle(g, Route{Method: http.MethodGet, Path: p, Name: "listGadgets", Perm: rbac.Public}, noop)
		if _, err := r.Build(); !errors.Is(err, ErrSchema) || !strings.Contains(err.Error(), "subtree") {
			t.Errorf("path %q: Build = %v, want ErrSchema naming the subtree", p, err)
		}
	}
	r, g := newRouter(t, "listGadgets")
	Handle(g, Route{Method: http.MethodGet, Path: "/gadgets/{$}", Name: "listGadgets", Perm: rbac.Public}, noop)
	h := mustBuild(t, r)
	if rec := serve(h, http.MethodGet, "/api/batcave/v1/gadgets/", ""); rec.Code != http.StatusOK {
		t.Fatalf("GET /gadgets/ = %d %s, want 200", rec.Code, rec.Body.String())
	}
	// "/{$}" is exact: nothing below it reaches the route.
	if rec := serve(h, http.MethodGet, "/api/batcave/v1/gadgets/batarang", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("GET below a /{$} route = %d, want 404", rec.Code)
	}
}

// TestHandle_RateClassChecked is the review's m3: RateExempt is for the infra
// probes and the SPA handler only, and an unknown class is refused.
func TestHandle_RateClassChecked(t *testing.T) {
	for _, rc := range []RateClass{RateExempt, RateExempt + 1} {
		r, g := newRouter(t, "getGadget")
		Handle(g, Route{Method: http.MethodGet, Path: "/gadgets/{id}", Name: "getGadget", Perm: rbac.Public, RateClass: rc}, getGadget)
		if _, err := r.Build(); !errors.Is(err, ErrSchema) {
			t.Errorf("RateClass %d: Build = %v, want ErrSchema", rc, err)
		}
	}
	for _, rc := range []RateClass{RateDefault, RateAuth, RateExpensive} {
		r, g := newRouter(t, "getGadget")
		Handle(g, Route{Method: http.MethodGet, Path: "/gadgets/{id}", Name: "getGadget", Perm: rbac.Public, RateClass: rc}, getGadget)
		mustBuild(t, r)
	}
}

// TestHandle_PointerCreatedRefused is the review's m6: Resp = *Created[T]
// is refused at registration, never a reflection panic.
func TestHandle_PointerCreatedRefused(t *testing.T) {
	r, g := newRouter(t, "createGadget")
	Handle(g, Route{Method: http.MethodPost, Path: "/gadgets", Name: "createGadget", Perm: rbac.Public},
		func(_ context.Context, in *In[createGadgetReq]) (*Created[Gadget], error) {
			return &Created[Gadget]{Body: in.Req.Body}, nil
		})
	if _, err := r.Build(); !errors.Is(err, ErrSchema) || !strings.Contains(err.Error(), "by value") {
		t.Fatalf("Build = %v, want ErrSchema asking for Created by value", err)
	}
}
