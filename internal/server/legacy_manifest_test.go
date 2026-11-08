// file: internal/server/legacy_manifest_test.go
// version: 1.0.6
// guid: e72629cb-5107-4d3a-a859-c970b70f9c17
// last-edited: 2026-11-08

package server

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
)

// These tests hold books' legacy manifest (legacy_manifest.go) to the real
// gin engine built by this package, with the ABS surface off and on, never
// to a fixture (05-http-api §4.8 rule 6, §12).

const legacyNotFoundBody = `{"error":"endpoint not found","code":"NOT_FOUND","status":404}`

// legacyEngine builds the real server with the ABS surface off or on. The
// fingerprint worker flag is on so its routes answer past their flag guard;
// rateLimited turns the /api limiter on at one request a minute.
func legacyEngine(t *testing.T, abs, rateLimited bool) *credGuardFixture {
	t.Helper()
	return setupCredGuardServerWith(t, func(c *config.Config) {
		c.FingerprintRemoteWorkersEnabled = true
		c.ABSAPIEnabled = abs
		if abs {
			c.ABSJWTSecret = "synthetic-legacy-manifest-secret-0123456789abcdef"
		}
		if rateLimited {
			c.EnableRateLimit = true
			c.APIRateLimitPerMinute = 1
		}
	})
}

func absModes() []bool { return []bool{false, true} }

func absName(abs bool) string {
	if abs {
		return "abs_on"
	}
	return "abs_off"
}

// legacyConcretePath fills a gin path's parameters with a placeholder segment.
func legacyConcretePath(gin string) string {
	segs := strings.Split(gin, "/")
	for i, s := range segs {
		if strings.HasPrefix(s, ":") || strings.HasPrefix(s, "*") {
			segs[i] = "riddler"
		}
	}
	return strings.Join(segs, "/")
}

// probe sends one request with an already-cancelled context, so a handler
// that does run returns at once, and an optional bearer credential.
func probe(h http.Handler, method, path, bearer string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	ctx, cancel := context.WithCancel(req.Context())
	cancel()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req.WithContext(ctx))
	return w
}

// TestLegacyManifest_MatchesGinRoutes: every gin route has a manifest row and
// every row a gin route (the drift check LegacyManifest runs at boot), and
// each row's permission, guards and rate class are what the gin chain
// enforces today. The permission oracle is the chain itself: a signed-in
// user holding no permission is refused by s.perm with the atom's name, by
// RequireAdmin with the admin message, by ABSRequireAuth as an ABS token;
// a permission-less route lets the request through.
func TestLegacyManifest_MatchesGinRoutes(t *testing.T) {
	for _, abs := range absModes() {
		t.Run(absName(abs), func(t *testing.T) {
			limited := rateLimitedRoutes(t, abs)
			f := legacyEngine(t, abs, false)
			manifest, err := f.srv.LegacyManifest()
			require.NoError(t, err)
			nobody, err := f.store.CreateUser("synthetic-alfred", "alfred@example.test", "bcrypt", "x", []string{}, "active")
			require.NoError(t, err)

			atoms := 0
			for _, lr := range manifest {
				key := lr.Method + " " + lr.GinPath
				path := legacyConcretePath(lr.GinPath)
				anon := probe(f.srv.router, lr.Method, path, "")
				authRequired := anon.Code == http.StatusUnauthorized && strings.Contains(anon.Body.String(), "authentication required")
				var signedIn *httptest.ResponseRecorder
				if authRequired {
					sess, err := f.store.CreateSessionWithOrigin(nobody.ID, "127.0.0.1", "test", time.Hour, database.SessionOriginPassword, 0)
					require.NoError(t, err)
					signedIn = probe(f.srv.router, lr.Method, path, sess.ID)
				}
				body := ""
				if signedIn != nil {
					body = signedIn.Body.String()
				}
				switch lr.Perm.Kind() {
				case rbac.KindLegacyAtom:
					atoms++
					assert.Contains(t, body, `permission denied: `+lr.Perm.Atom()+`"`, "%s: s.perm(%s) expected", key, lr.Perm.Atom())
				case rbac.KindAllOf:
					assert.Contains(t, body, "admin role required", "%s: an admin gate expected", key)
					assertAdminKind(t, f.srv, key, lr.Perm)
				case rbac.KindAuthenticated:
					assert.True(t, authRequired, "%s: authentication expected (%d %s)", key, anon.Code, anon.Body.String())
					assert.NotContains(t, body, "permission denied", "%s: has a route permission", key)
					assert.NotContains(t, body, "admin role required", "%s: has an admin gate", key)
					assert.NotContains(t, body, "invalid access token", "%s: runs ABSRequireAuth", key)
				case rbac.KindDelegated:
					assert.True(t, lr.OwnAuth, key)
					assert.Contains(t, body, "invalid access token", "%s: ABSRequireAuth expected", key)
				case rbac.KindPublic:
					if body, ok := publicHandlerRefusals[key]; ok {
						// The handler itself answers an empty credential
						// request 401; no gate runs before it.
						assert.Equal(t, http.StatusUnauthorized, anon.Code, key)
						assert.Equal(t, body, anon.Body.String(), "%s: not the handler's own refusal", key)
						break
					}
					assert.NotContains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, anon.Code,
						"%s: Public, but an anonymous request is refused: %d %s", key, anon.Code, anon.Body.String())
				default:
					t.Errorf("%s: unexpected requirement %s", key, lr.Perm)
				}
				_, cred := f.srv.credRoutes[key]
				assert.Equal(t, cred && f.srv.credRoutes[key] == credRouteAlways, lr.Guards.Interactive,
					"%s: Guards.Interactive must equal credRoute (always) registration", key)
				assert.Equal(t, limited[key], lr.RateClass == httpapi.RateDefault,
					"%s: RateDefault iff the /api rate limiter is in the chain", key)
			}
			assert.Equal(t, 426, atoms, "s.perm routes: the 421 s.perm call sites plus the five worker routes of its one shared s.perm")
		})
	}
}

// publicHandlerRefusals are the Public routes whose handler, not a gate,
// answers an anonymous request with no credentials 401: the body each
// handler writes, so a gate added in front of them still fails the check.
var publicHandlerRefusals = map[string]string{
	"POST /auth/refresh": `{"error":"Session not found"}`,
	"POST /login":        `{"error":"invalid credentials"}`,
}

// rateLimitedRoutes exhausts the /api limiter and returns the routes that
// then answer 429: the ones whose chain includes it.
func rateLimitedRoutes(t *testing.T, abs bool) map[string]bool {
	t.Helper()
	// HEAD installed its /api rate limiter on the /api/v1 and /api/v3 groups;
	// since R1-17 core's stage 10 limits by RateClass, and the hook marks the
	// routes those groups cover, the set the limiter covered.
	apiGroupHookForTest = func(c *gin.Context) { c.AbortWithStatus(http.StatusTooManyRequests) }
	f := legacyEngine(t, abs, false) // the hook is read once, by setupRoutes
	apiGroupHookForTest = nil
	out := map[string]bool{}
	for _, r := range f.srv.router.Routes() {
		if probe(f.srv.router, r.Method, legacyConcretePath(r.Path), "").Code == http.StatusTooManyRequests {
			out[r.Method+" "+r.Path] = true
		}
	}
	require.NotEmpty(t, out, "the limiter never answered 429")
	return out
}

// assertAdminKind checks an admin row's permission against the route's
// recorded kind (05-rbac §6.2): read-only introspection opens with auth
// off, everything else keeps answering 401.
func assertAdminKind(t *testing.T, srv *Server, key string, perm rbac.Requirement) {
	t.Helper()
	kind, recorded := srv.adminRoutes[key]
	switch perm.String() {
	case "core:system.view", "books:admin.view":
		assert.Equal(t, adminRouteReadOnly, kind, "%s", key)
	case "all(core:users.manage,books:admin.view)":
		// The master-list group wires adminRole directly (read-only kind)
		// with s.perm(users.manage) behind it.
		assert.False(t, recorded, "%s: master-list routes are not adminRoute registrations", key)
		assert.True(t, strings.HasPrefix(key[strings.IndexByte(key, ' ')+1:], "/api/v1/master-lists"), key)
	case "books:admin.debug", "core:admin":
		assert.Equal(t, adminRouteAlwaysAuthenticated, kind, "%s", key)
	default:
		t.Errorf("%s: %s is not a 05-rbac §6.2 admin permission", key, perm)
	}
}

// TestLegacyManifest_OwnerGuardsMatchRegistrations: the Guards.Owner rows
// are exactly the routes registered through ownerCredRoute, the one way a
// route gets ownerGateWhenOwnerSet (s.ownerRoutes), one to one (review
// MINOR-3). TestOwnerGateOnlyThroughOwnerCredRoute keeps it the one way.
func TestLegacyManifest_OwnerGuardsMatchRegistrations(t *testing.T) {
	for _, abs := range absModes() {
		t.Run(absName(abs), func(t *testing.T) {
			f := legacyEngine(t, abs, false)
			manifest, err := f.srv.LegacyManifest()
			require.NoError(t, err)
			var rows []string
			for _, lr := range manifest {
				if lr.Guards.Owner {
					rows = append(rows, lr.Method+" "+lr.GinPath)
				}
			}
			var registered []string
			for key := range f.srv.ownerRoutes {
				registered = append(registered, key)
			}
			slices.Sort(rows)
			slices.Sort(registered)
			assert.Len(t, registered, 10, "ownerCredRoute registrations at the base")
			assert.Equal(t, registered, rows, "Guards.Owner rows vs ownerCredRoute registrations")
		})
	}
}

// ownerGateViolations reports, for one parsed file of this package, every
// use of the owner gate that bypasses ownerCredRoute's recording: any
// reference to ownerGateWhenOwnerSet (called or not, on any receiver, method
// values and expressions included) outside the ownerCredRoute function, and
// any reference to RequireOwner outside owner_routes.go.
func ownerGateViolations(fset *token.FileSet, name string, f *ast.File) []string {
	var out []string
	for _, decl := range f.Decls {
		fn, _ := decl.(*ast.FuncDecl)
		inOwnerCredRoute := fn != nil && fn.Name.Name == "ownerCredRoute" && fn.Recv != nil
		ast.Inspect(decl, func(n ast.Node) bool {
			// Every use is an identifier: the Sel of a selector (s.X, a method
			// value or expression) or a bare name. A declaration's own name
			// is not a use.
			id, ok := n.(*ast.Ident)
			if !ok || (fn != nil && id == fn.Name) {
				return true
			}
			switch {
			case id.Name == "ownerGateWhenOwnerSet" && !inOwnerCredRoute:
				out = append(out, fmt.Sprintf("%s: ownerGateWhenOwnerSet outside ownerCredRoute", fset.Position(id.Pos())))
			case id.Name == "RequireOwner" && name != "owner_routes.go":
				out = append(out, fmt.Sprintf("%s: RequireOwner outside owner_routes.go", fset.Position(id.Pos())))
			}
			return true
		})
	}
	return out
}

// TestOwnerGateOnlyThroughOwnerCredRoute: no non-test file of this package
// reaches the owner gate except through ownerCredRoute, so s.ownerRoutes
// records every owner-gated route (review MINOR-N2: a go/ast scan, not a
// text count).
func TestOwnerGateOnlyThroughOwnerCredRoute(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	fset := token.NewFileSet()
	scanned := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		require.NoError(t, err)
		scanned++
		assert.Empty(t, ownerGateViolations(fset, name, f), "register owner-gated routes with ownerCredRoute")
	}
	assert.Greater(t, scanned, 50, "too few files scanned; the glob is broken")
}

// TestOwnerGateViolations_CatchesBypasses proves the scan on the forms a text
// count misses: a bound method value, another receiver, a method expression
// and a direct RequireOwner call, each outside ownerCredRoute.
func TestOwnerGateViolations_CatchesBypasses(t *testing.T) {
	const src = `package server

func (s *Server) wireArkham(g *gin.RouterGroup, server *Server) {
	og := s.ownerGateWhenOwnerSet
	g.POST("/a", og())
	g.POST("/b", server.ownerGateWhenOwnerSet())
	g.POST("/c", (*Server).ownerGateWhenOwnerSet(s))
	g.POST("/d", servermiddleware.RequireOwner(nil, "", nil))
}

func (s *Server) ownerCredRoute() { _ = s.ownerGateWhenOwnerSet() }
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "wire_arkham.go", src, parser.SkipObjectResolution)
	require.NoError(t, err)
	got := ownerGateViolations(fset, "wire_arkham.go", f)
	assert.Len(t, got, 4, "%v", got)
	for _, line := range []string{":4:", ":6:", ":7:", ":8:"} {
		assert.True(t, slices.ContainsFunc(got, func(v string) bool { return strings.Contains(v, line) }), "no violation at line %s: %v", line, got)
	}
}

// booksMount mounts the real engine in a core router as books' legacy mount,
// with the claims, top segments and redirects internal/books declares
// (LegacyPrefixes, LegacyTopSegments, LegacySPAShared, LegacyRedirects).
func booksMount(t *testing.T, f *credGuardFixture) (*httpapi.Router, http.Handler) {
	t.Helper()
	manifest, err := f.srv.LegacyManifest()
	require.NoError(t, err)
	r := httpapi.NewRouter(httpapi.Options{}, httpapi.Deps{})
	r.CoreGroup()
	for _, c := range LegacyPrefixes(config.AppConfig.ABSAPIEnabled) {
		require.NoError(t, r.Claim("books", c))
	}
	r.AllowAuthMethods("books", absAuthMethod)
	csp, report, build := f.srv.LegacyPerimeter()
	require.NoError(t, r.MountLegacy(httpapi.LegacyMount{
		Owner: "books", Handler: f.srv.router, Auth: httpapi.LegacyAuthGin, ServesSPA: true,
		Routes:      slices.Values(manifest),
		Redirects:   f.srv.LegacyRedirects(),
		TopSegments: LegacyTopSegments(),
		SPAShared:   LegacySPAShared(),
		DefaultCSP:  csp, CSPReport: report, BuildID: build,
	}))
	h, err := r.Build()
	require.NoError(t, err)
	return r, h
}

// generalised are the generalised patterns of 05-http-api §4.8 and their
// members; kept the routes that stay their own patterns.
func generalised(abs bool) (map[string][]string, []string) {
	gen := map[string][]string{
		"/api/v1/operations/{p4}/{p5}": {"/api/v1/operations/:id/activity", "/api/v1/operations/:id/changes",
			"/api/v1/operations/:id/result", "/api/v1/operations/:id/results"},
	}
	kept := []string{"/api/v1/operations/v2/:id"}
	if abs {
		gen["/api/me/item/{p4}/{p5}"] = []string{"/api/me/item/:id/remove-from-continue-listening"}
		kept = append(kept, "/api/me/item/listening-sessions/:id")
	}
	return gen, kept
}

// TestLegacyManifest_RegistersAsMuxPatterns: on the real engine, ABS off and
// on, every gin route is a mux pattern or a member of one generalised
// pattern, Build raises no conflict, §4.8's generalised patterns and kept
// routes are present, and for each conflicting pair Go's overlap example is
// routed by the mux to a pattern holding gin's route (09 §9.4 R1).
func TestLegacyManifest_RegistersAsMuxPatterns(t *testing.T) {
	for _, abs := range absModes() {
		t.Run(absName(abs), func(t *testing.T) {
			f := legacyEngine(t, abs, false)
			r, h := booksMount(t, f)
			served := map[string]string{}
			for ri := range r.Routes() {
				if ri.Kind == httpapi.RouteKindLegacy {
					served[ri.Method+" "+ri.Name] = ri.Pattern
				}
			}
			n := 0
			for _, gr := range f.srv.router.Routes() {
				if gr.Path == "/" {
					continue
				}
				n++
				_, ok := served[gr.Method+" "+gr.Path]
				assert.True(t, ok, "gin route %s %s is not served by a mux pattern", gr.Method, gr.Path)
			}
			want := 497
			if abs {
				want = 576
			}
			assert.Equal(t, want, n, "gin routes besides GET / (498 and 577 with it)")
			assert.Len(t, served, n)

			gen, kept := generalised(abs)
			for pattern, members := range gen {
				for _, m := range members {
					assert.Equal(t, pattern, served["GET "+m], "member %s", m)
				}
			}
			for _, k := range kept {
				p, err := ginToMux(k)
				require.NoError(t, err)
				assert.Equal(t, p, served["GET "+k], "kept route %s", k)
			}
			folded := 0
			for key, p := range served {
				if strings.Contains(p, "{p4}") {
					folded++
					_, ok := gen[p]
					assert.True(t, ok, "%s folded into an unexpected pattern %s", key, p)
				}
			}
			assert.Equal(t, len(gen["/api/v1/operations/{p4}/{p5}"])+len(gen["/api/me/item/{p4}/{p5}"]), folded)

			// Go's overlap examples: both patterns of each pair match them.
			overlaps := []string{"/api/v1/operations/v2/activity", "/api/v1/operations/v2/changes",
				"/api/v1/operations/v2/result", "/api/v1/operations/v2/results"}
			if abs {
				overlaps = append(overlaps, "/api/me/item/listening-sessions/remove-from-continue-listening")
			}
			for _, p := range overlaps {
				w := probe(h, http.MethodGet, p, "")
				assert.NotEqual(t, legacyNotFoundBody, w.Body.String(), "%s: the mux pattern does not hold gin's route", p)
				assert.Equal(t, http.StatusUnauthorized, w.Code, "%s: gin's chain should run (and ask for a credential)", p)
			}
		})
	}
}

// ginToMux is the test's own copy of §4.8's translation.
func ginToMux(gin string) (string, error) {
	segs := strings.Split(gin, "/")
	for i, s := range segs {
		switch {
		case strings.HasPrefix(s, ":"):
			segs[i] = "{" + s[1:] + "}"
		case strings.HasPrefix(s, "*"):
			segs[i] = "{" + s[1:] + "...}"
		case strings.ContainsAny(s, "{}"):
			return "", fmt.Errorf("%q", gin)
		}
	}
	return strings.Join(segs, "/"), nil
}

// TestLegacyMount_GeneralisedPatternAcceptsAnyMember: over books' gin engine,
// each member of a generalised pattern reaches its gin chain, a path the
// pattern matches and no member does gets 404 in the legacy shape with no
// handler run, and the kept route wins its overlap.
func TestLegacyMount_GeneralisedPatternAcceptsAnyMember(t *testing.T) {
	f := legacyEngine(t, true, false)
	_, h := booksMount(t, f)
	gen, kept := generalised(true)
	for _, members := range gen {
		for _, m := range members {
			w := probe(h, http.MethodGet, legacyConcretePath(m), "")
			assert.Equal(t, http.StatusUnauthorized, w.Code, "member %s: %s", m, w.Body.String())
			assert.NotEqual(t, legacyNotFoundBody, w.Body.String(), "member %s", m)
		}
	}
	for _, p := range []string{"/api/v1/operations/riddler/clues", "/api/me/item/riddler/clues"} {
		w := probe(h, http.MethodGet, p, "")
		assert.Equal(t, http.StatusNotFound, w.Code, p)
		assert.Equal(t, legacyNotFoundBody, w.Body.String(), p)
	}
	for _, k := range kept {
		w := probe(h, http.MethodGet, legacyConcretePath(k), "")
		assert.Equal(t, http.StatusUnauthorized, w.Code, "kept %s: %s", k, w.Body.String())
	}
	// The kept route wins the overlap: GetOperationV2's chain (s.perm) runs.
	sess, err := f.store.CreateSessionWithOrigin(f.adminID, "127.0.0.1", "test", time.Hour, database.SessionOriginPassword, 0)
	require.NoError(t, err)
	w := probe(h, http.MethodGet, "/api/v1/operations/v2/activity", sess.ID)
	assert.NotEqual(t, legacyNotFoundBody, w.Body.String())
	assert.NotEqual(t, http.StatusUnauthorized, w.Code)
}

// TestLegacyMount_MuxAndGinAgreeOnEncodedSlash: a request with %2F in a
// segment whose gin match (on the decoded path) differs from its mux pattern
// (on the escaped path) gets 404 in the legacy shape and no gin handler
// runs. Every pair of manifest routes A = .../:p and B = A + "/<literal>"
// is exercised, which reaches every route group of the engine.
func TestLegacyMount_MuxAndGinAgreeOnEncodedSlash(t *testing.T) {
	f := legacyEngine(t, true, false)
	_, h := booksMount(t, f)
	manifest, err := f.srv.LegacyManifest()
	require.NoError(t, err)
	have := map[string]bool{}
	for _, lr := range manifest {
		have[lr.Method+" "+lr.GinPath] = true
	}
	pairs := 0
	for _, lr := range manifest {
		dir, last := lr.GinPath[:strings.LastIndexByte(lr.GinPath, '/')], lr.GinPath[strings.LastIndexByte(lr.GinPath, '/')+1:]
		parentLast := dir[strings.LastIndexByte(dir, '/')+1:]
		if strings.ContainsAny(last, ":*") || !strings.HasPrefix(parentLast, ":") || !have[lr.Method+" "+dir] {
			continue
		}
		pairs++
		// The mux sees dir's pattern (one escaped segment); gin decodes the
		// path and matches lr instead.
		p := legacyConcretePath(dir) + "%2F" + last
		w := probe(h, lr.Method, p, "")
		assert.Equal(t, http.StatusNotFound, w.Code, "%s %s", lr.Method, p)
		assert.Equal(t, legacyNotFoundBody, w.Body.String(), "%s %s", lr.Method, p)
	}
	assert.Greater(t, pairs, 20, "too few encoded-slash pairs found; the scan is broken")
}

// TestLegacyMount_UnmatchedMethodMatchesGin: a request gin answers from its
// NoRoute chain (no route for that method and path) gets the same status,
// Content-Type and body through the mount. HEAD's gin has
// HandleMethodNotAllowed off, so a wrong method on a manifest path is gin's
// 404, never core's problem 405; GET /login, whose only route is POST, is the
// SPA's page (06 §6.10 TestReservedSegmentsDisjointFromNonSPA, request half).
// Every manifest path is tried with every method gin has no route for.
// Headers are not compared here (TestPerimeter_ParityWithHEAD does). OPTIONS
// is answered by core's stage 6 before routing since R1-17, so its reference
// is gin behind HEAD's frozen perimeter (headPerimeter), whose CORS
// middleware answered it.
func TestLegacyMount_UnmatchedMethodMatchesGin(t *testing.T) {
	for _, abs := range absModes() {
		t.Run(absName(abs), func(t *testing.T) {
			f := legacyEngine(t, abs, false)
			_, h := booksMount(t, f)
			manifest, err := f.srv.LegacyManifest()
			require.NoError(t, err)
			routed := map[string]bool{}
			var paths []string
			for _, lr := range manifest {
				p := legacyConcretePath(lr.GinPath)
				if !slices.Contains(paths, p) {
					paths = append(paths, p)
				}
				routed[lr.Method+" "+p] = true
			}
			methods := []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions}
			n := 0
			for _, p := range paths {
				for _, m := range methods {
					if routed[m+" "+p] {
						continue
					}
					n++
					ref := http.Handler(f.srv.router)
					if m == http.MethodOptions {
						ref = headPerimeter(f.srv.router)
					}
					g, mt := probe(ref, m, p, ""), probe(h, m, p, "")
					assert.Equal(t, g.Code, mt.Code, "%s %s", m, p)
					assert.Equal(t, g.Header().Get("Content-Type"), mt.Header().Get("Content-Type"), "%s %s", m, p)
					assert.Equal(t, g.Body.String(), mt.Body.String(), "%s %s", m, p)
				}
			}
			assert.Greater(t, n, 1000, "too few unrouted method/path pairs; the scan is broken")
			// The SPA-shared segment: gin serves GET /login from NoRoute.
			assert.Equal(t, probe(f.srv.router, http.MethodGet, "/login", "").Code, probe(h, http.MethodGet, "/login", "").Code)
		})
	}
}

// notARouteHandler reports a response no gin route handler wrote: the
// post-match check's legacy 404, core's problem 404 for an unclean /api/
// path, or gin's NoRoute chain (the legacy 404, or the SPA page for a GET).
func notARouteHandler(w *httptest.ResponseRecorder) bool {
	switch {
	case w.Code == http.StatusNotFound && w.Body.String() == legacyNotFoundBody:
		return true
	case w.Code == http.StatusNotFound && w.Header().Get("Content-Type") == "application/problem+json":
		return true
	case w.Code == http.StatusOK && strings.HasPrefix(w.Header().Get("Content-Type"), "text/html"):
		return true
	}
	return false
}

// TestLegacyMount_FallbackNeverRunsARouteHandler is review B1 on the real
// engine: the "/" fallback may reach gin's NoRoute chain and GET / only
// (05-http-api §8.2). Every parameterised manifest route is sent through the
// mount with each parameter "." and "..", an unclean path the mux never
// matches and gin would route; plus an encoded slash on the SPAShared
// segment. None may reach a route handler. On gin alone
// /public/session/../track/1 runs PublicSessionTrack with session "..";
// through the mount it is a 404 (a parity change 05-http-api §8.2 asks for).
func TestLegacyMount_FallbackNeverRunsARouteHandler(t *testing.T) {
	for _, abs := range absModes() {
		t.Run(absName(abs), func(t *testing.T) {
			f := legacyEngine(t, abs, false)
			_, h := booksMount(t, f)
			manifest, err := f.srv.LegacyManifest()
			require.NoError(t, err)
			n := 0
			for _, lr := range manifest {
				if !strings.ContainsAny(lr.GinPath, ":*") {
					continue
				}
				for _, v := range []string{".", ".."} {
					segs := strings.Split(lr.GinPath, "/")
					for i, sg := range segs {
						if strings.HasPrefix(sg, ":") || strings.HasPrefix(sg, "*") {
							segs[i] = v
						}
					}
					p := strings.Join(segs, "/")
					n++
					w := probe(h, lr.Method, p, "")
					assert.True(t, notARouteHandler(w), "%s %s (%s) reached a handler: %d %q %.120q",
						lr.Method, p, lr.GinPath, w.Code, w.Header().Get("Content-Type"), w.Body.String())
				}
			}
			assert.Greater(t, n, 300, "too few parameterised routes; the scan is broken")
			// Books has no GET route under /login, so gin answers these from
			// its NoRoute chain (the Cloudflare-recovery redirect); the mount
			// must give that answer, not a handler's.
			for _, p := range []string{"/login/joker%2Friddler", "/login%2Fjoker"} {
				g, mt := probe(f.srv.router, http.MethodGet, p, ""), probe(h, http.MethodGet, p, "")
				assert.Equal(t, g.Code, mt.Code, "GET %s", p)
				assert.Equal(t, g.Header().Get("Location"), mt.Header().Get("Location"), "GET %s", p)
				assert.Equal(t, g.Body.String(), mt.Body.String(), "GET %s", p)
			}
			if abs {
				for _, p := range []string{"/public/session/../track/1", "/public%2Fsession%2Fx%2Ftrack%2F1"} {
					g, mt := probe(f.srv.router, http.MethodGet, p, ""), probe(h, http.MethodGet, p, "")
					assert.False(t, notARouteHandler(g), "gin alone no longer routes %s; the hole this test guards is gone from the fixture", p)
					assert.Equal(t, http.StatusNotFound, mt.Code, p)
					assert.Equal(t, legacyNotFoundBody, mt.Body.String(), p)
				}
			}
		})
	}
}

// TestNativePrefixesNotRedirected: paths reserved for versioned APIs are not
// 301-redirected into /api/v1 (09 §9.2.4), by gin or by the mount.
func TestNativePrefixesNotRedirected(t *testing.T) {
	f := legacyEngine(t, false, false)
	_, h := booksMount(t, f)
	for _, p := range []string{"/api/core/v1/x", "/api/hello/v1/x", "/api/books/v1/x", "/api/core/v1", "/api/hello/v12/x"} {
		_, redirected := legacyAPIRedirect(http.MethodGet, p)
		assert.False(t, redirected, p)
		for name, srv := range map[string]http.Handler{"gin": f.srv.router, "mount": h} {
			w := probe(srv, http.MethodGet, p, "")
			assert.NotEqual(t, http.StatusMovedPermanently, w.Code, "%s %s: %s", name, p, w.Header().Get("Location"))
			assert.Equal(t, http.StatusNotFound, w.Code, "%s %s", name, p)
		}
	}
}

// TestCollidingNamespacesStillRedirect_VersionedLookalikes is 09 §9.4 R1's
// TestCollidingNamespacesStillRedirect (that name already belongs to the ABS
// collision test in wire_abs_routes_test.go, which keeps asserting the ABS
// half): a path that only looks versioned still redirects exactly as HEAD
// does, and HEAD's exclusions behave unchanged. Behind the mount a path under
// a native claim (/api/core/) is the claim owner's and is never redirected.
func TestCollidingNamespacesStillRedirect_VersionedLookalikes(t *testing.T) {
	f := legacyEngine(t, false, false)
	_, h := booksMount(t, f)
	for _, p := range []string{"/api/v1x/joker", "/api/A/v1/joker", "/api/toolongnamespacexx/v1/joker",
		"/api/core/vx/joker", "/api/core/v/joker", "/api/hello/1/joker", "/api/hello-x/v1/joker"} {
		want := strings.Replace(p, "/api/", "/api/v1/", 1)
		loc, ok := legacyAPIRedirect(http.MethodGet, p)
		assert.True(t, ok, p)
		assert.Equal(t, want, loc, p)
		for name, srv := range map[string]http.Handler{"gin": f.srv.router, "mount": h} {
			w := probe(srv, http.MethodGet, p, "")
			if name == "mount" && strings.HasPrefix(p, "/api/core/") {
				// Behind the mount core's claim on /api/core/ is matched before
				// the redirect, so no core path is ever redirected into /api/v1
				// (05-http-api §4.9 item 3): core answers its own problem 404.
				assert.Equal(t, http.StatusNotFound, w.Code, "%s %s", name, p)
				assert.Equal(t, "application/problem+json", w.Header().Get("Content-Type"), "%s %s", name, p)
				continue
			}
			assert.Equal(t, http.StatusMovedPermanently, w.Code, "%s %s", name, p)
			assert.Equal(t, want, w.Header().Get("Location"), "%s %s", name, p)
		}
	}
	// HEAD's exclusions: still not redirected.
	for _, p := range []string{"/api/v1/audiobooks", "/api/v3/ops/defs", "/api/health", "/api/events", "/api/metrics", "/api/me"} {
		_, ok := legacyAPIRedirect(http.MethodGet, p)
		assert.False(t, ok, p)
	}
	// And the plain unversioned form still redirects, through the mount too.
	w := probe(h, http.MethodGet, "/api/audiobooks", "")
	assert.Equal(t, http.StatusMovedPermanently, w.Code)
	assert.Equal(t, "/api/v1/audiobooks", w.Header().Get("Location"))
}

// goldenRoutes compares lines with testdata/<name> below its four-line
// version header; UPDATE_GOLDEN=1 rewrites the body and keeps the header
// (bump its version by hand), or writes a fresh one with guid.
func goldenRoutes(t *testing.T, name, guid string, lines []string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	body := strings.Join(lines, "\n") + "\n"
	split := func(b string) (header, rest string) {
		parts := strings.SplitAfterN(b, "\n", 5)
		if len(parts) == 5 && strings.HasPrefix(parts[0], "# file: ") && strings.HasPrefix(parts[3], "# last-edited: ") {
			return strings.Join(parts[:4], ""), parts[4]
		}
		return "", b
	}
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		header := "# file: internal/server/testdata/" + name + "\n# version: 1.0.0\n# guid: " + guid + "\n# last-edited: " +
			time.Now().Format(time.DateOnly) + "\n"
		if old, err := os.ReadFile(path); err == nil {
			if h, _ := split(string(old)); h != "" {
				header = h
			}
		}
		require.NoError(t, os.WriteFile(path, []byte(header+body), 0o644))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "missing golden; regenerate with UPDATE_GOLDEN=1 and review it")
	header, rest := split(string(want))
	require.NotEmpty(t, header, "%s has no version header", path)
	assert.Equal(t, rest, body, "%s changed: review the difference and regenerate with UPDATE_GOLDEN=1", path)
}

// TestPublicRoutesGolden is the reviewed set of every Public legacy route,
// ABS off (HEAD's 15, raw/09 §2.4) and on (plus the ABS surface's routes
// that run no ABSRequireAuth). The native half is empty until a module
// lists a public route.
func TestPublicRoutesGolden(t *testing.T) {
	var lines []string
	for _, abs := range absModes() {
		lines = append(lines, "# "+absName(abs))
		for _, lr := range legacyManifest(abs) {
			if lr.Public {
				lines = append(lines, lr.Method+" "+lr.GinPath)
			}
		}
	}
	goldenRoutes(t, "public_routes.golden", "1d4d1106-e43f-4e97-b110-00992aeec352", lines)
}

// TestOwnAuthRoutesGolden is the reviewed set of OwnAuth legacy routes and
// their auth method (SEC2-17): exactly the ABS routes whose chain runs
// ABSRequireAuth, none while ABS is off.
func TestOwnAuthRoutesGolden(t *testing.T) {
	for _, lr := range legacyManifest(false) {
		assert.False(t, lr.OwnAuth, "%s %s is OwnAuth with ABS off", lr.Method, lr.GinPath)
	}
	var lines []string
	for _, lr := range legacyManifest(true) {
		if lr.OwnAuth {
			lines = append(lines, lr.Method+" "+lr.GinPath+" "+lr.AuthMethod)
		}
	}
	goldenRoutes(t, "own_auth_routes.golden", "b304ad2a-94d4-454f-84c4-4c6d6a4c6bae", lines)
}

// TestLegacyManifest_DriftFailsBoot: a gin route with no row, and a row with
// no gin route, are ErrLegacyManifest.
func TestLegacyManifest_DriftFailsBoot(t *testing.T) {
	f := legacyEngine(t, false, false)
	walk := f.srv.router.Routes()
	m := legacyManifest(false)
	require.NoError(t, checkLegacyManifest(walk, m))
	err := checkLegacyManifest(walk, m[1:])
	require.True(t, errors.Is(err, httpapi.ErrLegacyManifest), "%v", err)
	assert.Contains(t, err.Error(), m[0].Method+" "+m[0].GinPath)
	extra := append(slices.Clone(m), httpapi.LegacyRoute{Method: http.MethodGet, GinPath: "/api/v1/batcave"})
	err = checkLegacyManifest(walk, extra)
	require.True(t, errors.Is(err, httpapi.ErrLegacyManifest), "%v", err)
	assert.Contains(t, err.Error(), "GET /api/v1/batcave")
	// Only GET / and HEAD / are the "/" owner's: a gin route with any other
	// method on "/" needs a manifest row (review M2).
	withPost := append(slices.Clone(walk), gin.RouteInfo{Method: http.MethodPost, Path: "/"})
	err = checkLegacyManifest(withPost, m)
	require.True(t, errors.Is(err, httpapi.ErrLegacyManifest), "POST / skipped by the drift check: %v", err)
	assert.Contains(t, err.Error(), "POST /")
	withHead := append(slices.Clone(walk), gin.RouteInfo{Method: http.MethodHead, Path: "/"})
	require.NoError(t, checkLegacyManifest(withHead, m))
}
