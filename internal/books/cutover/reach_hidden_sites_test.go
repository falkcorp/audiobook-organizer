// file: internal/books/cutover/reach_hidden_sites_test.go
// version: 1.0.0
// guid: 4ce7e278-2e21-401d-9c76-8ca830184e78
// last-edited: 2026-11-15

package cutover

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// The F4-07-HIDDEN-HANDLER-CHECKS bar is the routes of the in-handler permission
// check sites the row is named for. The recording shows the router's answer on a
// route whose handler turns the probe's dummy id (or empty body) into a 404, a 400
// or a plain 200 before its own check, so the check is never seen. Those sites are
// not recorded cells and no recorded status finds them (four of them sit on routes
// that never answered 404), so they are listed here by hand, as (file, function,
// routes), and held to the source with go/ast:
//
//   - each listed function must exist and still contain a permission check
//     (reachIsPermissionCall), so a renamed or deleted site fails;
//   - every function in the handler packages that contains such a check must be a
//     listed site or a named exclusion with its reason, so a site added to the
//     handlers fails until it is listed with its routes;
//   - every listed route must be a recorded route (or an owner route, which the
//     owner model computes), so a typo cannot make a route vanish from the bar.
//
// A discharge cannot clear the row without covering every site's routes.

// reachHiddenSite is one in-handler permission check site and the routes that reach it.
type reachHiddenSite struct {
	file   string // path from this package's directory
	recv   string // receiver type name, "" for a plain function
	name   string
	routes []string
	// ownerRoutes: the site guards the owner routes, which the owner model computes
	// (reachOwnerRoutes), so they are not typed here.
	ownerRoutes bool
}

func (s reachHiddenSite) id() string { return reachFuncID(s.file, s.recv, s.name) }

func reachFuncID(file, recv, name string) string {
	if recv != "" {
		return file + " " + recv + "." + name
	}
	return file + " " + name
}

const (
	reachSrvDir      = "../../server"
	reachHandlersDir = "../../server/handlers"
	reachCfaccessDir = "../../cfaccess"
)

// reachHiddenSites are the in-handler check sites. Line numbers are not kept: the
// pin finds them by function.
var reachHiddenSites = []reachHiddenSite{
	{file: reachHandlersDir + "/repairs/handler.go", recv: "Handler", name: "OwnerApply", routes: []string{
		"POST /api/v1/repairs/:fixer/owner-apply"}},
	{file: reachHandlersDir + "/apikeys.go", name: "isAdminUser", routes: []string{
		"POST /api/v1/auth/api-keys", "GET /api/v1/auth/api-keys",
		"GET /api/v1/auth/api-keys/:id", "PATCH /api/v1/auth/api-keys/:id", "DELETE /api/v1/auth/api-keys/:id"}},
	{file: reachHandlersDir + "/auth.go", recv: "AuthHandler", name: "ChangePassword", routes: []string{
		"PUT /api/v1/auth/me/password"}},
	{file: reachHandlersDir + "/audiobooks/handler.go", name: "trashProgressViewer", routes: []string{
		"GET /api/v1/audiobooks/soft-deleted", "POST /api/v1/audiobooks/:id/discard-progress-and-purge"}},
	{file: reachHandlersDir + "/audiobooks/handler_permission.go", name: "StrictPermissionHolds", routes: []string{
		"POST /api/v1/audiobooks/batch", "POST /api/v1/audiobooks/batch-operations"}},
	{file: reachHandlersDir + "/operations_v2.go", recv: "OperationsV2Handler", name: "defPermissionsHeld", routes: []string{
		"POST /api/v1/operations/v2", "POST /api/v1/operations/v2/:id/retry", "POST /api/v1/operations/v2/:id/finish"}},
	{file: reachSrvDir + "/wire_repairs_routes.go", recv: "Server", name: "permHoldsLive", routes: []string{
		"GET /api/v1/operations/events", "GET /api/v3/ops/events"}},
	{file: reachSrvDir + "/wire_ops_v3_routes.go", recv: "Server", name: "opsV3Authz", routes: []string{
		"POST /api/v3/ops/defs/:id/runs", "POST /api/v3/ops/runs",
		"POST /api/v3/ops/runs/:id/cancel", "POST /api/v3/ops/runs/:id/approve"}},
	{file: reachSrvDir + "/maintenance_dispatcher.go", recv: "Server", name: "runMaintenanceJob", routes: []string{
		"POST /api/v1/maintenance/jobs/:job_id"}},
	{file: reachCfaccessDir + "/owner_guard.go", name: "RequireOwner", ownerRoutes: true},
	{file: reachHandlersDir + "/user.go", recv: "UserHandler", name: "inviteTokenVisible", routes: []string{
		"GET /api/v1/users/invites"}},
}

// reachHiddenExcluded are functions that hold a permission check and are not sites
// of this row, with the reason.
var reachHiddenExcluded = map[string]string{
	reachFuncID(reachHandlersDir+"/user.go", "UserHandler", "SetUserEmail"):                "pinned route by route by reachHandlerAdminFuncs",
	reachFuncID(reachHandlersDir+"/user_merge.go", "UserHandler", "mergeActor"):            "pinned route by route by reachHandlerAdminFuncs",
	reachFuncID(reachHandlersDir+"/abs/collections.go", "Handler", "canManageCollections"): "the ABS delegated surface, replayed by F4-07-ABS-REPLAY",
	reachFuncID(reachHandlersDir+"/opsv3/handler.go", "", "StrictAuthz"):                   "the default for a handler the server did not wire; the wired Authz is opsV3Authz, a site",
	// Seen once go fix turned its role loop into slices.Contains (the form
	// reachIsPermissionCall matches); the loop form had always escaped the pin.
	reachFuncID(reachSrvDir+"/guest_key_reissue.go", "", "guestKeyFirstKeyBlocked"): "tests the TARGET user's roles (an admin gets no first phone-app key), not the caller's; the caller is gated by the route's ownerCredRoute",
}

// reachIsPermissionCall matches auth.Can(...) and slices.Contains(<x>.Roles, adminRole
// or auth.SeedRoleAdmin): the two shapes an in-handler permission test takes.
func reachIsPermissionCall(c *ast.CallExpr) bool {
	sel, ok := c.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	if pkg.Name == "auth" && sel.Sel.Name == "Can" {
		return true
	}
	if pkg.Name != "slices" || sel.Sel.Name != "Contains" || len(c.Args) != 2 {
		return false
	}
	if roles, ok := c.Args[0].(*ast.SelectorExpr); !ok || roles.Sel.Name != "Roles" {
		return false
	}
	switch a := c.Args[1].(type) {
	case *ast.Ident:
		return a.Name == "adminRole"
	case *ast.SelectorExpr:
		return a.Sel.Name == "SeedRoleAdmin"
	}
	return false
}

// reachFuncsWithPermissionCheck lists every top-level function of src that contains a
// permission check anywhere in its body (a closure counts for its enclosing function).
func reachFuncsWithPermissionCheck(file string, src []byte) ([]string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), file, src, 0)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		found := false
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok && reachIsPermissionCall(c) {
				found = true
			}
			return !found
		})
		if found {
			recv := ""
			if fd.Recv != nil && len(fd.Recv.List) == 1 {
				t := fd.Recv.List[0].Type
				if star, ok := t.(*ast.StarExpr); ok {
					t = star.X
				}
				if id, ok := t.(*ast.Ident); ok {
					recv = id.Name
				}
			}
			out = append(out, reachFuncID(file, recv, fd.Name.Name))
		}
	}
	return out, nil
}

// reachHiddenSiteProblems holds sites to the source files given (path to bytes). It
// is a pure function so the tests can run it on in-memory mutants.
func reachHiddenSiteProblems(sites []reachHiddenSite, excluded map[string]string, files map[string][]byte) []string {
	var out []string
	have := map[string]bool{}
	for file, src := range files {
		ids, err := reachFuncsWithPermissionCheck(file, src)
		if err != nil {
			out = append(out, fmt.Sprintf("%s: %v", file, err))
			continue
		}
		for _, id := range ids {
			have[id] = true
		}
	}
	listed := map[string]bool{}
	for _, s := range sites {
		listed[s.id()] = true
		if _, ok := files[s.file]; !ok {
			out = append(out, s.id()+": the file is not read (moved or deleted)")
		} else if !have[s.id()] {
			out = append(out, s.id()+": no such function holds a permission check any more (renamed, moved or the check removed); update reachHiddenSites")
		}
	}
	for id := range have {
		if listed[id] {
			continue
		}
		if _, ok := excluded[id]; !ok {
			out = append(out, id+": holds a permission check and is neither a listed in-handler site nor an excluded function; add it to reachHiddenSites with its routes, or to reachHiddenExcluded with the reason")
		}
	}
	for id := range excluded {
		if !have[id] {
			out = append(out, id+": excluded but holds no permission check any more; drop it from reachHiddenExcluded")
		}
	}
	sort.Strings(out)
	return out
}

// reachReadHandlerSources reads the non-test Go files the scan covers: the server
// package itself (not its subpackages), everything under handlers, and cfaccess.
func reachReadHandlerSources(t testing.TB) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	add := func(path string) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		files[filepath.ToSlash(filepath.Clean(path))] = raw
	}
	keep := func(name string) bool {
		return strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go")
	}
	for _, dir := range []string{reachSrvDir, reachCfaccessDir} {
		ents, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range ents {
			if !e.IsDir() && keep(e.Name()) {
				add(filepath.Join(dir, e.Name()))
			}
		}
	}
	err := filepath.WalkDir(reachHandlersDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && keep(d.Name()) {
			add(path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// reachBarHiddenHandlerChecks: the routes of every listed in-handler check site (the
// owner routes for the owner guard). A discharge must cover each of them.
func reachBarHiddenHandlerChecks(in reachObligationInput) []string {
	set := map[string]bool{}
	for _, s := range reachHiddenSites {
		for _, r := range s.routes {
			set[r] = true
		}
		if s.ownerRoutes {
			for _, r := range reachOwnerRoutes(in) {
				set[r] = true
			}
		}
	}
	return reachSorted(set)
}

// ---- tests ----

func TestReachHiddenSites_AreHeldToTheHandlerSource(t *testing.T) {
	files := reachReadHandlerSources(t)
	if problems := reachHiddenSiteProblems(reachHiddenSites, reachHiddenExcluded, files); len(problems) > 0 {
		t.Errorf("the in-handler check sites no longer match the handlers:\n  %s", strings.Join(problems, "\n  "))
	}
}

func TestReachHiddenSites_EveryRouteIsARecordedRouteAndEverySiteHasRoutes(t *testing.T) {
	_, vd := reachLoad(t)
	have := map[string]bool{}
	for _, r := range vd.Routes {
		have[r.key()] = true
	}
	seen := map[string]bool{}
	for _, s := range reachHiddenSites {
		if seen[s.id()] {
			t.Errorf("%s is listed twice", s.id())
		}
		seen[s.id()] = true
		if len(s.routes) == 0 && !s.ownerRoutes {
			t.Errorf("%s has no routes: a site nobody reaches is not a bar", s.id())
		}
		for _, r := range s.routes {
			if !have[r] {
				t.Errorf("%s: %s is not a recorded route", s.id(), r)
			}
		}
	}
}

// The pin must notice each way a site can change; run on in-memory mutants of the
// real sources.
func TestReachHiddenSites_ThePinCatchesARenamedARemovedAndAnAddedSite(t *testing.T) {
	files := reachReadHandlerSources(t)
	if p := reachHiddenSiteProblems(reachHiddenSites, reachHiddenExcluded, files); len(p) > 0 {
		t.Fatalf("the unmutated sources already fail: %v", p)
	}
	mutate := func(file, from, to string) map[string][]byte {
		out := map[string][]byte{}
		maps.Copy(out, files)
		src := string(files[file])
		if !strings.Contains(src, from) {
			t.Fatalf("%s does not contain %q", file, from)
		}
		out[file] = []byte(strings.ReplaceAll(src, from, to))
		return out
	}
	apikeys := reachHandlersDir + "/apikeys.go"
	cases := []struct {
		name  string
		sites []reachHiddenSite
		files map[string][]byte
		want  string
	}{
		{"a handler function renamed", reachHiddenSites, mutate(apikeys, "func isAdminUser(", "func isAdminUserRenamed("), "isAdminUser: no such function"},
		{"the check removed from a site", reachHiddenSites, mutate(apikeys, "return auth.Can(c.Request.Context(), auth.PermUsersManage)", "return true"), "isAdminUser: no such function"},
		{"a site removed from the list", slices.DeleteFunc(slices.Clone(reachHiddenSites), func(s reachHiddenSite) bool { return s.name == "ChangePassword" }), files, "ChangePassword: holds a permission check and is neither"},
		{"a new check added to a handler", reachHiddenSites, mutate(apikeys, "// Create handles POST /auth/api-keys.", "func newHiddenGate(c *gin.Context) bool { return auth.Can(c.Request.Context(), auth.PermUsersManage) }\n\n// Create handles POST /auth/api-keys."), "newHiddenGate: holds a permission check"},
	}
	for _, tc := range cases {
		problems := reachHiddenSiteProblems(tc.sites, reachHiddenExcluded, tc.files)
		if !slices.ContainsFunc(problems, func(p string) bool { return strings.Contains(p, tc.want) }) {
			t.Errorf("%s: the pin did not report %q; got %v", tc.name, tc.want, problems)
		}
	}
}
