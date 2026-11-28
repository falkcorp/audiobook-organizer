// file: core/archtest/corescopes.go
// version: 1.0.0
// guid: 108df91a-c621-4d4a-abfe-0f22f6ea0909
// last-edited: 2026-11-28

package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// coreScopeConstructors are the rbac functions that take scope strings and
// build or parse a Permission or a Requirement from them.
func coreScopeConstructors() map[string]bool {
	return map[string]bool{"Need": true, "AllOf": true, "AnyOf": true, "ParsePermission": true, "ParsePattern": true}
}

// coreScopeExempt reports the directories CoreScopesOnly does not read: the
// test-support packages (CoreTestSupport: coretest, archtest, every
// <pkg>/<pkg>test suite), which fixture scopes of their own, and examples,
// which are apps built on core (hello names hello:admin) and not core. Both
// are listed in the PR that added the rule; a new entry is a decision.
func coreScopeExempt(rel string) bool {
	return CoreTestSupport(rel) || rel == "examples" || strings.HasPrefix(rel, "examples/")
}

// CoreScopesOnly fails t when a non-test Go file of core, the module rooted at
// dir, builds an rbac.Permission, rbac.Need, rbac.AllOf or rbac.AnyOf from a
// string literal whose namespace is not "core" (W6, 05-rbac §6.1).
//
// Core names no scope of an app: a core route requires core's own scopes (R6,
// httpapi checkPerm), and when an app needs one of its own on a core route it
// says so itself, in app.Options.RouteCoRequirements, where the router ANDs it
// on. A core package that named books:integrations.manage would put one app's
// vocabulary into every app's binary and bypass the rule that keeps the
// co-requirement the app's to declare, so the scan is how that stays true.
//
// The shape is syntactic (no type check) and covers every place a scope string
// literal becomes a Permission in non-test code:
//
//   - an argument of Need, AllOf, AnyOf, ParsePermission or ParsePattern
//     (rbac.X, or X inside package rbac);
//   - a conversion Permission("ns:action");
//   - the value of a const or var declared as Permission, whatever its form
//     (const X rbac.Permission = "ns:action", var P = []rbac.Permission{...});
//   - an element of a []Permission or map-of-Permission composite literal, and
//     the Name of a PermissionSpec literal.
//
// "*" and any literal with no ":" are not scopes and are skipped. A scope held
// in a variable the scan cannot see through (a string built at run time) is
// not caught: every real route and task declaration in core names its scope as
// a typed constant or a literal in one of the forms above.
func CoreScopesOnly(t testing.TB, dir string) {
	t.Helper()
	root, err := resolveRoot(dir)
	if err != nil {
		t.Fatalf("CoreScopesOnly: %v", err)
	}
	files, err := moduleSourceFiles(root, coreScopeExempt)
	if err != nil {
		t.Fatalf("CoreScopesOnly: %v", err)
	}
	if len(files) == 0 {
		t.Fatalf("CoreScopesOnly: no non-test Go file under %s; the check would be vacuous", dir)
	}
	var findings []string
	for _, f := range files {
		hits, err := foreignScopeLiterals(f, nil)
		if err != nil {
			t.Fatalf("CoreScopesOnly: %v", err)
		}
		rel, err := filepath.Rel(root, f)
		if err != nil {
			rel = f
		}
		for _, h := range hits {
			findings = append(findings, filepath.ToSlash(rel)+":"+h)
		}
	}
	sort.Strings(findings)
	if len(findings) > 0 {
		t.Errorf("%d scope literal(s) in core outside the core namespace; core names core scopes only, and an app's scope on a core route is app.Options.RouteCoRequirements:\n  %s",
			len(findings), strings.Join(findings, "\n  "))
	}
}

// foreignScopeLiterals parses one file (src when non-nil, else the file at
// path) and returns "line: "literal"" for every scope literal in the forms
// CoreScopesOnly lists whose namespace is not core.
func foreignScopeLiterals(path string, src any) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	var out []string
	seen := map[token.Pos]bool{}
	check := func(e ast.Expr) {
		lit, ok := e.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING || seen[lit.Pos()] {
			return
		}
		s, err := strconv.Unquote(lit.Value)
		if err != nil {
			return
		}
		if ns, _, ok := strings.Cut(s, ":"); ok && ns != "core" {
			seen[lit.Pos()] = true
			out = append(out, fmt.Sprintf("%d: %s", fset.Position(lit.Pos()).Line, lit.Value))
		}
	}
	// A bare Need or Permission names rbac's only inside package rbac; in any
	// other package it is a function of that package's own.
	inRBAC := f.Name.Name == "rbac"
	name := func(e ast.Expr) string { return rbacName(e, inRBAC) }
	// values checks every string literal of a Permission-typed declaration's
	// value, descending into its composite literals (a map's keys are not
	// scopes and are skipped).
	var values func(e ast.Expr)
	values = func(e ast.Expr) {
		switch e := e.(type) {
		case *ast.CompositeLit:
			for _, el := range e.Elts {
				values(el)
			}
		case *ast.KeyValueExpr:
			values(e.Value)
		default:
			check(e)
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			switch fn := name(n.Fun); {
			case coreScopeConstructors()[fn]:
				for _, a := range n.Args {
					check(a)
				}
			case fn == "Permission" && len(n.Args) == 1:
				check(n.Args[0])
			}
		case *ast.ValueSpec:
			if isPermissionType(n.Type, inRBAC) {
				for _, v := range n.Values {
					values(v)
				}
			}
		case *ast.CompositeLit:
			switch {
			case isPermissionType(n.Type, inRBAC):
				values(n)
			case name(n.Type) == "PermissionSpec":
				for _, el := range n.Elts {
					if kv, ok := el.(*ast.KeyValueExpr); ok {
						if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "Name" {
							check(kv.Value)
						}
					}
				}
			}
		}
		return true
	})
	return out, nil
}

// rbacName is the identifier an expression names as rbac's: Name for rbac.Name,
// and for a bare Name when bare is set (the file is in package rbac); "" for
// anything else.
func rbacName(e ast.Expr, bare bool) string {
	switch e := e.(type) {
	case *ast.SelectorExpr:
		if x, ok := e.X.(*ast.Ident); ok && x.Name == "rbac" {
			return e.Sel.Name
		}
	case *ast.Ident:
		if bare {
			return e.Name
		}
	}
	return ""
}

// isPermissionType reports Permission, []Permission, [N]Permission and
// map[K]Permission (rbac-qualified, or bare inside package rbac): the types
// whose literal values are scopes.
func isPermissionType(e ast.Expr, bare bool) bool {
	switch e := e.(type) {
	case *ast.ArrayType:
		return isPermissionType(e.Elt, bare)
	case *ast.MapType:
		return isPermissionType(e.Value, bare)
	case nil:
		return false
	}
	return rbacName(e, bare) == "Permission"
}
