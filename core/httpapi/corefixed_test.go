// file: core/httpapi/corefixed_test.go
// version: 1.0.0
// guid: bc1981a3-432a-4b32-a346-f04d6e6d9a5d
// last-edited: 2026-11-10

package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/rbac"
)

// oauthReq binds the {provider} wildcard of the two exact core patterns.
type oauthReq struct {
	Provider string `path:"provider"`
}

// oauthStub is the stand-in for authhttp's callback and start handlers (F4-05
// owns the real ones); it names the provider it was reached for.
type oauthStub struct {
	Provider string `json:"provider"`
}

func oauthHandler(_ context.Context, in *In[oauthReq]) (oauthStub, error) {
	return oauthStub{Provider: in.Req.Provider}, nil
}

// fixedRoute registers one public stub on g at method and path.
func fixedRoute(g *Group, method, path, name string) {
	Handle(g, Route{Method: method, Path: path, Name: name, Perm: rbac.Public, RateClass: RateAuth}, oauthHandler)
}

// oauthRoutes registers the two exact patterns' stubs on a fixed group.
func oauthRoutes(g *Group) {
	fixedRoute(g, http.MethodGet, "/{provider}/callback", "oauthCallback")
	fixedRoute(g, http.MethodGet, "/{provider}/start", "oauthStart")
}

// routeLines is Routes() reduced to the fields a registration decides: owner,
// method, pattern and kind.
func routeLines(r *Router) []string {
	var out []string
	for ri := range r.Routes() {
		out = append(out, fmt.Sprintf("%s %s %s %d", ri.Owner, ri.Method, ri.Pattern, ri.Kind))
	}
	return out
}

// publicManifest is a books-like mount's manifest with every route Public.
func publicManifest(routes ...LegacyRoute) []LegacyRoute {
	for i := range routes {
		routes[i].Perm, routes[i].Public = rbac.Public, true
	}
	return routes
}

func TestCoreExactPatterns_TwoEntries(t *testing.T) {
	want := []string{
		"GET /api/v1/auth/oauth/{provider}/callback",
		"GET /api/v1/auth/oauth/{provider}/start",
	}
	got := CoreExactPatterns()
	if !slices.Equal(got, want) {
		t.Fatalf("CoreExactPatterns() = %q, want exactly %q", got, want)
	}
	// R5: a fresh slice per call; a caller that scribbles on one cannot change
	// the list the next call (or Build) sees.
	got[0], got[1] = "GET /api/v1/anything", "POST /api/v1/else"
	if again := CoreExactPatterns(); !slices.Equal(again, want) {
		t.Fatalf("CoreExactPatterns() after the caller mutated a result = %q, want %q", again, want)
	}
}

func TestCoreFixedGroup_OIDCCallback(t *testing.T) {
	const base = "/api/v1/auth/oauth"

	// newFixed is a books-like legacy mount claiming /api/v1/ and a stub pair
	// on the fixed group; extra are more manifest routes of the mount.
	newFixed := func(t *testing.T, gin *fakeGin, extra ...LegacyRoute) (*Router, *Group) {
		t.Helper()
		r := gcpdRouter(t, gin, publicManifest(extra...)...)
		return r, r.CoreFixedGroup(base)
	}

	t.Run("builds and serves through the whole core chain as core", func(t *testing.T) {
		gin := newFakeGin("GET /api/v1/cases/:id")
		r, g := newFixed(t, gin, gothamRoute(http.MethodGet, "/api/v1/cases/:id"))
		oauthRoutes(g)
		var stages []int
		r.trace = func(s int) { stages = append(stages, s) }
		var observed []string
		r.observe = func(route string) { observed = append(observed, route) }
		h := mustBuild(t, r) // neither ErrPrefixCollision nor ErrShadow against gcpd's /api/v1/ claim
		if g.Module() != "core" || g.Base() != base {
			t.Fatalf("group = %s at %s, want core at %s", g.Module(), g.Base(), base)
		}
		for _, path := range []string{"callback", "start"} {
			stages = nil
			rec := serve(h, http.MethodGet, base+"/penguin/"+path, "")
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"provider":"penguin"`) {
				t.Fatalf("GET %s/penguin/%s = %d %s, want the stub answering for provider penguin", base, path, rec.Code, rec.Body)
			}
			if id := rec.Header().Get("X-Request-ID"); len(id) != 26 {
				t.Fatalf("GET %s/penguin/%s: X-Request-ID = %q, want stage 1's ULID", base, path, id)
			}
			if took := gin.took(); len(took) != 0 {
				t.Fatalf("GET %s/penguin/%s reached the legacy mount: %v", base, path, took)
			}
			// The chain is the one every core route runs: same stages, in the
			// same order, as a CoreGroup route (stage 14 is the handler).
			if want := []int{0, 1, 2, 3, 4, 5, 6, 7, 9, 10, 11}; !slices.Equal(stages, want) {
				t.Fatalf("GET %s/penguin/%s ran stages %v, want %v", base, path, stages, want)
			}
		}
		if want := []string{base + "/{provider}/callback", base + "/{provider}/start"}; !slices.Equal(observed, want) {
			t.Fatalf("stage 2 recorded routes %q, want %q", observed, want)
		}
		// The legacy mount still serves the rest of /api/v1/.
		if rec := serve(h, http.MethodGet, "/api/v1/cases/2", ""); rec.Code != http.StatusOK || len(gin.took()) != 1 {
			t.Fatalf("GET /api/v1/cases/2 = %d, want the legacy mount", rec.Code)
		}
		owners := map[string]string{}
		for ri := range r.Routes() {
			if strings.HasPrefix(ri.Pattern, base+"/") {
				owners[ri.Method+" "+ri.Pattern] = fmt.Sprintf("%s/%d", ri.Owner, ri.Kind)
			}
		}
		want := map[string]string{
			"GET " + base + "/{provider}/callback": fmt.Sprintf("core/%d", RouteKindNative),
			"GET " + base + "/{provider}/start":    fmt.Sprintf("core/%d", RouteKindNative),
		}
		if fmt.Sprint(owners) != fmt.Sprint(want) {
			t.Fatalf("RouteInfo of the fixed group = %v, want %v", owners, want)
		}
	})

	t.Run("a fixed-group route outside the two patterns is refused", func(t *testing.T) {
		for name, rt := range map[string]struct{ method, path string }{
			"other path":        {http.MethodGet, "/{provider}/other"},
			"other method":      {http.MethodPost, "/{provider}/callback"},
			"other wildcard":    {http.MethodGet, "/{vendor}/callback"},
			"literal provider":  {http.MethodGet, "/riddler/callback"},
			"deeper path":       {http.MethodGet, "/{provider}/callback/extra"},
			"the group's index": {http.MethodGet, "/{$}"},
		} {
			r, g := newFixed(t, newFakeGin())
			oauthRoutes(g)
			fixedRoute(g, rt.method, rt.path, "oauthExtra")
			_, err := r.Build()
			if !errors.Is(err, ErrPrefixCollision) || !strings.Contains(err.Error(), base+rt.path) {
				t.Errorf("%s: Build = %v, want ErrPrefixCollision naming %s%s", name, err, base, rt.path)
			}
		}
	})

	t.Run("a legacy manifest route over an entry is refused", func(t *testing.T) {
		for name, gin := range map[string]string{
			"the HEAD callback":      "GET /api/v1/auth/oauth/:provider/callback",
			"the HEAD start":         "GET /api/v1/auth/oauth/:provider/start",
			"another wildcard name":  "GET /api/v1/auth/oauth/:idp/callback",
			"HEAD, which GET covers": "HEAD /api/v1/auth/oauth/:provider/callback",
			"a literal provider":     "GET /api/v1/auth/oauth/riddler/callback",
			"a catch-all below":      "GET /api/v1/auth/oauth/*rest",
			"a wildcard above":       "GET /api/v1/auth/:kind/:provider/start",
		} {
			method, path, _ := strings.Cut(gin, " ")
			r, g := newFixed(t, newFakeGin(), gothamRoute(method, path))
			oauthRoutes(g)
			_, err := r.Build()
			if !errors.Is(err, ErrPrefixCollision) {
				t.Errorf("%s (%s): Build = %v, want ErrPrefixCollision", name, gin, err)
			}
			// The entry is declared inside the mount's claim, so it shadows
			// nothing: the refusal is the one ErrPrefixCollision, no ErrShadow.
			if errors.Is(err, ErrShadow) {
				t.Errorf("%s (%s): Build = %v, want no ErrShadow against the declared entry", name, gin, err)
			}
		}
	})

	t.Run("a legacy route that shares no request with an entry is accepted", func(t *testing.T) {
		gin := newFakeGin("POST /api/v1/auth/oauth/:provider/callback", "GET /api/v1/auth/oauth/:provider/other", "GET /api/v1/auth/oauth/status")
		r, g := newFixed(t, gin,
			gothamRoute(http.MethodPost, "/api/v1/auth/oauth/:provider/callback"), // a method the entry does not match
			gothamRoute(http.MethodGet, "/api/v1/auth/oauth/:provider/other"),     // a path it does not match
			gothamRoute(http.MethodGet, "/api/v1/auth/oauth/status"))
		oauthRoutes(g)
		mustBuild(t, r)
	})
}

// TestCoreFixedGroup_NoBaseNoGroup: a router whose app registers no
// fixed-group route builds exactly as before this PR, including a legacy
// manifest that holds HEAD's own callback and start routes (F4-08 deletes
// them in the PR that mounts core's), and an unused CoreFixedGroup changes
// nothing.
func TestCoreFixedGroup_NoBaseNoGroup(t *testing.T) {
	build := func(t *testing.T, touch bool) []string {
		t.Helper()
		routes := append(gcpdCaseRoutes(),
			gothamRoute(http.MethodGet, "/api/v1/auth/oauth/:provider/callback"),
			gothamRoute(http.MethodGet, "/api/v1/auth/oauth/:provider/start"))
		r := gcpdRouter(t, newFakeGin(), publicManifest(routes...)...)
		r.Infra("GET /livez", rbac.Public, http.NotFoundHandler())
		if touch {
			r.CoreFixedGroup("/api/v1/auth/oauth") // an app that registers nothing on it
		}
		mustBuild(t, r)
		return routeLines(r)
	}
	// Routes() of this router at BASE (a9d9f2136), before CoreFixedGroup existed.
	want := []string{
		"core GET /livez 4",
		"gcpd GET /api/v1/cases/{p4}/{p5} 3",
		"gcpd GET /api/v1/cases/{p4}/{p5} 3",
		"gcpd GET /api/v1/cases/v2/{id} 3",
		"gcpd GET /api/v1/cases/{id} 3",
		"gcpd POST /api/v1/cases 3",
		"gcpd GET /ping 3",
		"gcpd GET /api/v1/auth/oauth/{provider}/callback 3",
		"gcpd GET /api/v1/auth/oauth/{provider}/start 3",
	}
	for _, touch := range []bool{false, true} {
		if got := build(t, touch); !slices.Equal(got, want) {
			t.Fatalf("Routes() (CoreFixedGroup touched: %t) = %q, want %q", touch, got, want)
		}
	}
}

// TestCoreFixedBase_Invalid: a base that is not a clean literal path below "/"
// or that lies in core's own /api/core/ space is a recorded ErrPrefixCollision
// (as an invalid claim is).
func TestCoreFixedBase_Invalid(t *testing.T) {
	for _, base := range []string{"", "/", "api/v1/auth/oauth", "/api/v1/auth/oauth/", "/api/v1/{id}", "/api/v1/../x", "/api/core", "/api/core/v1/auth"} {
		r := NewRouter(Options{}, Deps{})
		g := r.CoreFixedGroup(base)
		oauthRoutes(g)
		_, err := r.Build()
		if !errors.Is(err, ErrPrefixCollision) {
			t.Errorf("CoreFixedGroup(%q): Build = %v, want ErrPrefixCollision", base, err)
		}
	}
}

// TestCoreFixedBase_SameBaseSameGroup: one base, one group; a second base is
// a second group, still owned by core.
func TestCoreFixedBase_SameBaseSameGroup(t *testing.T) {
	r := NewRouter(Options{}, Deps{})
	a, b := r.CoreFixedGroup("/api/v1/auth/oauth"), r.CoreFixedGroup("/api/v1/auth/oauth")
	if a != b {
		t.Fatal("two calls for one base returned two groups")
	}
	if c := r.CoreFixedGroup("/api/v1/auth/saml"); c == a || c.Module() != "core" {
		t.Fatalf("CoreFixedGroup(second base) = %p owner %q, want a distinct core group", c, c.Module())
	}
	if core := r.CoreGroup(); core == a || core.Base() != "/api/core/v1" {
		t.Fatalf("CoreGroup() = %s, want its own group at /api/core/v1", core.Base())
	}
}
