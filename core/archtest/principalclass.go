// file: core/archtest/principalclass.go
// version: 0.1.0
// guid: 6b0e2d94-3c7f-4a18-9e52-d81f4c6a07b3
// last-edited: 2026-11-22

package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// PrincipalClassFromRegistry fails t when a non-test Go file of the module
// rooted at dir asks a principal's Method field for its classification:
// a call of the shape <x>.Method.Interactive() or <x>.Method.MayChangeCredentials(),
// other than in the files in allowed (slash paths relative to dir).
//
// auth.Method's two predicates are core's classification of core's own
// methods only. An identity extension declares its own in its registered
// MethodSpec, which only the principal carries (auth.Principal.Interactive,
// auth.Principal.MayChangeCredentials). Asking the Method answers false for
// ext:cfaccess whatever its spec says: that is F4-08 B10y, which refused the
// Access-verified owner 403 interactive_required on every owner route. The
// shape is syntactic (no type check), so a field named Method of any type is
// caught too; an allowed entry carries the reason a site may ask the Method.
// An allowed entry that names no scanned file fails, so an exemption cannot
// outlive its file.
func PrincipalClassFromRegistry(t testing.TB, dir string, allowed []string) {
	t.Helper()
	root, err := resolveRoot(dir)
	if err != nil {
		t.Fatalf("PrincipalClassFromRegistry: %v", err)
	}
	all, err := moduleSourceFiles(root, nil)
	if err != nil {
		t.Fatalf("PrincipalClassFromRegistry: %v", err)
	}
	seen := make(map[string]bool, len(allowed))
	for _, a := range allowed {
		seen[a] = false
	}
	var files []string
	for _, f := range all {
		rel, err := filepath.Rel(root, f)
		if err != nil {
			t.Fatalf("PrincipalClassFromRegistry: %v", err)
		}
		if _, ok := seen[filepath.ToSlash(rel)]; ok {
			seen[filepath.ToSlash(rel)] = true
			continue
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		t.Fatalf("PrincipalClassFromRegistry: no non-test Go file under %s; the check would be vacuous", dir)
	}
	for _, a := range allowed {
		if !seen[a] {
			t.Errorf("PrincipalClassFromRegistry: allowed file %s is not a scanned non-test file; drop the stale exemption", a)
		}
	}
	var findings []string
	for _, f := range files {
		hits, err := methodClassCalls(f, nil)
		if err != nil {
			t.Fatalf("PrincipalClassFromRegistry: %v", err)
		}
		for _, h := range hits {
			rel, err := filepath.Rel(root, f)
			if err != nil {
				rel = f
			}
			findings = append(findings, filepath.ToSlash(rel)+":"+h)
		}
	}
	sort.Strings(findings)
	if len(findings) > 0 {
		t.Errorf("%d call(s) ask a principal's Method for its classification; ask the principal (auth.Principal.Interactive / MayChangeCredentials), which carries the registered MethodSpec:\n  %s",
			len(findings), strings.Join(findings, "\n  "))
	}
}

// methodClassCalls parses one file (src when non-nil, else the file at path)
// and returns "line: <x>.Method.<Pred>()" for every banned call.
func methodClassCalls(path string, src any) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (sel.Sel.Name != "Interactive" && sel.Sel.Name != "MayChangeCredentials") {
			return true
		}
		field, ok := sel.X.(*ast.SelectorExpr)
		if !ok || field.Sel.Name != "Method" {
			return true
		}
		out = append(out, fmt.Sprintf("%d: .Method.%s()", fset.Position(call.Pos()).Line, sel.Sel.Name))
		return true
	})
	return out, nil
}
