// file: internal/books/cutover/reach_pin_source_test.go
// version: 1.0.0
// guid: 6dca55f4-5b1d-4718-b6a4-30854084cc7c
// last-edited: 2026-11-14

package cutover

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
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

// reachIsAdminRoleTest matches slices.Contains(<x>.Roles, adminRole).
func reachIsAdminRoleTest(c *ast.CallExpr) bool {
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

// reachHasAdminRefusal reports whether the body holds the pinned refusal: an if
// whose condition ORs both negated tests and whose body returns.
func reachHasAdminRefusal(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		ifs, ok := n.(*ast.IfStmt)
		if !ok || found {
			return !found
		}
		role, key := false, false
		for _, op := range reachOrOperands(ifs.Cond) {
			role = role || reachNegatedCall(op, reachIsAdminRoleTest)
			key = key || reachNegatedCall(op, reachIsKeyMayActAsAdmin)
		}
		if role && key && reachEndsInReturn(ifs.Body) {
			found = true
		}
		return !found
	})
	return found
}

// reachCallsGuard reports whether the body calls <x>.name(...) as the condition
// or init of an if whose body returns when it fails: `if _, ok := h.name(c); !ok {
// return }` (the shape every merge handler uses).
func reachCallsGuard(body *ast.BlockStmt, name string) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		ifs, ok := n.(*ast.IfStmt)
		if !ok || found || ifs.Init == nil || !reachEndsInReturn(ifs.Body) {
			return !found
		}
		ast.Inspect(ifs.Init, func(m ast.Node) bool {
			if c, ok := m.(*ast.CallExpr); ok {
				if sel, ok := c.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == name {
					found = true
				}
			}
			return !found
		})
		return !found
	})
	return found
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
	if fn.guard && !reachHasAdminRefusal(fd.Body) {
		return []string{fmt.Sprintf("%s: %s no longer refuses a caller that lacks the admin role (an if that ORs !slices.Contains(<actor>.Roles, adminRole) and !APIKeyMayActAsAdmin and returns)", fn.file, who)}
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
// R0 has been recorded with an owner set. An unreadable or unfamiliar mount fails
// closed.
func reachFlipProblems(src []byte, pinned int, ownerRecorded bool) []string {
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
	for _, p := range reachFlipProblems(src, len(reachHandlerAdmin), vd.OwnerSet != nil) {
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
		want          int // problems
	}{
		{"unflipped: nothing to say", src, 3, false, 0},
		{"flipped with the pin", flipped, 3, true, 1},
		{"flipped with no owner-set recording", flipped, 0, false, 1},
		{"flipped with both", flipped, 3, false, 2},
		{"flipped with neither: free to go", flipped, 0, true, 0},
		{"an unreadable mount fails closed", []byte("package books\n"), 0, true, 1},
		{"an unfamiliar Auth fails closed", reachMutate(t, src, "Auth:        httpapi.LegacyAuthGin,", "Auth:        authMode(),"), 0, true, 1},
	} {
		if got := reachFlipProblems(tc.src, tc.pinned, tc.ownerRecorded); len(got) != tc.want {
			t.Errorf("%s: %d problem(s) %q, want %d", tc.name, len(got), got, tc.want)
		}
	}
}
