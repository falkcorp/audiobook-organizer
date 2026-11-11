// file: core/archtest/nolowerhashcost.go
// version: 1.0.0
// guid: 9435cb81-2185-46de-a156-be772126bf0f
// last-edited: 2026-11-10

package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// passwordImportPath is the package whose Options carries the test-only cost
// fields.
const passwordImportPath = "github.com/falkcorp/mediacore/auth/password"

// NoLoweredHashCost fails t when a non-test Go file of the module rooted at
// dir, outside the directories testSupport reports (paths relative to dir;
// nil: none), builds a password.Options literal that sets Time, MemoryKiB or
// Compute, or sets its fields by position. Those fields lower the argon2id
// cost every password is written at and belong to tests only; a production
// file that set them would make every stored password cheap to guess. It
// walks the module's directories (moduleSourceFiles), so a file behind any
// build tag and the root main package are read too. password.New also panics
// on those fields outside a test binary; this guard catches the literal
// before it can reach one. An Options value built by assigning its fields one
// by one is not resolved without type information and is left to that
// runtime guard. Replaces ABO's TestHashCostSeamStaysInTests, whose seam
// (absauth's SetHashCostForTest) became these Options fields in F4-02.
func NoLoweredHashCost(t testing.TB, dir string, testSupport func(rel string) bool) {
	t.Helper()
	root, err := resolveRoot(dir)
	if err != nil {
		t.Fatalf("NoLoweredHashCost: %v", err)
	}
	files, err := moduleSourceFiles(root, testSupport)
	if err != nil {
		t.Fatalf("NoLoweredHashCost: %v", err)
	}
	if len(files) == 0 {
		t.Fatalf("NoLoweredHashCost: no non-test Go file under %s; the check would be vacuous", dir)
	}
	findings, err := NoLoweredHashCostFindings(files)
	if err != nil {
		t.Fatalf("NoLoweredHashCost: %v", err)
	}
	if len(findings) > 0 {
		t.Errorf("%d production password.Options literal(s) lower the hash cost; set Time, MemoryKiB and Compute from tests only:\n  %s",
			len(findings), strings.Join(findings, "\n  "))
	}
}

// NoLoweredHashCostFindings parses files and returns "file:line: field" for
// every password.Options literal that sets a cost field, sorted.
func NoLoweredHashCostFindings(files []string) ([]string, error) {
	var out []string
	for _, f := range uniq(append([]string(nil), files...)) {
		hits, err := loweredHashCost(f, nil)
		if err != nil {
			return nil, err
		}
		for _, h := range hits {
			out = append(out, f+":"+h)
		}
	}
	sort.Strings(out)
	return out, nil
}

// loweredHashCost returns "line: Options.Field" for each cost field a
// password.Options literal in one source sets ("line: Options{positional}"
// for a positional literal). The package is resolved by import path, so an
// alias or a dot import is caught and an unrelated Options type is not.
func loweredHashCost(filename string, src any) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %v", filename, err)
	}
	names := map[string]bool{}
	dotImport := false
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || path != passwordImportPath {
			continue
		}
		switch {
		case imp.Name == nil:
			names["password"] = true
		case imp.Name.Name == ".":
			dotImport = true
		case imp.Name.Name != "_":
			names[imp.Name.Name] = true
		}
	}
	if len(names) == 0 && !dotImport {
		return nil, nil
	}
	isOptions := func(e ast.Expr) bool {
		switch x := e.(type) {
		case *ast.SelectorExpr:
			id, ok := x.X.(*ast.Ident)
			return ok && names[id.Name] && x.Sel.Name == "Options"
		case *ast.Ident:
			return dotImport && x.Name == "Options"
		}
		return false
	}
	var hits []string
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok || !isOptions(lit.Type) {
			return true
		}
		for _, el := range lit.Elts {
			kv, ok := el.(*ast.KeyValueExpr)
			if !ok {
				hits = append(hits, strconv.Itoa(fset.Position(el.Pos()).Line)+": Options{positional}")
				break
			}
			if key, ok := kv.Key.(*ast.Ident); ok {
				switch key.Name {
				case "Time", "MemoryKiB", "Compute":
					hits = append(hits, strconv.Itoa(fset.Position(kv.Pos()).Line)+": Options."+key.Name)
				}
			}
		}
		return true
	})
	return hits, nil
}
