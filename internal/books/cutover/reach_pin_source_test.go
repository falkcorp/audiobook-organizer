// file: internal/books/cutover/reach_pin_source_test.go
// version: 1.2.0
// guid: 6dca55f4-5b1d-4718-b6a4-30854084cc7c
// last-edited: 2026-11-15

package cutover

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// The handler-admin pin (reachHandlerAdmin) is a claim about HEAD's handler code,
// so it is held to that code by structure, not by a string. Each pinned route's
// refusal is located inside a NAMED handler function with go/ast: the function
// has an `if` whose condition ORs `!slices.Contains(<actor>.Roles, adminRole)`
// and `!<pkg>.APIKeyMayActAsAdmin(...)`, and whose body returns. A needle search
// over the file would also match inviteTokenVisible's identical test and leave
// SetUserEmail unguarded (round-2 MAJOR-1); an `&& false` disable or a missing
// return keeps the text and fails here.
//
// Every function below is a pure function over source bytes, so the tests prove
// themselves on in-memory mutants and never touch the handler files.

// reachAdminFunc names one function the pin rests on.
type reachAdminFunc struct {
	file string // path from this package's directory
	recv string // receiver type name, "" for a plain function
	name string
	// guard: the function holds the admin refusal itself. Otherwise it must call
	// callee (mergeActor), which holds it.
	guard  bool
	callee string
}

// reachHandlerAdminFuncs maps each pinned route to the functions whose code
// makes the pinned refusal true at HEAD: the handler, and the function that
// holds the check when the handler delegates to it.
var reachHandlerAdminFuncs = map[string][]reachAdminFunc{
	"GET /api/v1/users/:id/footprint": {
		{file: "../../server/handlers/user_merge.go", recv: "UserHandler", name: "UserFootprint", callee: "mergeActor"},
		{file: "../../server/handlers/user_merge.go", recv: "UserHandler", name: "mergeActor", guard: true},
	},
	"GET /api/v1/users/merges": {
		{file: "../../server/handlers/user_merge.go", recv: "UserHandler", name: "ListUserMerges", callee: "mergeActor"},
		{file: "../../server/handlers/user_merge.go", recv: "UserHandler", name: "mergeActor", guard: true},
	},
	"PUT /api/v1/users/:id/email": {
		{file: "../../server/handlers/user.go", recv: "UserHandler", name: "SetUserEmail", guard: true},
	},
}

// reachFindFunc is the declaration of recv.name in a parsed file.
func reachFindFunc(f *ast.File, recv, name string) *ast.FuncDecl {
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Name.Name != name || fd.Body == nil {
			continue
		}
		got := ""
		if fd.Recv != nil && len(fd.Recv.List) == 1 {
			t := fd.Recv.List[0].Type
			if star, ok := t.(*ast.StarExpr); ok {
				t = star.X
			}
			if id, ok := t.(*ast.Ident); ok {
				got = id.Name
			}
		}
		if got == recv {
			return fd
		}
	}
	return nil
}

// reachOrOperands flattens a chain of `||` (through parentheses) into its
// operands. A `&&` operand stays whole: the refusal must be one of the ORed terms,
// so `(false && !slices.Contains(...))` is not.
func reachOrOperands(e ast.Expr) []ast.Expr {
	switch x := e.(type) {
	case *ast.ParenExpr:
		return reachOrOperands(x.X)
	case *ast.BinaryExpr:
		if x.Op == token.LOR {
			return append(reachOrOperands(x.X), reachOrOperands(x.Y)...)
		}
	}
	return []ast.Expr{e}
}

// reachNegatedCall reports whether e is `!<call>` for a call that match accepts.
func reachNegatedCall(e ast.Expr, match func(*ast.CallExpr) bool) bool {
	for {
		p, ok := e.(*ast.ParenExpr)
		if !ok {
			break
		}
		e = p.X
	}
	u, ok := e.(*ast.UnaryExpr)
	if !ok || u.Op != token.NOT {
		return false
	}
	var inner ast.Expr = u.X
	for {
		p, ok := inner.(*ast.ParenExpr)
		if !ok {
			break
		}
		inner = p.X
	}
	call, ok := inner.(*ast.CallExpr)
	return ok && match(call)
}

// reachIsAdminRoleTest matches slices.Contains(<actor>.Roles, adminRole), where
// <actor> is the identifier the CurrentUser assignment bound: a test on some
// other value's Roles (a variable introduced after the assignment, a request
// field) keeps the text and tests nobody the pin is about (round-4 A2c).
func reachIsAdminRoleTest(c *ast.CallExpr, actor string) bool {
	sel, ok := c.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Contains" || len(c.Args) != 2 {
		return false
	}
	if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "slices" {
		return false
	}
	roles, ok := c.Args[0].(*ast.SelectorExpr)
	if !ok || roles.Sel.Name != "Roles" {
		return false
	}
	if x, ok := roles.X.(*ast.Ident); !ok || actor == "" || x.Name != actor {
		return false
	}
	id, ok := c.Args[1].(*ast.Ident)
	return ok && id.Name == "adminRole"
}

// reachIsKeyMayActAsAdmin matches <pkg>.APIKeyMayActAsAdmin(...).
func reachIsKeyMayActAsAdmin(c *ast.CallExpr) bool {
	sel, ok := c.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "APIKeyMayActAsAdmin"
}

// reachEndsInReturn reports whether a block's last statement is a return.
func reachEndsInReturn(b *ast.BlockStmt) bool {
	if len(b.List) == 0 {
		return false
	}
	_, ok := b.List[len(b.List)-1].(*ast.ReturnStmt)
	return ok
}

// reachCurrentUserActor returns the identifier a statement of the shape
// `<a>, <b> := <pkg>.CurrentUser(c)` binds the user to (a), or "" when st is not
// that statement or binds the user to the blank identifier. It is the only
// statement that may precede the refusal.
func reachCurrentUserActor(st ast.Stmt) string {
	as, ok := st.(*ast.AssignStmt)
	if !ok || as.Tok != token.DEFINE || len(as.Rhs) != 1 || len(as.Lhs) != 2 {
		return ""
	}
	call, ok := as.Rhs[0].(*ast.CallExpr)
	if !ok {
		return ""
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "CurrentUser" {
		return ""
	}
	id, ok := as.Lhs[0].(*ast.Ident)
	if !ok || id.Name == "_" {
		return ""
	}
	return id.Name
}

// reachRefusalReturns reports whether a block ends in the return that refuses:
// a bare return for a function with no results, and for one with results a
// return whose LAST result is the literal `false` (mergeActor's `ok`). A refusal
// that answers 403 and then returns `"", true` hands the caller the success
// path under a refusal status and keeps every other line of the check (A2).
func reachRefusalReturns(b *ast.BlockStmt, hasResults bool) bool {
	if len(b.List) == 0 {
		return false
	}
	ret, ok := b.List[len(b.List)-1].(*ast.ReturnStmt)
	if !ok {
		return false
	}
	if !hasResults {
		return len(ret.Results) == 0
	}
	if len(ret.Results) == 0 {
		return false
	}
	last, ok := ret.Results[len(ret.Results)-1].(*ast.Ident)
	return ok && last.Name == "false"
}

// reachIsAdminRefusal reports whether st is the pinned refusal: an if with no init
// whose condition ORs both negated tests (the role test on the bound actor) and
// whose body ends in the refusing return.
func reachIsAdminRefusal(st ast.Stmt, actor string, hasResults bool) bool {
	ifs, ok := st.(*ast.IfStmt)
	if !ok || ifs.Init != nil {
		return false
	}
	role, key := false, false
	for _, op := range reachOrOperands(ifs.Cond) {
		role = role || reachNegatedCall(op, func(c *ast.CallExpr) bool { return reachIsAdminRoleTest(c, actor) })
		key = key || reachNegatedCall(op, reachIsKeyMayActAsAdmin)
	}
	return role && key && reachRefusalReturns(ifs.Body, hasResults)
}

// reachHasAdminRefusal reports whether the body holds the pinned refusal as its
// SECOND statement, right after the CurrentUser assignment that binds the actor:
// an if that ORs both negated tests (the role test on that actor) and ends in
// the refusing return. Walking the whole body (as an earlier version did)
// accepted the refusal inside a dead closure, under `if false`, or behind a
// request parameter (round-3 MINOR-A M1, M2, M4): each leaves the text and
// removes the refusal for the caller the pin is about. hasResults is whether the
// function declares results, which decides what its refusing return looks like.
func reachHasAdminRefusal(body *ast.BlockStmt, hasResults bool) bool {
	if len(body.List) < 2 {
		return false
	}
	actor := reachCurrentUserActor(body.List[0])
	return actor != "" && reachIsAdminRefusal(body.List[1], actor, hasResults)
}

// reachCallsGuard reports whether the body's FIRST statement is
// `if _, ok := <x>.name(...); !ok { ...return }`, the shape every merge handler
// uses: the condition is exactly the negation of the identifier the init assigned
// from the call, so `; ok {`, `; false {` and a guard that is not the first
// statement are refused.
func reachCallsGuard(body *ast.BlockStmt, name string) bool {
	if len(body.List) == 0 {
		return false
	}
	ifs, ok := body.List[0].(*ast.IfStmt)
	if !ok || ifs.Init == nil || !reachEndsInReturn(ifs.Body) {
		return false
	}
	as, ok := ifs.Init.(*ast.AssignStmt)
	if !ok || as.Tok != token.DEFINE || len(as.Rhs) != 1 || len(as.Lhs) != 2 {
		return false
	}
	call, ok := as.Rhs[0].(*ast.CallExpr)
	if !ok {
		return false
	}
	if sel, ok := call.Fun.(*ast.SelectorExpr); !ok || sel.Sel.Name != name {
		return false
	}
	okID, ok := as.Lhs[1].(*ast.Ident)
	if !ok {
		return false
	}
	var cond ast.Expr = ifs.Cond
	for {
		p, isParen := cond.(*ast.ParenExpr)
		if !isParen {
			break
		}
		cond = p.X
	}
	u, ok := cond.(*ast.UnaryExpr)
	if !ok || u.Op != token.NOT {
		return false
	}
	id, ok := u.X.(*ast.Ident)
	return ok && id.Name == okID.Name
}

// reachAdminFuncProblems checks one function against the source of its file.
func reachAdminFuncProblems(src []byte, fn reachAdminFunc) []string {
	f, err := parser.ParseFile(token.NewFileSet(), fn.file, src, 0)
	if err != nil {
		return []string{fmt.Sprintf("%s does not parse: %v", fn.file, err)}
	}
	who := fn.name
	if fn.recv != "" {
		who = "(" + fn.recv + ")." + fn.name
	}
	fd := reachFindFunc(f, fn.recv, fn.name)
	if fd == nil {
		return []string{fmt.Sprintf("%s: %s is gone or renamed: the handler the reachHandlerAdmin pin states has moved", fn.file, who)}
	}
	hasResults := fd.Type.Results != nil && len(fd.Type.Results.List) > 0
	if fn.guard && !reachHasAdminRefusal(fd.Body, hasResults) {
		return []string{fmt.Sprintf("%s: %s no longer refuses a caller that lacks the admin role (right after `<actor>, <ok> := CurrentUser(c)`, an if that ORs !slices.Contains(<actor>.Roles, adminRole) and !APIKeyMayActAsAdmin and returns, with a literal false last when the function has results)", fn.file, who)}
	}
	if fn.callee != "" && !reachCallsGuard(fd.Body, fn.callee) {
		return []string{fmt.Sprintf("%s: %s no longer returns when %s refuses", fn.file, who, fn.callee)}
	}
	return nil
}

// reachHandlerAdminProblems is what the handler side of the pin fails on, as text:
// every pinned route has functions listed, and each holds its check.
func reachHandlerAdminProblems(read func(path string) ([]byte, error), pin map[string]string, funcs map[string][]reachAdminFunc) []string {
	var out []string
	for route := range pin {
		fns, ok := funcs[route]
		if !ok || len(fns) == 0 {
			out = append(out, route+" is pinned and no handler function is named for it in reachHandlerAdminFuncs")
			continue
		}
		for _, fn := range fns {
			src, err := read(fn.file)
			if err != nil {
				out = append(out, fmt.Sprintf("%s: read %s: %v", route, fn.file, err))
				continue
			}
			for _, p := range reachAdminFuncProblems(src, fn) {
				out = append(out, route+": "+p)
			}
		}
	}
	for route := range funcs {
		if _, ok := pin[route]; !ok {
			out = append(out, route+" has handler functions listed and is not pinned")
		}
	}
	return out
}

// While HEAD's role-name tests sit inside the named handlers, the pin states
// what the router cannot see. The day F4-08 or the handler migration (04)
// replaces them with rbac.Can, the pin has outlived its reason and this fails:
// the registered path must prove the handlers and the pin is deleted.
func TestReachHandlerAdminPin_GoesWhenTheHandlersMigrate(t *testing.T) {
	for _, p := range reachHandlerAdminProblems(os.ReadFile, reachHandlerAdmin, reachHandlerAdminFuncs) {
		t.Errorf("%s. Delete the pin and prove the three routes (footprint, merges, email) through the registered path, as seams.d/F4-07.md asks of F4-08", p)
	}
}

// ---- the registered route table ----

// reachRouteSource is where the three pinned routes are registered.
const reachRouteSource = "../../server/wire_library_routes.go"

// reachHandlerRoutes names, for each pinned route, the handler the registration
// must hand it: the functions reachHandlerAdminFuncs checks are the ones the route
// reaches only while this holds. Without it the pin could keep the text of a
// handler that no longer serves the route (round-3 MINOR-A M7).
var reachHandlerRoutes = map[string]reachHandlerRoute{
	"GET /api/v1/users/:id/footprint": {handler: "UserFootprint", via: "GET"},
	"GET /api/v1/users/merges":        {handler: "ListUserMerges", via: "GET"},
	"PUT /api/v1/users/:id/email":     {handler: "SetUserEmail", via: "credRoute"},
}

// reachHandlerRoute is what a pinned route's registration must look like: the
// handler, the call that registers it, and, between the path and the handler,
// exactly the one route guard `s.perm("users.manage")` and nothing else. An
// extra argument is an inline middleware (the review's A3), which the AST pin
// of the handler cannot see and which can answer before the handler does.
type reachHandlerRoute struct {
	handler string
	via     string // "GET" for `<group>.GET`, "credRoute" for `s.credRoute(<group>, http.MethodPut, ...)`
}

// reachGuardArgs is the registration's arguments between the path and the end,
// as the file spells them, that a pinned route's registration must have.
func (r reachHandlerRoute) guardArgs() []string {
	return []string{`s.perm("users.manage")`, "userH." + r.handler}
}

// reachRegistration is one route registration read from wire_library_routes.go.
type reachRegistration struct {
	method  string
	suffix  string   // the group's own prefix plus the route's: "/users/merges"
	handler string   // the last argument, `userH.<name>`'s name, or "" when it is not one
	via     string   // the verb for `<group>.GET(...)`, the helper's name for `s.credRoute(...)`
	args    []string // every argument after the path, as written (go/types.ExprString)
}

// reachRegistrations reads every registration on a group the file creates with
// `x := ....Group("/prefix")`: x.GET/PUT/POST/DELETE/PATCH(rel, ...) and
// s.credRoute / s.credRouteWhen / s.ownerCredRoute(x, http.MethodX, rel, ...).
func reachRegistrations(src []byte) ([]reachRegistration, error) {
	f, err := parser.ParseFile(token.NewFileSet(), reachRouteSource, src, 0)
	if err != nil {
		return nil, err
	}
	str := func(e ast.Expr) (string, bool) {
		lit, ok := e.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return "", false
		}
		v, err := strconv.Unquote(lit.Value)
		return v, err == nil
	}
	prefix := map[string]string{}
	ast.Inspect(f, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
			return true
		}
		call, ok := as.Rhs[0].(*ast.CallExpr)
		if !ok || len(call.Args) < 1 {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Group" {
			if id, ok := as.Lhs[0].(*ast.Ident); ok {
				if v, ok := str(call.Args[0]); ok {
					prefix[id.Name] = v
				}
			}
		}
		return true
	})
	handlerOf := func(call *ast.CallExpr) string {
		sel, ok := call.Args[len(call.Args)-1].(*ast.SelectorExpr)
		if !ok {
			return ""
		}
		if x, ok := sel.X.(*ast.Ident); !ok || x.Name != "userH" {
			return ""
		}
		return sel.Sel.Name
	}
	argsFrom := func(call *ast.CallExpr, from int) []string {
		var out []string
		for _, a := range call.Args[from:] {
			out = append(out, types.ExprString(a))
		}
		return out
	}
	verbs := map[string]bool{"GET": true, "PUT": true, "POST": true, "DELETE": true, "PATCH": true}
	var out []reachRegistration
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch {
		case verbs[sel.Sel.Name]:
			g, ok := sel.X.(*ast.Ident)
			rel, ok2 := str(call.Args[0])
			if ok && ok2 && prefix[g.Name] != "" {
				out = append(out, reachRegistration{sel.Sel.Name, prefix[g.Name] + rel, handlerOf(call), sel.Sel.Name, argsFrom(call, 1)})
			}
		case sel.Sel.Name == "credRoute" || sel.Sel.Name == "credRouteWhen" || sel.Sel.Name == "ownerCredRoute":
			if len(call.Args) < 4 {
				return true
			}
			g, ok := call.Args[0].(*ast.Ident)
			verb, ok2 := call.Args[1].(*ast.SelectorExpr)
			rel, ok3 := str(call.Args[2])
			if ok && ok2 && ok3 && prefix[g.Name] != "" && strings.HasPrefix(verb.Sel.Name, "Method") {
				out = append(out, reachRegistration{strings.ToUpper(strings.TrimPrefix(verb.Sel.Name, "Method")), prefix[g.Name] + rel, handlerOf(call), sel.Sel.Name, argsFrom(call, 3)})
			}
		}
		return true
	})
	return out, nil
}

// reachRouteHandlerProblems is what the route-to-handler side of the pin fails on,
// as text: each pinned route is registered exactly once, with the handler
// reachHandlerRoutes names.
func reachRouteHandlerProblems(src []byte, pin map[string]string, want map[string]reachHandlerRoute) []string {
	regs, err := reachRegistrations(src)
	if err != nil {
		return []string{reachRouteSource + " does not parse: " + err.Error()}
	}
	var out []string
	for route := range pin {
		method, path, _ := strings.Cut(route, " ")
		wantRoute, ok := want[route]
		if !ok {
			out = append(out, route+" is pinned and reachHandlerRoutes names no handler for it")
			continue
		}
		var hits []reachRegistration
		for _, r := range regs {
			if r.method == method && strings.HasSuffix(path, r.suffix) {
				hits = append(hits, r)
			}
		}
		switch {
		case len(hits) != 1:
			out = append(out, fmt.Sprintf("%s: %s registers it %d time(s), want exactly 1", route, reachRouteSource, len(hits)))
		case hits[0].handler != wantRoute.handler:
			out = append(out, fmt.Sprintf("%s: %s registers it with %q, the pin rests on userH.%s", route, reachRouteSource, hits[0].handler, wantRoute.handler))
		case hits[0].via != wantRoute.via:
			out = append(out, fmt.Sprintf("%s: %s registers it through %s, the pin rests on %s", route, reachRouteSource, hits[0].via, wantRoute.via))
		case !reflect.DeepEqual(hits[0].args, wantRoute.guardArgs()):
			out = append(out, fmt.Sprintf("%s: %s registers it with the arguments %v, the pin rests on exactly %v (an extra one is a middleware the pin cannot see)", route, reachRouteSource, hits[0].args, wantRoute.guardArgs()))
		}
	}
	for route := range want {
		if _, ok := pin[route]; !ok {
			out = append(out, route+" has a handler named in reachHandlerRoutes and is not pinned")
		}
	}
	return out
}

// ---- the mount switch ----

const reachMountSource = "../module.go"

// reachMountAuth is what books' MountLegacy call hands as Auth: the name of the
// httpapi identifier, found in the httpapi.LegacyMount literal.
func reachMountAuth(src []byte) (string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), reachMountSource, src, 0)
	if err != nil {
		return "", err
	}
	found := ""
	n := 0
	ast.Inspect(f, func(node ast.Node) bool {
		lit, ok := node.(*ast.CompositeLit)
		if !ok {
			return true
		}
		sel, ok := lit.Type.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "LegacyMount" {
			return true
		}
		for _, el := range lit.Elts {
			kv, ok := el.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			if k, ok := kv.Key.(*ast.Ident); !ok || k.Name != "Auth" {
				continue
			}
			n++
			switch v := kv.Value.(type) {
			case *ast.SelectorExpr:
				found = v.Sel.Name
			case *ast.Ident:
				found = v.Name
			default:
				found = "<expression>"
			}
		}
		return true
	})
	if n != 1 {
		return "", fmt.Errorf("%s holds %d httpapi.LegacyMount literals with an Auth field, want exactly 1", reachMountSource, n)
	}
	return found, nil
}

// reachFlipProblems is what the mount switch fails on, as text. While the pin
// holds cells (pinned) or the oracle has no owner-set recording (ownerRecorded
// false), books may mount only with LegacyAuthGin: the router is not the
// authorization decision for the 11 pinned cells until the registered path
// proves the handlers, and the oracle models no owner-assured deployment until
// R0 has been recorded with an owner set. It also fails once per open obligation
// (reachFlipObligations: what the oracle knows it does not compare), so that list
// must be empty at the flip. An unreadable or unfamiliar mount fails closed.
func reachFlipProblems(src []byte, pinned int, ownerRecorded bool, obligations []reachFlipObligation) []string {
	auth, err := reachMountAuth(src)
	if err != nil {
		return []string{"the mount's Auth could not be read, so the flip to LegacyAuthCore cannot be ruled out: " + err.Error()}
	}
	switch auth {
	case "LegacyAuthGin":
		return nil
	case "LegacyAuthCore":
		var out []string
		if pinned > 0 {
			out = append(out, fmt.Sprintf("books mounts with LegacyAuthCore while reachHandlerAdmin pins %d route(s): stage 13 would admit the %d pinned cells the handler refuses today. Prove the migrated handlers on the registered path, delete the pin, then flip (seams.d/F4-07.md, Obligations on F4-08)", pinned, len(reachPinnedCells)))
		}
		if !ownerRecorded {
			out = append(out, "books mounts with LegacyAuthCore and verdicts.json holds no owner_set recording: the oracle modelled only deployments with no owner set (05-authn 4.8 keeps the owner-set behaviour through core.auth.owner.required). Record R0 with owner_email set and model it before the flip")
		}
		for _, o := range obligations {
			out = append(out, fmt.Sprintf("books mounts with LegacyAuthCore while %s is open: %s", o.name, o.discharge))
		}
		return out
	}
	return []string{fmt.Sprintf("books mounts with Auth %s, which this guard does not know: it fails closed until the guard names it", auth)}
}

// The mount switch to LegacyAuthCore waits on the pin and on the owner-set
// recording (the ruled fallback of round-2 MINOR-8). Reading module.go as source
// means the guard fires in this package's own CI the moment the flip lands.
func TestReachFlip_FailsWhileThePinOrTheOwnerlessModelStands(t *testing.T) {
	src, err := os.ReadFile(reachMountSource)
	if err != nil {
		t.Fatal(err)
	}
	_, vd := reachLoad(t)
	for _, p := range reachFlipProblems(src, len(reachHandlerAdmin), vd.OwnerSet != nil, reachFlipObligations) {
		t.Error(p)
	}
}

// ---- the guards, proven on in-memory mutants ----

func reachMutate(t *testing.T, src []byte, from, to string) []byte {
	t.Helper()
	if bytes.Count(src, []byte(from)) != 1 {
		t.Fatalf("the mutant's anchor %q occurs %d times, want 1", from, bytes.Count(src, []byte(from)))
	}
	return bytes.Replace(src, []byte(from), []byte(to), 1)
}

// The real files satisfy the guard (so a mutant's failure is the mutant's).
func TestReachHandlerAdminPin_TheRealHandlersHoldTheirChecks(t *testing.T) {
	if p := reachHandlerAdminProblems(os.ReadFile, reachHandlerAdmin, reachHandlerAdminFuncs); len(p) > 0 {
		t.Fatalf("%s", strings.Join(p, "\n"))
	}
}

// Round-2 MAJOR-1: SetUserEmail loses its admin test while inviteTokenVisible
// keeps the same text in the same file. A per-file needle search passed this.
func TestReachHandlerAdminPin_FailsWhenSetUserEmailLosesItsCheckButAnotherFunctionKeepsTheText(t *testing.T) {
	src, err := os.ReadFile("../../server/handlers/user.go")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(src, []byte("slices.Contains(actor.Roles, adminRole)")) < 2 {
		t.Fatal("the premise changed: the admin-role test no longer occurs in a second function of user.go, so this mutant proves nothing")
	}
	mutant := reachMutate(t, src,
		`if !ok || actor == nil || actor.Status != "active" || !slices.Contains(actor.Roles, adminRole) || !servermiddleware.APIKeyMayActAsAdmin(c) {
		httputil.RespondWithForbidden(c, "setting another user's email needs the admin role")`,
		`if !ok || actor == nil || actor.Status != "active" {
		httputil.RespondWithForbidden(c, "setting another user's email needs the admin role")`)
	read := func(path string) ([]byte, error) {
		if strings.HasSuffix(path, "handlers/user.go") {
			return mutant, nil
		}
		return os.ReadFile(path)
	}
	got := reachHandlerAdminProblems(read, reachHandlerAdmin, reachHandlerAdminFuncs)
	if len(got) != 1 || !strings.Contains(got[0], "SetUserEmail") || !strings.Contains(got[0], "no longer refuses") {
		t.Errorf("deleting SetUserEmail's admin check: %q", got)
	}
}

func TestReachHandlerAdminPin_FailsOnEachWayTheRefusalCanBeDisabled(t *testing.T) {
	src, err := os.ReadFile("../../server/handlers/user_merge.go")
	if err != nil {
		t.Fatal(err)
	}
	const cond = `!has || actor == nil || actor.Status != "active" || !slices.Contains(actor.Roles, adminRole) || !servermiddleware.APIKeyMayActAsAdmin(c)`
	for _, tc := range []struct {
		name string
		from string
		to   string
		want string
	}{
		{"the role test is ANDed with false", cond, `!has || actor == nil || actor.Status != "active" || (false && !slices.Contains(actor.Roles, adminRole)) || !servermiddleware.APIKeyMayActAsAdmin(c)`, "mergeActor"},
		{"the key test is dropped", cond, `!has || actor == nil || actor.Status != "active" || !slices.Contains(actor.Roles, adminRole)`, "mergeActor"},
		{"the role test is no longer negated", cond, `!has || actor == nil || actor.Status != "active" || slices.Contains(actor.Roles, adminRole) || !servermiddleware.APIKeyMayActAsAdmin(c)`, "mergeActor"},
		{"the refusal no longer returns", "\t\treturn \"\", false\n\t}\n\tif h.merge == nil {", "\t}\n\tif h.merge == nil {", "mergeActor"},
		{"A2: the refusal answers 403 and returns ok", "\t\treturn \"\", false\n\t}\n\tif h.merge == nil {", "\t\treturn \"\", true\n\t}\n\tif h.merge == nil {", "mergeActor"},
		{"A2: the refusal returns a computed ok", "\t\treturn \"\", false\n\t}\n\tif h.merge == nil {", "\t\treturn \"\", h.merge != nil\n\t}\n\tif h.merge == nil {", "mergeActor"},
		{"A2c: the role test reads another value's Roles", cond, `!has || actor == nil || actor.Status != "active" || !slices.Contains(servermiddleware.Roles, adminRole) || !servermiddleware.APIKeyMayActAsAdmin(c)`, "mergeActor"},
		{"footprint ignores the actor check", "func (h *UserHandler) UserFootprint(c *gin.Context) {\n\tif _, ok := h.mergeActor(c); !ok {\n\t\treturn\n\t}\n", "func (h *UserHandler) UserFootprint(c *gin.Context) {\n\tif _, ok := h.mergeActor(c); ok {\n\t}\n", "UserFootprint"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mutant := reachMutate(t, src, tc.from, tc.to)
			read := func(path string) ([]byte, error) {
				if strings.HasSuffix(path, "handlers/user_merge.go") {
					return mutant, nil
				}
				return os.ReadFile(path)
			}
			got := reachHandlerAdminProblems(read, reachHandlerAdmin, reachHandlerAdminFuncs)
			if len(got) == 0 {
				t.Fatalf("%s: the guard passed", tc.name)
			}
			if tc.want != "" && !strings.Contains(strings.Join(got, "\n"), tc.want) {
				t.Errorf("%s: the failure does not name %s: %q", tc.name, tc.want, got)
			}
		})
	}
}

// A2c on SetUserEmail: the role test reads the Roles of a value other than the
// signed-in user the CurrentUser assignment bound.
func TestReachHandlerAdminPin_FailsWhenTheRoleTestReadsAnotherValue(t *testing.T) {
	src, err := os.ReadFile("../../server/handlers/user.go")
	if err != nil {
		t.Fatal(err)
	}
	mutant := reachMutate(t, src,
		`actor.Status != "active" || !slices.Contains(actor.Roles, adminRole) || !servermiddleware.APIKeyMayActAsAdmin(c) {
		httputil.RespondWithForbidden(c, "setting another user's email needs the admin role")`,
		`actor.Status != "active" || !slices.Contains(c.Keys["roles"].([]string), adminRole) || !servermiddleware.APIKeyMayActAsAdmin(c) {
		httputil.RespondWithForbidden(c, "setting another user's email needs the admin role")`)
	got := reachAdminFuncProblems(mutant, reachAdminFunc{file: "user.go", recv: "UserHandler", name: "SetUserEmail", guard: true})
	if len(got) != 1 || !strings.Contains(got[0], "no longer refuses") {
		t.Errorf("a role test on another value: %q", got)
	}
	bound := reachMutate(t, src,
		`actor, ok := servermiddleware.CurrentUser(c)
	if !ok || actor == nil || actor.Status != "active" || !slices.Contains(actor.Roles, adminRole) || !servermiddleware.APIKeyMayActAsAdmin(c) {
		httputil.RespondWithForbidden(c, "setting another user's email needs the admin role")`,
		`_, ok := servermiddleware.CurrentUser(c)
	if !ok || actor == nil || actor.Status != "active" || !slices.Contains(actor.Roles, adminRole) || !servermiddleware.APIKeyMayActAsAdmin(c) {
		httputil.RespondWithForbidden(c, "setting another user's email needs the admin role")`)
	got = reachAdminFuncProblems(bound, reachAdminFunc{file: "user.go", recv: "UserHandler", name: "SetUserEmail", guard: true})
	if len(got) != 1 {
		t.Errorf("a CurrentUser assignment that binds nothing to the test: %q", got)
	}
}

func TestReachHandlerAdminPin_FailsWhenAHandlerIsRenamedOrAPinnedRouteHasNoFunction(t *testing.T) {
	src, err := os.ReadFile("../../server/handlers/user.go")
	if err != nil {
		t.Fatal(err)
	}
	renamed := reachMutate(t, src, "func (h *UserHandler) SetUserEmail(", "func (h *UserHandler) SetUserEmailV2(")
	got := reachAdminFuncProblems(renamed, reachAdminFunc{file: "user.go", recv: "UserHandler", name: "SetUserEmail", guard: true})
	if len(got) != 1 || !strings.Contains(got[0], "gone or renamed") {
		t.Errorf("a renamed handler: %q", got)
	}
	got = reachHandlerAdminProblems(os.ReadFile, map[string]string{"GET /api/v1/batcave/gadgets": "x"}, reachHandlerAdminFuncs)
	if len(got) == 0 {
		t.Error("a pinned route with no named handler function was accepted")
	}
	got = reachHandlerAdminProblems(os.ReadFile, map[string]string{}, reachHandlerAdminFuncs)
	if len(got) == 0 {
		t.Error("handler functions listed for a route that is no longer pinned were accepted")
	}
}

func TestReachFlip_TheRealMountIsLegacyAuthGin(t *testing.T) {
	src, err := os.ReadFile(reachMountSource)
	if err != nil {
		t.Fatal(err)
	}
	if auth, err := reachMountAuth(src); err != nil || auth != "LegacyAuthGin" {
		t.Fatalf("books mounts with %q (%v): the flip landed, and these tests were written for the mount before it", auth, err)
	}
}

func TestReachFlip_FailsAtTheFlipWhileThePinOrTheOwnerlessModelStands(t *testing.T) {
	src, err := os.ReadFile(reachMountSource)
	if err != nil {
		t.Fatal(err)
	}
	flipped := reachMutate(t, src, "Auth:        httpapi.LegacyAuthGin,", "Auth:        httpapi.LegacyAuthCore,")
	for _, tc := range []struct {
		name          string
		src           []byte
		pinned        int
		ownerRecorded bool
		obligations   []reachFlipObligation
		want          int // problems
	}{
		{"unflipped: nothing to say", src, 3, false, reachFlipObligations, 0},
		{"flipped with the pin", flipped, 3, true, nil, 1},
		{"flipped with no owner-set recording", flipped, 0, false, nil, 1},
		{"flipped with both", flipped, 3, false, nil, 2},
		{"flipped with neither and no open obligation: free to go", flipped, 0, true, nil, 0},
		{"flipped with every open obligation", flipped, 0, true, reachFlipObligations, len(reachFlipObligations)},
		{"an unreadable mount fails closed", []byte("package books\n"), 0, true, nil, 1},
		{"an unfamiliar Auth fails closed", reachMutate(t, src, "Auth:        httpapi.LegacyAuthGin,", "Auth:        authMode(),"), 0, true, nil, 1},
	} {
		if got := reachFlipProblems(tc.src, tc.pinned, tc.ownerRecorded, tc.obligations); len(got) != tc.want {
			t.Errorf("%s: %d problem(s) %q, want %d", tc.name, len(got), got, tc.want)
		}
	}
}

// One flip case per obligation: with the pin gone and an owner-set recording, an
// obligation that is the only one open still blocks the flip, with exactly one
// problem that names it. Removing an entry from the list while the flip stands is
// caught by reachObligationProblems (reach_flip_obligations_test.go).
func TestReachFlip_EachOpenObligationAloneBlocksTheFlip(t *testing.T) {
	src, err := os.ReadFile(reachMountSource)
	if err != nil {
		t.Fatal(err)
	}
	flipped := reachMutate(t, src, "Auth:        httpapi.LegacyAuthGin,", "Auth:        httpapi.LegacyAuthCore,")
	if len(reachFlipObligations) == 0 {
		t.Fatal("no open obligations listed: nothing blocks the flip")
	}
	for _, o := range reachFlipObligations {
		got := reachFlipProblems(flipped, 0, true, []reachFlipObligation{o})
		if len(got) != 1 || !strings.Contains(got[0], o.name) {
			t.Errorf("flipped with only %s open: %q", o.name, got)
		}
	}
}

// ---- round-3 MINOR-A: the refusal must be a top-level statement, and the pinned
// routes must reach the functions that hold it ----

func TestReachRouteTable_TheRealRegistrationsNameThePinnedHandlers(t *testing.T) {
	src, err := os.ReadFile(reachRouteSource)
	if err != nil {
		t.Fatal(err)
	}
	if p := reachRouteHandlerProblems(src, reachHandlerAdmin, reachHandlerRoutes); len(p) != 0 {
		t.Fatalf("%s", strings.Join(p, "\n"))
	}
	regs, err := reachRegistrations(src)
	if err != nil || len(regs) < 10 {
		t.Fatalf("the registration reader found %d registrations (%v): it no longer reads the file's shapes", len(regs), err)
	}
}

// M7: the route is rewired to a different handler (the pin keeps the text of one
// that no longer serves it), registered twice, or no longer registered at all.
func TestReachRouteTable_FailsWhenAPinnedRouteIsRewiredDuplicatedOrDropped(t *testing.T) {
	src, err := os.ReadFile(reachRouteSource)
	if err != nil {
		t.Fatal(err)
	}
	const email = `s.credRoute(users, http.MethodPut, "/:id/email", s.perm("users.manage"), userH.SetUserEmail)`
	const merges = `users.GET("/merges", s.perm("users.manage"), userH.ListUserMerges)`
	const footprint = `users.GET("/:id/footprint", s.perm("users.manage"), userH.UserFootprint)`
	for _, tc := range []struct {
		name, from, to, want string
	}{
		{"M7: the email route rewired to another handler", email, `s.credRoute(users, http.MethodPut, "/:id/email", s.perm("users.manage"), userH.DeactivateUser)`, "DeactivateUser"},
		{"the merges route rewired to the footprint handler", merges, `users.GET("/merges", s.perm("users.manage"), userH.UserFootprint)`, "UserFootprint"},
		{"the footprint route rewired to a closure", footprint, `users.GET("/:id/footprint", s.perm("users.manage"), func(c *gin.Context) {})`, `registers it with ""`},
		{"the email route registered twice", email, email + "\n\t\t" + email, "2 time(s)"},
		{"the footprint route no longer registered", footprint, "", "0 time(s)"},
		{"the merges route moved to another verb", merges, `users.POST("/merges", s.perm("users.manage"), userH.ListUserMerges)`, "0 time(s)"},
		{"A3: an inline middleware before the footprint handler", footprint, `users.GET("/:id/footprint", s.perm("users.manage"), gin.Logger(), userH.UserFootprint)`, "an extra one is a middleware"},
		{"A3: an inline middleware before the route guard", merges, `users.GET("/merges", gin.Recovery(), s.perm("users.manage"), userH.ListUserMerges)`, "an extra one is a middleware"},
		{"A3: an inline middleware on the credential route", email, `s.credRoute(users, http.MethodPut, "/:id/email", s.perm("users.manage"), gin.Logger(), userH.SetUserEmail)`, "an extra one is a middleware"},
		{"the route guard is dropped", merges, `users.GET("/merges", userH.ListUserMerges)`, "an extra one is a middleware"},
		{"the route guard asks for another permission", footprint, `users.GET("/:id/footprint", s.perm("users.view"), userH.UserFootprint)`, "an extra one is a middleware"},
		{"the email route registered without the credential helper", email, `users.PUT("/:id/email", s.perm("users.manage"), userH.SetUserEmail)`, "registers it through PUT"},
	} {
		got := reachRouteHandlerProblems(reachMutate(t, src, tc.from, tc.to), reachHandlerAdmin, reachHandlerRoutes)
		if len(got) != 1 || !strings.Contains(got[0], tc.want) {
			t.Errorf("%s: %q, want one problem naming %s", tc.name, got, tc.want)
		}
	}
	if got := reachRouteHandlerProblems(src, map[string]string{"GET /api/v1/batcave/gadgets": "x"}, reachHandlerRoutes); len(got) == 0 {
		t.Error("a pinned route with no handler named was accepted")
	}
	if got := reachRouteHandlerProblems(src, map[string]string{}, reachHandlerRoutes); len(got) == 0 {
		t.Error("a handler named for a route that is no longer pinned was accepted")
	}
}

// M1, M2 and M4 (and the guard-call variants): each keeps the refusal's text and
// takes it off the path of the caller the pin is about.
func TestReachHandlerAdminPin_FailsWhenTheRefusalIsNotATopLevelStatement(t *testing.T) {
	user, err := os.ReadFile("../../server/handlers/user.go")
	if err != nil {
		t.Fatal(err)
	}
	merge, err := os.ReadFile("../../server/handlers/user_merge.go")
	if err != nil {
		t.Fatal(err)
	}
	const setCond = `!ok || actor == nil || actor.Status != "active" || !slices.Contains(actor.Roles, adminRole) || !servermiddleware.APIKeyMayActAsAdmin(c)`
	const setIf = "\tif " + setCond + " {\n\t\thttputil.RespondWithForbidden(c, \"setting another user's email needs the admin role\")\n\t\treturn\n\t}\n"
	const mergeCond = `!has || actor == nil || actor.Status != "active" || !slices.Contains(actor.Roles, adminRole) || !servermiddleware.APIKeyMayActAsAdmin(c)`
	const mergeIf = "\tif " + mergeCond + " {\n\t\thttputil.RespondWithForbidden(c, \"merging accounts needs the admin role\")\n\t\treturn \"\", false\n\t}\n"
	const foot = "func (h *UserHandler) UserFootprint(c *gin.Context) {\n\tif _, ok := h.mergeActor(c); !ok {\n\t\treturn\n\t}\n"
	for _, tc := range []struct {
		name string
		file string
		src  []byte
		from string
		to   string
		want string
	}{
		{"M1: SetUserEmail's refusal inside a closure that is never called", "handlers/user.go", user, setIf, "\t_ = func() {\n" + setIf + "\t}\n", "SetUserEmail"},
		{"M2: SetUserEmail's refusal under if false", "handlers/user.go", user, "\tif " + setCond + " {\n\t\thttputil.RespondWithForbidden(c, \"setting another user's email needs the admin role\")", "\tif false {\n\t\thttputil.RespondWithForbidden(c, \"setting another user's email needs the admin role\")", "SetUserEmail"},
		{"M4: SetUserEmail's refusal behind a query parameter", "handlers/user.go", user, setIf, "\tif c.Query(\"batarang\") == \"\" {\n" + setIf + "\t}\n", "SetUserEmail"},
		{"SetUserEmail's refusal moved below another statement", "handlers/user.go", user, setIf + "\tif h.emailSetter == nil {", "\tif h.emailSetter == nil {" + "\n\t\thttputil.RespondWithInternalError(c, \"x\")\n\t\treturn\n\t}\n" + setIf + "\tif false {", "SetUserEmail"},
		{"M1: mergeActor's refusal inside a closure", "handlers/user_merge.go", merge, mergeIf, "\t_ = func() {\n" + mergeIf + "\t}\n", "mergeActor"},
		{"M2: mergeActor's refusal under if false", "handlers/user_merge.go", merge, "\tif " + mergeCond + " {\n\t\thttputil.RespondWithForbidden(c, \"merging accounts needs the admin role\")", "\tif false {\n\t\thttputil.RespondWithForbidden(c, \"merging accounts needs the admin role\")", "mergeActor"},
		{"M4: mergeActor's refusal behind a query parameter", "handlers/user_merge.go", merge, mergeIf, "\tif c.Query(\"batarang\") == \"\" {\n" + mergeIf + "\t}\n", "mergeActor"},
		{"the footprint guard call's condition is false", "handlers/user_merge.go", merge, foot, "func (h *UserHandler) UserFootprint(c *gin.Context) {\n\tif _, ok := h.mergeActor(c); false {\n\t\treturn\n\t}\n", "UserFootprint"},
		{"the footprint guard call is gated behind a query parameter", "handlers/user_merge.go", merge, foot, "func (h *UserHandler) UserFootprint(c *gin.Context) {\n\tif c.Query(\"batarang\") == \"\" {\n\tif _, ok := h.mergeActor(c); !ok {\n\t\treturn\n\t}\n\t}\n", "UserFootprint"},
		{"the footprint guard call tests a different identifier", "handlers/user_merge.go", merge, foot, "func (h *UserHandler) UserFootprint(c *gin.Context) {\n\tif _, ok := h.mergeActor(c); !c.IsAborted() {\n\t\treturn\n\t}\n", "UserFootprint"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mutant := reachMutate(t, tc.src, tc.from, tc.to)
			read := func(path string) ([]byte, error) {
				if strings.HasSuffix(path, tc.file) {
					return mutant, nil
				}
				return os.ReadFile(path)
			}
			got := reachHandlerAdminProblems(read, reachHandlerAdmin, reachHandlerAdminFuncs)
			if len(got) == 0 || !strings.Contains(strings.Join(got, "\n"), tc.want) {
				t.Errorf("%s: the guard passed or does not name %s: %q", tc.name, tc.want, got)
			}
		})
	}
}
