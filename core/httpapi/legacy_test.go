// file: core/httpapi/legacy_test.go
// version: 1.0.1
// guid: e2bad0d9-b859-4674-9232-a674448cef1f
// last-edited: 2026-11-07

package httpapi

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/mediacore/rbac"
)

// fakeGin stands in for a gin engine (core never imports gin, N7): it routes
// on the decoded r.URL.Path with gin's tree rule (a static segment before a
// parameter at the same depth), runs the post-match check through
// LegacyMatchAgrees first, as ABO's first gin middleware does (a request it
// routes nowhere skips the check and gets its NoRoute answer, 418 here), and
// records which route's handler ran.
type fakeGin struct {
	mu     sync.Mutex
	routes []ginRoute
	ran    []string
}

func newFakeGin(routes ...string) *fakeGin {
	g := &fakeGin{}
	for _, r := range routes {
		m, p, _ := strings.Cut(r, " ")
		g.routes = append(g.routes, ginRoute{method: m, path: p})
	}
	return g
}

func (g *fakeGin) match(method, path string) string {
	want := strings.Split(strings.TrimPrefix(path, "/"), "/")
	best := ""
	var bestSegs []string
	for _, r := range g.routes {
		if r.method != method {
			continue
		}
		segs := strings.Split(strings.TrimPrefix(r.path, "/"), "/")
		if len(segs) != len(want) {
			continue
		}
		ok := true
		for i, s := range segs {
			if !strings.HasPrefix(s, ":") && s != want[i] {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		if best == "" || ginBefore(segs, bestSegs) {
			best, bestSegs = r.path, segs
		}
	}
	return best
}

// ginBefore: at the first position where one is static and the other a
// parameter, the static one wins.
func ginBefore(a, b []string) bool {
	for i := range a {
		as, bs := !strings.HasPrefix(a[i], ":"), !strings.HasPrefix(b[i], ":")
		if as != bs {
			return as
		}
	}
	return false
}

func (g *fakeGin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	full := g.match(r.Method, r.URL.Path)
	if full == "" {
		http.Error(w, "noroute", http.StatusTeapot)
		return
	}
	if agrees, ok := LegacyMatchAgrees(r.Context(), r.Method, full); ok && !agrees {
		writeLegacyNotFound(r.Context(), w, nil)
		return
	}
	g.mu.Lock()
	g.ran = append(g.ran, r.Method+" "+full)
	g.mu.Unlock()
	pattern, _ := MatchedPattern(r.Context())
	_, _ = w.Write([]byte(pattern))
}

func (g *fakeGin) took() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := g.ran
	g.ran = nil
	return out
}

// gothamRoute is a manifest route with the defaults the tests vary from.
func gothamRoute(method, path string) LegacyRoute {
	return LegacyRoute{Method: method, GinPath: path, Perm: rbac.LegacyAtom("cases.view")}
}

func manifestOf(routes ...LegacyRoute) func(func(LegacyRoute) bool) {
	return func(yield func(LegacyRoute) bool) {
		for _, r := range routes {
			if !yield(r) {
				return
			}
		}
	}
}

// gcpdRouter mounts h for owner "gcpd" (claims /api/v1/ and /ping) with the
// manifest routes, as a LegacyAuthGin mount (which module may use it is the
// Registrar's check, not the router's).
func gcpdRouter(t *testing.T, h http.Handler, routes ...LegacyRoute) *Router {
	t.Helper()
	r := NewRouter(Options{}, Deps{})
	for _, c := range []string{"/api/v1/", "/ping"} {
		if err := r.Claim("gcpd", c); err != nil {
			t.Fatal(err)
		}
	}
	r.AllowAuthMethods("gcpd", "gcpd.signal")
	if err := r.MountLegacy(LegacyMount{Owner: "gcpd", Handler: h, Auth: LegacyAuthGin, TopSegments: []string{"ping"},
		Routes: manifestOf(routes...)}); err != nil {
		t.Fatal(err)
	}
	return r
}

func gcpdCaseRoutes() []LegacyRoute {
	return []LegacyRoute{
		gothamRoute(http.MethodGet, "/api/v1/cases/:id/clues"),
		gothamRoute(http.MethodGet, "/api/v1/cases/:id/suspects"),
		gothamRoute(http.MethodGet, "/api/v1/cases/v2/:id"),
		gothamRoute(http.MethodGet, "/api/v1/cases/:id"),
		gothamRoute(http.MethodPost, "/api/v1/cases"),
		{Method: http.MethodGet, GinPath: "/ping", Perm: rbac.Public, Public: true},
	}
}

func TestTranslateGinPath(t *testing.T) {
	for in, want := range map[string]string{
		"/api/v1/cases/:id":      "GET /api/v1/cases/{id}",
		"/files/*filepath":       "GET /files/{filepath...}",
		"/":                      "GET /{$}",
		"/api/v1/cases/":         "GET /api/v1/cases/{$}",
		"/api/v1/cases/export.x": "GET /api/v1/cases/export.x",
	} {
		p, err := translateGinPath(http.MethodGet, in)
		if err != nil || p.String() != want {
			t.Errorf("translate %q = %q, %v; want %q", in, p.String(), err, want)
		}
	}
	for _, bad := range []string{"api/v1", "/a//b", "/a/{b}", "/a/*rest/more", "/a/b:c", "/a/../b"} {
		if _, err := translateGinPath(http.MethodGet, bad); err == nil {
			t.Errorf("translate %q: want an error", bad)
		}
	}
}

func TestPatternRelationsAgreeWithServeMux(t *testing.T) {
	pats := []string{
		"GET /a/{x}/b", "GET /a/c/{y}", "HEAD /a/{x}/b", "POST /a/c/b", "GET /a/c/b", "/a/c/b",
		"GET /a/{x}", "GET /a/{x...}", "GET /a/c/{$}", "GET /{$}", "GET /a/{x}/{y}", "DELETE /a/{x}/b",
	}
	for _, a := range pats {
		for _, b := range pats {
			if a == b {
				continue
			}
			pa, _ := parseMuxPattern(splitPattern(a))
			pb, _ := parseMuxPattern(splitPattern(b))
			conflict := muxConflict(a, b)
			if conflict && !overlaps(pa, pb) {
				t.Errorf("%s and %s conflict in ServeMux but do not overlap structurally", a, b)
			}
			if !conflict && overlaps(pa, pb) && !strictlyMoreSpecific(pa, pb) && !strictlyMoreSpecific(pb, pa) {
				t.Errorf("%s and %s overlap without conflict, but neither is strictly more specific", a, b)
			}
			if conflict && (strictlyMoreSpecific(pa, pb) || strictlyMoreSpecific(pb, pa)) {
				t.Errorf("%s and %s conflict, yet one is strictly more specific", a, b)
			}
		}
	}
}

func TestLegacyMount_FoldsConflictingSet(t *testing.T) {
	g := newFakeGin("GET /api/v1/cases/:id/clues", "GET /api/v1/cases/:id/suspects", "GET /api/v1/cases/v2/:id",
		"GET /api/v1/cases/:id", "POST /api/v1/cases", "GET /ping")
	r := gcpdRouter(t, g, gcpdCaseRoutes()...)
	h := mustBuild(t, r)

	cases := []struct{ path, ran, pattern string }{
		{"/api/v1/cases/c-1/clues", "GET /api/v1/cases/:id/clues", "GET /api/v1/cases/{p4}/{p5}"},
		{"/api/v1/cases/c-1/suspects", "GET /api/v1/cases/:id/suspects", "GET /api/v1/cases/{p4}/{p5}"},
		{"/api/v1/cases/v2/c-1", "GET /api/v1/cases/v2/:id", "GET /api/v1/cases/v2/{id}"},
		// The overlap Go reports for the pair: gin routes it to the kept route,
		// and so does the mux, the kept route being more specific.
		{"/api/v1/cases/v2/clues", "GET /api/v1/cases/v2/:id", "GET /api/v1/cases/v2/{id}"},
		{"/api/v1/cases/c-1", "GET /api/v1/cases/:id", "GET /api/v1/cases/{id}"},
	}
	for _, c := range cases {
		rec := serve(h, http.MethodGet, c.path, "")
		if rec.Code != http.StatusOK || rec.Body.String() != c.pattern {
			t.Errorf("GET %s = %d %q, want 200 from pattern %q", c.path, rec.Code, rec.Body.String(), c.pattern)
		}
		if got := g.took(); !slices.Equal(got, []string{c.ran}) {
			t.Errorf("GET %s ran %v, want %s", c.path, got, c.ran)
		}
	}
	// A path the generalised pattern matches and no member does never reaches
	// a handler (rule 5): gin routes it nowhere and gives its own NoRoute
	// answer (418 here; HEAD's gin answers an /api/ path with its 404).
	rec := serve(h, http.MethodGet, "/api/v1/cases/c-1/alibis", "")
	if rec.Code != http.StatusTeapot {
		t.Errorf("non-member path = %d %q, want gin's NoRoute answer", rec.Code, rec.Body.String())
	}
	if got := g.took(); len(got) != 0 {
		t.Errorf("non-member path ran %v", got)
	}

	var infos []string
	for ri := range r.Routes() {
		if ri.Kind == RouteKindLegacy {
			infos = append(infos, ri.Method+" "+ri.Name+" -> "+ri.Pattern)
		}
	}
	slices.Sort(infos)
	want := []string{
		"GET /api/v1/cases/:id -> /api/v1/cases/{id}",
		"GET /api/v1/cases/:id/clues -> /api/v1/cases/{p4}/{p5}",
		"GET /api/v1/cases/:id/suspects -> /api/v1/cases/{p4}/{p5}",
		"GET /api/v1/cases/v2/:id -> /api/v1/cases/v2/{id}",
		"GET /ping -> /ping",
		"POST /api/v1/cases -> /api/v1/cases",
	}
	if !slices.Equal(infos, want) {
		t.Fatalf("legacy RouteInfo =\n%s\nwant\n%s", strings.Join(infos, "\n"), strings.Join(want, "\n"))
	}
}

func TestLegacyMount_EncodedSlashRefusedWhenGinDisagrees(t *testing.T) {
	g := newFakeGin("GET /api/v1/cases/:id", "GET /api/v1/cases/:id/clues")
	h := mustBuild(t, gcpdRouter(t, g, gothamRoute(http.MethodGet, "/api/v1/cases/:id"),
		gothamRoute(http.MethodGet, "/api/v1/cases/:id/clues")))
	// The mux sees one escaped segment and matches /cases/{id}; the decoded
	// path is /cases/c-1/clues, which gin would route to the other entry.
	rec := serve(h, http.MethodGet, "/api/v1/cases/c-1%2Fclues", "")
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "endpoint not found") {
		t.Fatalf("encoded slash = %d %q, want legacy 404", rec.Code, rec.Body.String())
	}
	if got := g.took(); len(got) != 0 {
		t.Fatalf("encoded slash ran %v", got)
	}
}

func TestLegacyMatchAgrees_OutsideTheWrapper(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "/x", nil)
	if _, ok := LegacyMatchAgrees(req.Context(), http.MethodGet, "/x"); ok {
		t.Fatal("LegacyMatchAgrees ok outside a wrapped request")
	}
	if _, ok := MatchedPattern(req.Context()); ok {
		t.Fatal("MatchedPattern ok outside a wrapped request")
	}
}

func TestCollisionRule_RefusesUnmergeableSets(t *testing.T) {
	base := func() []LegacyRoute {
		return []LegacyRoute{
			gothamRoute(http.MethodGet, "/api/v1/cases/:id/clues"),
			gothamRoute(http.MethodGet, "/api/v1/cases/v2/:id"),
		}
	}
	mutate := func(f func(*LegacyRoute)) []LegacyRoute {
		rs := base()
		f(&rs[0])
		rs = append(rs, gothamRoute(http.MethodGet, "/api/v1/cases/:id/suspects"))
		return rs
	}
	cases := map[string][]LegacyRoute{
		"Perm":        mutate(func(r *LegacyRoute) { r.Perm = rbac.LegacyAtom("cases.manage") }),
		"Guards":      mutate(func(r *LegacyRoute) { r.Guards.Interactive = true }),
		"RateClass":   mutate(func(r *LegacyRoute) { r.RateClass = RateExempt }),
		"OwnAuth":     mutate(func(r *LegacyRoute) { r.OwnAuth, r.AuthMethod, r.Perm = true, "gcpd.signal", rbac.Delegated("signal") }),
		"BasicExempt": mutate(func(r *LegacyRoute) { r.BasicExempt = true }),
		"NoCompress":  mutate(func(r *LegacyRoute) { r.NoCompress = true }),
		"a {name...} wildcard": {
			gothamRoute(http.MethodGet, "/api/v1/:unit/clues"),
			gothamRoute(http.MethodGet, "/api/v1/cases/*rest"),
		},
		"kept route not strictly more specific": {
			gothamRoute(http.MethodHead, "/api/v1/cases/:id/clues"),
			gothamRoute(http.MethodGet, "/api/v1/cases/v2/:id"),
		},
		"generalised pattern conflicts with another route": append(base(),
			gothamRoute(http.MethodGet, "/api/v1/:unit/z/w")),
	}
	for name, routes := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := gcpdRouter(t, newFakeGin(), routes...).Build()
			if !errors.Is(err, ErrPatternConflict) {
				t.Fatalf("Build = %v, want ErrPatternConflict", err)
			}
			for _, r := range routes[:1] {
				p, _ := translateGinPath(r.Method, r.GinPath)
				if !strings.Contains(err.Error(), p.String()) {
					t.Errorf("error does not name %s: %v", p, err)
				}
			}
		})
	}

	t.Run("two owners", func(t *testing.T) {
		mk := func(owner, method, path string, mount int) *node {
			p, _ := translateGinPath(method, path)
			return &node{owner: owner, legacy: true, pat: p, key: p.String(), mount: mount, route: gothamRoute(method, path)}
		}
		_, errs := collisionPlan([]*node{
			mk("gcpd", http.MethodGet, "/api/v1/cases/:id/clues", 0),
			mk("arkham", http.MethodGet, "/api/v1/cases/v2/:id", 1),
		})
		if len(errs) != 1 || !errors.Is(errs[0], ErrPatternConflict) || !strings.Contains(errs[0].Error(), "rule 1") {
			t.Fatalf("collisionPlan = %v, want one rule-1 ErrPatternConflict", errs)
		}
	})
}

func TestOutsideClaim_Refused(t *testing.T) {
	r := NewRouter(Options{}, Deps{})
	r.CoreGroup()
	if err := r.Claim("gcpd", "/api/v1/"); err != nil {
		t.Fatal(err)
	}
	if err := r.MountLegacy(LegacyMount{Owner: "gcpd", Handler: newFakeGin(), Auth: LegacyAuthGin,
		Routes: manifestOf(gothamRoute(http.MethodGet, "/api/core/v1/auth/oidc/google/callback"))}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Build(); !errors.Is(err, ErrOutsideClaim) {
		t.Fatalf("Build = %v, want ErrOutsideClaim", err)
	}
	// "/" belongs to the "/" owner, never to a manifest route.
	r = gcpdRouter(t, newFakeGin(), LegacyRoute{Method: http.MethodGet, GinPath: "/", Perm: rbac.Public, Public: true})
	if _, err := r.Build(); !errors.Is(err, ErrOutsideClaim) {
		t.Fatalf("Build with a manifest root route = %v, want ErrOutsideClaim", err)
	}
}

func TestBuild_CrossOwnerShadowRefused(t *testing.T) {
	r := NewRouter(Options{}, Deps{})
	for _, c := range []struct{ owner, prefix string }{{"gcpd", "/api/v1/"}, {"arkham", "/arkham"}} {
		if err := r.Claim(c.owner, c.prefix); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.MountLegacy(LegacyMount{Owner: "gcpd", Handler: newFakeGin(), Auth: LegacyAuthGin,
		Routes: manifestOf(gothamRoute(http.MethodGet, "/api/v1/cases/:id"))}); err != nil {
		t.Fatal(err)
	}
	// Claims keep two mounts' routes apart, so the shadow is shown against a
	// core infra route that registers no claim.
	if err := r.MountLegacy(LegacyMount{Owner: "arkham", Handler: newFakeGin(), Auth: LegacyAuthGin, TopSegments: []string{"arkham"},
		Routes: manifestOf(gothamRoute(http.MethodGet, "/arkham"))}); err != nil {
		t.Fatal(err)
	}
	r.Infra("/arkham", rbac.Public, http.NotFoundHandler()) // core: any method; arkham's GET is strictly more specific
	_, err := r.Build()
	if !errors.Is(err, ErrShadow) || !strings.Contains(err.Error(), "arkham GET /arkham is strictly more specific than core /arkham") {
		t.Fatalf("Build = %v, want ErrShadow naming arkham's route over core's", err)
	}
}

func TestBuild_PublicNotInManifestRefused(t *testing.T) {
	r, g := newRouter(t) // batcave lists no public route
	Handle(g, Route{Method: http.MethodGet, Path: "/signal", Name: "getSignal", Perm: rbac.Public}, getGadget)
	if _, err := r.Build(); !errors.Is(err, ErrPublicNotAllowed) {
		t.Fatalf("Build = %v, want ErrPublicNotAllowed", err)
	}
	// The legacy counterpart: Public and rbac.Public go together.
	_, err := gcpdRouter(t, newFakeGin(), LegacyRoute{Method: http.MethodGet, GinPath: "/ping", Perm: rbac.Public}).Build()
	if !errors.Is(err, ErrLegacyManifest) {
		t.Fatalf("legacy rbac.Public without Public = %v, want ErrLegacyManifest", err)
	}
}

func TestOwnAuthMethod_Admission(t *testing.T) {
	own := LegacyRoute{Method: http.MethodGet, GinPath: "/api/v1/signal", Perm: rbac.Delegated("signal"),
		OwnAuth: true, AuthMethod: "gcpd.flare"}
	if _, err := gcpdRouter(t, newFakeGin(), own).Build(); !errors.Is(err, ErrOwnAuthMethod) {
		t.Fatalf("OwnAuth with an unregistered method = %v, want ErrOwnAuthMethod", err)
	}
	own.AuthMethod = ""
	if _, err := gcpdRouter(t, newFakeGin(), own).Build(); !errors.Is(err, ErrOwnAuthMethod) {
		t.Fatalf("OwnAuth with no method = %v, want ErrOwnAuthMethod", err)
	}
	own.AuthMethod = "gcpd.signal"
	if _, err := gcpdRouter(t, newFakeGin(), own).Build(); err != nil {
		t.Fatalf("OwnAuth with its owner's method: %v", err)
	}
	notOwn := gothamRoute(http.MethodGet, "/api/v1/signal")
	notOwn.Perm = rbac.Delegated("signal")
	if _, err := gcpdRouter(t, newFakeGin(), notOwn).Build(); !errors.Is(err, ErrLegacyManifest) {
		t.Fatalf("Delegated without OwnAuth = %v, want ErrLegacyManifest", err)
	}
}

func TestLegacyManifest_Refusals(t *testing.T) {
	cases := map[string]struct {
		route LegacyRoute
		want  error
	}{
		"zero permission":        {LegacyRoute{Method: http.MethodGet, GinPath: "/api/v1/cases"}, ErrNoPermission},
		"bad method":             {LegacyRoute{Method: "get", GinPath: "/api/v1/cases", Perm: rbac.AnyAuthenticated}, ErrLegacyManifest},
		"untranslatable path":    {gothamRoute(http.MethodGet, "/api/v1/{cases}"), ErrLegacyManifest},
		"in-handler check":       {LegacyRoute{Method: http.MethodGet, GinPath: "/api/v1/cases", Perm: rbac.LegacyCheck("cases.view", false)}, ErrLegacyManifest},
		"unknown top segment":    {gothamRoute(http.MethodGet, "/batsignal"), ErrPrefixCollision},
		"AuthMethod w/o OwnAuth": {LegacyRoute{Method: http.MethodGet, GinPath: "/api/v1/cases", Perm: rbac.AnyAuthenticated, AuthMethod: "gcpd.signal"}, ErrLegacyManifest},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := gcpdRouter(t, newFakeGin(), c.route).Build(); !errors.Is(err, c.want) {
				t.Fatalf("Build = %v, want %v", err, c.want)
			}
		})
	}
	dup := gothamRoute(http.MethodGet, "/api/v1/cases")
	if _, err := gcpdRouter(t, newFakeGin(), dup, dup).Build(); !errors.Is(err, ErrDuplicateRoute) {
		t.Fatalf("duplicate manifest route = %v, want ErrDuplicateRoute", err)
	}
	// LegacyAuthCore cannot be enforced before the auth service exists (R2).
	r := NewRouter(Options{}, Deps{})
	if err := r.Claim("gcpd", "/api/v1/"); err != nil {
		t.Fatal(err)
	}
	if err := r.MountLegacy(LegacyMount{Owner: "gcpd", Handler: newFakeGin(), Auth: LegacyAuthCore,
		Routes: manifestOf(gothamRoute(http.MethodGet, "/api/v1/cases"))}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Build(); !errors.Is(err, ErrNoAuthService) {
		t.Fatalf("LegacyAuthCore mount = %v, want ErrNoAuthService", err)
	}
	// A manifest needs an Auth.
	r = NewRouter(Options{}, Deps{})
	if err := r.MountLegacy(LegacyMount{Owner: "gcpd", Handler: newFakeGin(),
		Routes: manifestOf(gothamRoute(http.MethodGet, "/api/v1/cases"))}); !errors.Is(err, ErrLegacyManifest) {
		t.Fatalf("manifest without Auth = %v, want ErrLegacyManifest", err)
	}
}

func TestLegacyRedirect_CoversOnlyUnclaimedPaths(t *testing.T) {
	g := newFakeGin("GET /api/v1/cases")
	r := NewRouter(Options{}, Deps{})
	r.CoreGroup()
	r.ModuleGroup("oracle")
	if err := r.Claim("gcpd", "/api/v1/"); err != nil {
		t.Fatal(err)
	}
	loc := func(req *http.Request) (string, bool) {
		p := req.URL.Path
		if strings.HasPrefix(p, "/api/v1/") || strings.HasPrefix(p, "/api/joker") {
			return "", false
		}
		return strings.Replace(p, "/api/", "/api/v1/", 1), true
	}
	if err := r.MountLegacy(LegacyMount{Owner: "gcpd", Handler: g, Auth: LegacyAuthGin,
		Routes:    manifestOf(gothamRoute(http.MethodGet, "/api/v1/cases")),
		Redirects: []LegacyRedirect{{Prefix: "/api/", Location: loc}}}); err != nil {
		t.Fatal(err)
	}
	h := mustBuild(t, r)
	if rec := serve(h, http.MethodGet, "/api/cases", ""); rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/api/v1/cases" {
		t.Errorf("/api/cases = %d %q, want 301 to /api/v1/cases", rec.Code, rec.Header().Get("Location"))
	}
	if rec := serve(h, http.MethodGet, "/api/joker/laugh", ""); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "endpoint not found") {
		t.Errorf("excluded path = %d %q, want legacy 404", rec.Code, rec.Body.String())
	}
	for _, p := range []string{"/api/core/v1/nope", "/api/oracle/v1/nope"} {
		rec := serve(h, http.MethodGet, p, "")
		if rec.Code != http.StatusNotFound || rec.Header().Get("Content-Type") != "application/problem+json" {
			t.Errorf("%s = %d %q, want the claim owner's problem 404", p, rec.Code, rec.Header().Get("Content-Type"))
		}
	}
	if rec := serve(h, http.MethodGet, "/api/v1/cases", ""); rec.Code != http.StatusOK {
		t.Errorf("a manifest route = %d, want 200", rec.Code)
	}
	if err := NewRouter(Options{}, Deps{}).MountLegacy(LegacyMount{Owner: "gcpd", Handler: g,
		Redirects: []LegacyRedirect{{Prefix: "/api", Location: loc}}}); !errors.Is(err, ErrLegacyManifest) {
		t.Fatalf("redirect without a subtree prefix = %v, want ErrLegacyManifest", err)
	}
}

const legacyNotFound = `{"error":"endpoint not found","code":"NOT_FOUND","status":404}`

// TestDispatch_LegacyWrongMethodIsLegacyNotFound: HEAD's gin has
// HandleMethodNotAllowed off, so a wrong method on a manifest path is gin's
// 404, byte for byte, never core's problem 405 with an Allow header.
func TestDispatch_LegacyWrongMethodIsLegacyNotFound(t *testing.T) {
	g := newFakeGin("GET /api/v1/cases/:id", "POST /api/v1/cases")
	h := mustBuild(t, gcpdRouter(t, g, gothamRoute(http.MethodGet, "/api/v1/cases/:id"),
		gothamRoute(http.MethodPost, "/api/v1/cases")))
	for _, c := range [][2]string{{http.MethodDelete, "/api/v1/cases/penguin"}, {http.MethodGet, "/api/v1/cases"}, {http.MethodPut, "/api/v1/cases"}} {
		rec := serve(h, c[0], c[1], "")
		if rec.Code != http.StatusNotFound || rec.Body.String() != legacyNotFound || rec.Header().Get("Allow") != "" {
			t.Errorf("%s %s = %d %q Allow %q, want the legacy 404", c[0], c[1], rec.Code, rec.Body.String(), rec.Header().Get("Allow"))
		}
	}
	if ran := g.took(); len(ran) != 0 {
		t.Fatalf("gin ran %v", ran)
	}
}

// TestDispatch_TrailingSlashPatternNoRedirect: ServeMux answers GET /x with a
// 307 to /x/ when only "/x/{$}" is registered; the router answers it as the
// miss it is (a problem 404 for a native path, the legacy 404 under a legacy
// claim), and the slashed path is still served.
func TestDispatch_TrailingSlashPatternNoRedirect(t *testing.T) {
	r, grp := newRouter(t, "listGadgets")
	Handle(grp, Route{Method: http.MethodGet, Path: "/gadgets/{$}", Name: "listGadgets", Perm: rbac.Public}, noop)
	h := mustBuild(t, r)
	rec := serve(h, http.MethodGet, "/api/batcave/v1/gadgets", "")
	if rec.Code != http.StatusNotFound || rec.Header().Get("Content-Type") != "application/problem+json" || rec.Header().Get("Location") != "" {
		t.Fatalf("GET without the slash = %d %q Location %q, want problem 404", rec.Code, rec.Header().Get("Content-Type"), rec.Header().Get("Location"))
	}
	if rec := serve(h, http.MethodGet, "/api/batcave/v1/gadgets/", ""); rec.Code != http.StatusOK {
		t.Fatalf("GET with the slash = %d %q", rec.Code, rec.Body.String())
	}

	g := newFakeGin("GET /api/v1/cases/:id/")
	lh := mustBuild(t, gcpdRouter(t, g, gothamRoute(http.MethodGet, "/api/v1/cases/:id/")))
	rec = serve(lh, http.MethodGet, "/api/v1/cases/penguin", "")
	if rec.Code != http.StatusNotFound || rec.Body.String() != legacyNotFound {
		t.Fatalf("legacy GET without the slash = %d %q Location %q, want the legacy 404", rec.Code, rec.Body.String(), rec.Header().Get("Location"))
	}
	if rec := serve(lh, http.MethodGet, "/api/v1/cases/penguin/", ""); rec.Code != http.StatusOK || !slices.Equal(g.took(), []string{"GET /api/v1/cases/:id/"}) {
		t.Fatalf("legacy GET with the slash = %d %q", rec.Code, rec.Body.String())
	}
}

// TestDispatch_SPASharedGetServesShell is the request half of 06 §6.10's
// TestReservedSegmentsDisjointFromNonSPA: "login" is a SPAShared segment under
// its owner's claim; GET /login, which no route matches, goes to the "/"
// owner (the same engine, whose NoRoute page is the shell: 418 here), and
// POST /login reaches the mount's route.
func TestDispatch_SPASharedGetServesShell(t *testing.T) {
	g := newFakeGin("POST /login")
	r := NewRouter(Options{}, Deps{})
	if err := r.Claim("gcpd", "/login"); err != nil {
		t.Fatal(err)
	}
	if err := r.MountLegacy(LegacyMount{Owner: "gcpd", Handler: g, Auth: LegacyAuthGin, ServesSPA: true, SPAShared: []string{"login"},
		Routes: manifestOf(LegacyRoute{Method: http.MethodPost, GinPath: "/login", Perm: rbac.Public, Public: true})}); err != nil {
		t.Fatal(err)
	}
	h := mustBuild(t, r)
	if rec := serve(h, http.MethodPost, "/login", ""); rec.Code != http.StatusOK || !slices.Equal(g.took(), []string{"POST /login"}) {
		t.Fatalf("POST /login = %d %q", rec.Code, rec.Body.String())
	}
	if rec := serve(h, http.MethodGet, "/login", ""); rec.Code != http.StatusTeapot {
		t.Fatalf("GET /login = %d %q, want the shell (gin's NoRoute)", rec.Code, rec.Body.String())
	}
}
