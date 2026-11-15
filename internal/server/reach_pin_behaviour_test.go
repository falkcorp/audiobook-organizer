// file: internal/server/reach_pin_behaviour_test.go
// version: 1.0.1
// guid: 5d0c2b8e-9b1f-4c63-a7e4-2f8d6a1c3b90
// last-edited: 2026-11-15

package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/usermerge"
)

// The reach oracle (internal/books/cutover, F4-07) hides 11 cells behind
// `reachHandlerAdmin`: `GET /users/:id/footprint` and `GET /users/merges` for
// five principals and `PUT /users/:id/email` for one, because the route's
// `users.manage` admits them and the HANDLER's own admin test refuses them.
// `reach_pin_source_test.go` pins the SOURCE of those handlers with go/ast.
// This file pins their BEHAVIOUR through the router the server really
// registers, so a source change the AST shapes do not describe (a refusal that
// answers 403 and then goes on to fill the body, a middleware added to the
// group or inline at registration) still fails here, by name.
//
// F4-08 ports these handlers to the registered path and must refuse each of the
// 11 cells again there (seams.d/F4-07.md); these are the tests to port.

// reachPinnedRoute is one route the oracle's pin hides.
type reachPinnedRoute struct {
	name    string
	method  string
	pattern string // the gin full path, as Routes() lists it
	path    func(f *credGuardFixture) string
	body    any
	// refusal is the message the handler's own admin test answers with.
	refusal string
	// keyRefusal, when set, is what an API-key principal gets instead: the
	// route sits behind RequireCredentialChangeMethod, which refuses every key
	// before the handler runs (so the oracle hides only the session cell of
	// `PUT /users/:id/email`).
	keyRefusal string
	// chain is the ordered middleware and handler names the router builds for
	// the route, normalized by reachChainName. The route guard is the one
	// `s.perm("users.manage")` link, RequirePermission.
	chain []string
	// data returns values the route's success answer carries and a refusal must
	// not: a body that held one under a 403 would be a leak whatever its status.
	data func(f *credGuardFixture) []string
}

// reachMergeJournalID is the merge journal the fixtures store, so the merges
// list has a row whose values a leaking refusal would carry.
const reachMergeJournalID = "merge-batman-0001"

// reachSharedChain is every route's link set up to RequireAuth: the perimeter
// stages and the global middleware.
var reachSharedChain = []string{
	"server.perimeterSeen.func*",
	"middleware.BasicAuth.func*",
	"server.perimeterGatePassed.func*",
	"server.captureRawResponseController.func*",
	"server.legacyMatchCheck.func*",
	"server.(*Server).setupRoutes.func*",
	"middleware.MaxRequestBodySize.func*",
	"middleware.RequireAuth.func*",
}

func reachPinnedRoutes() []reachPinnedRoute {
	chain := func(extra ...string) []string {
		out := append([]string{}, reachSharedChain...)
		return append(out, extra...)
	}
	return []reachPinnedRoute{
		{
			name: "footprint", method: http.MethodGet, pattern: "/api/v1/users/:id/footprint",
			path:    func(f *credGuardFixture) string { return "/api/v1/users/" + f.targetID + "/footprint" },
			refusal: "merging accounts needs the admin role",
			chain: chain("middleware.RequirePermission.func*",
				"handlers.(*UserHandler).UserFootprint-fm"),
			data: func(f *credGuardFixture) []string {
				return []string{f.targetID, "synthetic-target", "target@example.test", `"api_keys":1`}
			},
		},
		{
			name: "merges", method: http.MethodGet, pattern: "/api/v1/users/merges",
			path:    func(*credGuardFixture) string { return "/api/v1/users/merges" },
			refusal: "merging accounts needs the admin role",
			chain: chain("middleware.RequirePermission.func*",
				"handlers.(*UserHandler).ListUserMerges-fm"),
			data: func(f *credGuardFixture) []string {
				return []string{reachMergeJournalID, f.targetID, f.adminID}
			},
		},
		{
			name: "email", method: http.MethodPut, pattern: "/api/v1/users/:id/email",
			path:       func(f *credGuardFixture) string { return "/api/v1/users/" + f.targetID + "/email" },
			body:       map[string]any{"email": "robin@example.invalid"},
			refusal:    "setting another user's email needs the admin role",
			keyRefusal: "API keys cannot change passwords, users, roles, invites, sessions, keys of other users, sign-in settings, executables, database locations or server paths; sign in to do this",
			chain: chain("middleware.RequireCredentialChangeMethod.func*",
				"middleware.RequirePermission.func*",
				"handlers.(*UserHandler).SetUserEmail-fm"),
			data: func(f *credGuardFixture) []string {
				return []string{f.targetID, "synthetic-target", "robin@example.invalid"}
			},
		},
	}
}

// reachChainSuffix matches the numbering of a function literal's name
// (`.func1`, `.func1.2`), which moves whenever a literal is added above it in
// the file and says nothing about the link.
var reachChainSuffix = regexp.MustCompile(`\.func\d+(\.\d+)*`)

// reachChainName is a handler's identity as the chain pin spells it: the
// package-qualified function name without the module path, its literal
// numbering folded to `.func*`.
func reachChainName(h gin.HandlerFunc) string {
	name := runtime.FuncForPC(reflect.ValueOf(h).Pointer()).Name()
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	return reachChainSuffix.ReplaceAllString(name, ".func*")
}

// ginRouteChains returns the ordered handler names of every route the engine
// registered, keyed "METHOD /full/path". gin keeps the chain only in its
// unexported radix trees (`Routes()` lists one handler per route, the last),
// so the walk reads them by reflection; TestGinRouteChains_ReadsGroupAndInline
// pins the walk to gin's real behaviour, and the route count is checked against
// `engine.Routes()` so a gin change that moves the fields fails loudly.
func ginRouteChains(t *testing.T, engine *gin.Engine) map[string][]string {
	t.Helper()
	read := func(v reflect.Value, field string) reflect.Value {
		f := v.FieldByName(field)
		require.True(t, f.IsValid(), "gin no longer has the field %s; the chain walk must be redone", field)
		return reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem()
	}
	out := map[string][]string{}
	var walk func(method string, n reflect.Value)
	walk = func(method string, n reflect.Value) {
		if n.IsNil() {
			return
		}
		node := n.Elem()
		handlers := read(node, "handlers").Interface().(gin.HandlersChain)
		if len(handlers) > 0 {
			full := read(node, "fullPath").String()
			names := make([]string, 0, len(handlers))
			for _, h := range handlers {
				names = append(names, reachChainName(h))
			}
			out[method+" "+full] = names
		}
		children := read(node, "children")
		for i := 0; i < children.Len(); i++ {
			walk(method, children.Index(i))
		}
	}
	trees := read(reflect.ValueOf(engine).Elem(), "trees")
	for i := 0; i < trees.Len(); i++ {
		tree := trees.Index(i)
		walk(read(tree, "method").String(), read(tree, "root"))
	}
	require.Len(t, out, len(engine.Routes()), "the walk must find exactly the routes the engine lists")
	return out
}

func reachGroupMW() gin.HandlerFunc  { return func(*gin.Context) {} }
func reachInlineMW() gin.HandlerFunc { return func(*gin.Context) {} }
func reachLeafHandler(*gin.Context)  {}

// TestGinRouteChains_ReadsGroupAndInline pins the chain walk to what gin does
// with `Group.Use` and a middleware passed at registration, the two ways the
// review's A3 and A3b mutants add a link.
func TestGinRouteChains_ReadsGroupAndInline(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/g")
	g.Use(reachGroupMW())
	g.GET("/plain", reachLeafHandler)
	g.GET("/inline", reachInlineMW(), reachLeafHandler)
	g.GET("/:id/deep", reachLeafHandler)

	got := ginRouteChains(t, r)
	assert.Equal(t, []string{"server.reachGroupMW.func*", "server.reachLeafHandler"}, got["GET /g/plain"])
	assert.Equal(t, []string{"server.reachGroupMW.func*", "server.reachInlineMW.func*", "server.reachLeafHandler"}, got["GET /g/inline"],
		"a middleware passed at registration is a link of that route's chain only")
	assert.Equal(t, []string{"server.reachGroupMW.func*", "server.reachLeafHandler"}, got["GET /g/:id/deep"])
	assert.Len(t, got, 3)

	// A link added to the group after the fact is a link of every route in it.
	r2 := gin.New()
	g2 := r2.Group("/g")
	g2.GET("/plain", reachLeafHandler)
	g2.Use(reachGroupMW())
	g2.GET("/later", reachLeafHandler)
	got2 := ginRouteChains(t, r2)
	assert.Equal(t, []string{"server.reachLeafHandler"}, got2["GET /g/plain"], "a Use after registration does not reach the earlier route, as in gin")
	assert.Equal(t, []string{"server.reachGroupMW.func*", "server.reachLeafHandler"}, got2["GET /g/later"])
}

// TestReachPin_ChainsAreTheOnesTheOraclePins pins the ordered link names of the
// three hidden routes from the router the server builds. An added link (a
// middleware inline at registration, a `users.Use(...)` on the group) or a
// removed one (the route guard, the credential-change method check) changes the
// list and fails here with the route and the two lists.
func TestReachPin_ChainsAreTheOnesTheOraclePins(t *testing.T) {
	f := setupCredGuardServer(t)
	chains := ginRouteChains(t, f.srv.router)
	for _, rt := range reachPinnedRoutes() {
		t.Run(rt.name, func(t *testing.T) {
			got, ok := chains[rt.method+" "+rt.pattern]
			require.True(t, ok, "the router has no route %s %s", rt.method, rt.pattern)
			assert.Equal(t, rt.chain, got,
				"the ordered middleware and handler chain of %s %s changed; the oracle's pin (reachHandlerAdmin) rests on it", rt.method, rt.pattern)
		})
	}
}

// reachRefusedPrincipal is a principal the route's `users.manage` guard admits
// and the handler's admin test refuses.
type reachRefusedPrincipal struct {
	name   string
	isKey  bool
	bearer func(t *testing.T, f *credGuardFixture, customAllID string) string
}

func reachKeyFor(t *testing.T, f *credGuardFixture, userID, name string, scopes []string) string {
	t.Helper()
	raw, hash, err := database.GenerateAPIKeyToken()
	require.NoError(t, err)
	exp := time.Now().Add(time.Hour)
	_, err = f.store.CreateAPIKey(&database.APIKey{
		UserID: userID, Name: name, TokenHash: hash, Scopes: scopes,
		Status: "active", CreatedAt: time.Now(), ExpiresAt: &exp,
	})
	require.NoError(t, err)
	return raw
}

// reachCustomAllUser is a user whose custom role holds every atom and is not
// the admin role: it passes every route guard and fails the handler's role test.
func reachCustomAllUser(t *testing.T, f *credGuardFixture) string {
	t.Helper()
	perms := make([]string, 0, len(auth.All()))
	for _, p := range auth.All() {
		perms = append(perms, string(p))
	}
	role, err := f.store.CreateRole(&database.Role{ID: "synthetic-custom-all", Name: "synthetic-custom-all", Permissions: perms})
	require.NoError(t, err)
	u, err := f.store.CreateUser("synthetic-custom-all", "custom-all@example.invalid", "bcrypt", "x", []string{role.ID}, "active")
	require.NoError(t, err)
	return u.ID
}

func reachRefusedPrincipals() []reachRefusedPrincipal {
	return []reachRefusedPrincipal{
		{"session:custom-all", false, func(t *testing.T, f *credGuardFixture, customAllID string) string {
			return f.sessionFor(t, customAllID)
		}},
		{"key:custom-all:every", true, func(t *testing.T, f *credGuardFixture, customAllID string) string {
			var all []string
			for _, p := range auth.All() {
				all = append(all, string(p))
			}
			return reachKeyFor(t, f, customAllID, "synthetic every", all)
		}},
		{"key:custom-all:empty", true, func(t *testing.T, f *credGuardFixture, customAllID string) string {
			return reachKeyFor(t, f, customAllID, "synthetic empty", nil)
		}},
		{"key:admin:users", true, func(t *testing.T, f *credGuardFixture, _ string) string {
			return reachKeyFor(t, f, f.adminID, "synthetic users only", []string{string(auth.PermUsersManage)})
		}},
		{"session:exactly-users.manage", false, func(t *testing.T, f *credGuardFixture, _ string) string {
			return f.exactPermGoneSession(t, "umg", string(auth.PermUsersManage))
		}},
	}
}

// reachSeedMergeJournal stores a merge journal so the merges list carries a row.
func reachSeedMergeJournal(t *testing.T, f *credGuardFixture) {
	t.Helper()
	now := time.Now()
	j := usermerge.Journal{
		ID: reachMergeJournalID, Status: usermerge.StatusApplied,
		SurvivorID: f.adminID, AbsorbedID: f.targetID, ActorID: f.adminID,
		CreatedAt: now, UpdatedAt: now,
	}
	raw, err := json.Marshal(j)
	require.NoError(t, err)
	require.NoError(t, f.store.SetRaw(database.UserMergeJournalPrefix+j.ID, raw))
}

// reachRefusalBody is the plain refusal the handler answers with: nothing but
// the message, the code and the status.
func reachRefusalBody(message string) string {
	b, _ := json.Marshal(map[string]any{"error": message, "code": "FORBIDDEN", "status": http.StatusForbidden})
	return string(b)
}

// TestReachPin_RefusedPrincipalsGetOnlyTheRefusal sends each hidden route the
// principals the pin hides, through the real router, and requires the whole
// body to be the handler's plain refusal. A refusal that answers 403 and then
// writes the route's data (the review's A2: `mergeActor` returning
// `"", true` after the refusal) keeps the status and fails the body check, and
// the data values are checked by name so the failure says what leaked.
func TestReachPin_RefusedPrincipalsGetOnlyTheRefusal(t *testing.T) {
	f := setupCredGuardServer(t)
	reachSeedMergeJournal(t, f)

	// Control: the same requests from the admin session succeed and carry the
	// values, so "none of the values" below cannot hold only because the
	// fixture has none (an unwired merge service answers 503 and fails here).
	for _, rt := range reachPinnedRoutes() {
		w := f.do(t, f.sessionToken, rt.method, rt.path(f), rt.body)
		require.Equal(t, http.StatusOK, w.Code, "control: the admin session must be served %s: %s", rt.name, w.Body.String())
		for _, v := range rt.data(f) {
			require.Contains(t, w.Body.String(), v, "control: the admin answer for %s must carry %q or the leak check is vacuous", rt.name, v)
		}
	}
	// The email control changed the stored address; put it back for the checks.
	require.NoError(t, resetTargetEmail(f))

	customAllID := reachCustomAllUser(t, f)
	for _, p := range reachRefusedPrincipals() {
		bearer := p.bearer(t, f, customAllID)
		for _, rt := range reachPinnedRoutes() {
			t.Run(p.name+"/"+rt.name, func(t *testing.T) {
				w := f.do(t, bearer, rt.method, rt.path(f), rt.body)
				assert.Equal(t, http.StatusForbidden, w.Code, "body: %s", w.Body.String())
				want := rt.refusal
				if p.isKey && rt.keyRefusal != "" {
					want = rt.keyRefusal
				}
				assert.JSONEq(t, reachRefusalBody(want), w.Body.String(),
					"a refused principal must get exactly the plain refusal and nothing else")
				for _, v := range rt.data(f) {
					assert.NotContains(t, w.Body.String(), v, "the refusal of %s leaked %q", rt.name, v)
				}
			})
		}
	}

	target, err := f.store.GetUserByID(f.targetID)
	require.NoError(t, err)
	assert.Equal(t, "target@example.test", target.Email, "a refused SetUserEmail must not have changed the address")
}

// TestReachPin_AuthDisabledPrincipalGetsOnlyTheRefusal is the same check for the
// fifth hidden principal, the auth-disabled one, which sends no credential.
func TestReachPin_AuthDisabledPrincipalGetsOnlyTheRefusal(t *testing.T) {
	f := setupCredGuardServerWith(t, func(c *config.Config) { c.EnableAuth = false })
	reachSeedMergeJournal(t, f)
	for _, rt := range reachPinnedRoutes() {
		t.Run(rt.name, func(t *testing.T) {
			w := f.doAnonymous(t, rt.method, rt.path(f), rt.body)
			assert.Equal(t, http.StatusForbidden, w.Code, "body: %s", w.Body.String())
			assert.JSONEq(t, reachRefusalBody(rt.refusal), w.Body.String())
			for _, v := range rt.data(f) {
				assert.NotContains(t, w.Body.String(), v, "the refusal of %s leaked %q", rt.name, v)
			}
		})
	}
	target, err := f.store.GetUserByID(f.targetID)
	require.NoError(t, err)
	assert.Equal(t, "target@example.test", target.Email)
}

// resetTargetEmail puts the fixture target's address back after the email
// control changed it.
func resetTargetEmail(f *credGuardFixture) error {
	_, err := f.store.SetUserEmailAsAdmin(f.targetID, "target@example.test")
	if err != nil {
		return fmt.Errorf("reset target email: %w", err)
	}
	return nil
}

// TestReachPin_RoutesAreAllInTheRouter keeps the table in step with the router:
// every route it names exists and the table covers the three the oracle's pin hides.
func TestReachPin_RoutesAreAllInTheRouter(t *testing.T) {
	f := setupCredGuardServer(t)
	have := map[string]bool{}
	for _, r := range f.srv.router.Routes() {
		have[r.Method+" "+r.Path] = true
	}
	var names []string
	for _, rt := range reachPinnedRoutes() {
		assert.True(t, have[rt.method+" "+rt.pattern], "no route %s %s", rt.method, rt.pattern)
		names = append(names, rt.method+" "+rt.pattern)
	}
	sort.Strings(names)
	assert.Equal(t, []string{
		"GET /api/v1/users/:id/footprint",
		"GET /api/v1/users/merges",
		"PUT /api/v1/users/:id/email",
	}, names)
}
