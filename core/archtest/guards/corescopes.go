// file: core/archtest/guards/corescopes.go
// version: 2.0.0
// guid: 108df91a-c621-4d4a-abfe-0f22f6ea0909
// last-edited: 2026-11-28

package guards

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"path"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/types/typeutil"

	"github.com/falkcorp/mediacore/archtest"
)

// rbacPath is core's rbac package, whose Permission type the scan follows.
const rbacPath = "github.com/falkcorp/mediacore/rbac"

// coreScopeExempt reports the packages CoreScopesOnly does not read, by path
// relative to the module: the test-support packages (CoreTestSupport:
// coretest, archtest, every <pkg>/<pkg>test suite) and the packages below such
// a suite (tasks/taskstest/parity is part of the taskstest suite and names an
// app's arkham:run in its fixtures), which fixture scopes of their own, and
// examples, which are apps built on core (hello names hello:admin) and not
// core. A new entry is a decision.
func coreScopeExempt(rel string) bool {
	if rel == "examples" || strings.HasPrefix(rel, "examples/") {
		return true
	}
	for dir := rel; dir != "" && dir != "."; dir = path.Dir(dir) {
		if archtest.CoreTestSupport(dir) {
			return true
		}
	}
	return false
}

// CoreScopesOnly fails t when, in the non-test code of the packages patterns
// name (core's own: module/...), a constant-valued expression of type
// rbac.Permission has a namespace other than "core", or a constant string is
// passed to rbac.ParsePermission or rbac.ParsePattern with one (W6, 05-rbac
// 6.1). module is the module path; packages whose path relative to it is
// coreScopeExempt are loaded and skipped.
//
// Core names no scope of an app: a core route requires core's own scopes (R6,
// httpapi checkPerm), and when an app needs one of its own on a core route it
// says so itself, in app.Options.RouteCoRequirements, where the router ANDs it
// on. A core package that named books:integrations.manage would put one app's
// vocabulary into every app's binary and bypass the rule that keeps the
// co-requirement the app's to declare, so the scan is how that stays true.
//
// The scan is typed, so it follows the value and not the spelling. It flags
// every expression go/types gives a constant value and the type
// rbac.Permission, however the type got there: a literal passed to rbac.Need,
// AllOf, AnyOf, to Checker.Has or to httpapi.Forbidden; an untyped const
// handed to any of them; "books"+":x" and rbac.Permission(ns+":x") over
// constants; a Permission-typed const, var, struct field, slice or array
// element, map key or value, return value or assignment; a PermissionSpec
// Name or Implies; all through an import alias too. The outermost such
// expression is the one reported, and a use of a constant that is itself
// declared as a Permission is not reported again (its declaration is). A
// constant string given to ParsePermission or ParsePattern is flagged by the
// call, since those take a string and not a Permission.
//
// What it cannot see: a scope computed at run time. go/types has no constant
// value for fmt.Sprintf("%s:x", ns), for rbac.Permission(ns + ":x") with ns a
// variable, or for a scope read from a map, a file or the network. That is a
// documented gap, pinned by a fixture so the doc cannot drift from the code
// (TestCoreScopesOnlyDocumentedGaps). "*" and a value with no ":" are not
// scopes and are skipped.
func CoreScopesOnly(t testing.TB, module string, patterns ...string) {
	t.Helper()
	findings, checked, err := foreignScopeFindings(module, patterns, coreScopeExempt)
	if err != nil {
		t.Fatalf("CoreScopesOnly: %v", err)
		return
	}
	if checked == 0 {
		t.Fatalf("CoreScopesOnly: no non-exempt package matched %v; the check would be vacuous", patterns)
		return
	}
	if len(findings) > 0 {
		lines := make([]string, len(findings))
		for i, f := range findings {
			lines[i] = f.String()
		}
		t.Errorf("%d scope(s) in core outside the core namespace; core names core scopes only, and an app's scope on a core route is app.Options.RouteCoRequirements:\n  %s",
			len(findings), strings.Join(lines, "\n  "))
	}
}

// foreignScopeFindings is CoreScopesOnly's scan: the findings (sorted) and the
// number of packages it read.
func foreignScopeFindings(module string, patterns []string, exempt func(rel string) bool) ([]GuardFinding, int, error) {
	pkgs, err := loadTyped(patterns)
	if err != nil {
		return nil, 0, err
	}
	var out []GuardFinding
	checked := 0
	for _, p := range pkgs {
		rel := strings.TrimPrefix(strings.TrimPrefix(p.PkgPath, module), "/")
		if exempt(rel) {
			continue
		}
		checked++
		for _, f := range nonTestFiles(p) {
			out = append(out, fileForeignScopes(p, f)...)
		}
	}
	sortFindings(out)
	return out, checked, nil
}

// fileForeignScopes returns the findings of one file.
func fileForeignScopes(p *packages.Package, f *ast.File) []GuardFinding {
	info := p.TypesInfo
	var out []GuardFinding
	var reported [][2]token.Pos // flagged ranges: anything inside is already covered by the outer expression
	covered := func(n ast.Node) bool {
		for _, r := range reported {
			if n.Pos() >= r[0] && n.End() <= r[1] {
				return true
			}
		}
		return false
	}
	flag := func(n ast.Node, scope, how string) {
		reported = append(reported, [2]token.Pos{n.Pos(), n.End()})
		pos := p.Fset.Position(n.Pos())
		out = append(out, GuardFinding{File: pos.Filename, Line: pos.Line, Msg: fmt.Sprintf("%q is a scope outside the core namespace (%s)", scope, how)})
	}
	ast.Inspect(f, func(n ast.Node) bool {
		e, ok := n.(ast.Expr)
		if !ok || covered(e) {
			return true
		}
		if call, ok := e.(*ast.CallExpr); ok {
			if name := rbacFuncName(info, call); name == "ParsePermission" || name == "ParsePattern" {
				for _, a := range call.Args {
					if s, ok := constString(info, a); ok && foreignNamespace(s) {
						flag(a, s, "argument of rbac."+name)
					}
				}
				return true
			}
		}
		tv, ok := info.Types[e]
		if !ok || tv.Value == nil || !isRBACPermission(tv.Type) {
			return true
		}
		// A use of a constant declared as a Permission is reported where it
		// is declared, not at every use.
		if c := usedConst(info, e); c != nil && isRBACPermission(c.Type()) {
			return true
		}
		if s := constant.StringVal(tv.Value); foreignNamespace(s) {
			flag(e, s, "a constant of type rbac.Permission")
		}
		return true
	})
	return out
}

// foreignNamespace reports whether s is a scope or pattern of a namespace
// other than core: it has a ":" and the part before it is not "core".
func foreignNamespace(s string) bool {
	ns, _, ok := strings.Cut(s, ":")
	return ok && ns != "core"
}

// isRBACPermission reports whether t is rbac.Permission.
func isRBACPermission(t types.Type) bool {
	n, ok := types.Unalias(t).(*types.Named)
	if !ok {
		return false
	}
	o := n.Origin().Obj()
	return o.Pkg() != nil && o.Pkg().Path() == rbacPath && o.Name() == "Permission"
}

// usedConst is the constant an identifier or selector names, or nil.
func usedConst(info *types.Info, e ast.Expr) *types.Const {
	var obj types.Object
	switch x := ast.Unparen(e).(type) {
	case *ast.Ident:
		obj = info.Uses[x]
	case *ast.SelectorExpr:
		obj = info.Uses[x.Sel]
	}
	c, _ := obj.(*types.Const)
	return c
}

// rbacFuncName is the name of the rbac package-level function call invokes
// (an import alias resolved by type), or "".
func rbacFuncName(info *types.Info, call *ast.CallExpr) string {
	fn, ok := typeutil.Callee(info, call).(*types.Func)
	if !ok || fn.Pkg() == nil || fn.Pkg().Path() != rbacPath {
		return ""
	}
	if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
		return ""
	}
	return fn.Name()
}
