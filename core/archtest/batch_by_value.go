// file: core/archtest/batch_by_value.go
// version: 1.0.0
// guid: ae675bac-a470-401e-9cc1-dbbb18c73ac4
// last-edited: 2026-11-07

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

// storagePath is the import path whose Batch is checked.
const storagePath = "github.com/falkcorp/mediacore/storage"

// NoBatchByValue fails t when a non-test file of a package under dir matching
// patterns (nil: ./...) takes or holds a storage.Batch by value (COR2-12): a
// parameter, result, receiver, field, variable, element, map key or value,
// channel element, conversion or composite literal of type storage.Batch
// rather than *storage.Batch. Batch embeds a noCopy marker, so go vet's
// copylocks flags most copies; this check also catches the declarations vet
// does not see, such as a field or result never copied in the package
// itself. It reads every file of the tag union.
func NoBatchByValue(t testing.TB, dir string, patterns ...string) {
	t.Helper()
	pkgs, err := Load(LoadOptions{Dir: dir, Patterns: patternsOrAll(patterns), TagSets: TagSets()})
	if err != nil {
		t.Fatalf("NoBatchByValue: %v", err)
	}
	var files []string
	for _, p := range pkgs {
		files = append(files, p.GoFiles...)
	}
	if len(files) == 0 {
		t.Fatalf("NoBatchByValue: no non-test Go file under %s %v; the check would be vacuous", dir, patterns)
	}
	findings, err := NoBatchByValueFindings(files)
	if err != nil {
		t.Fatalf("NoBatchByValue: %v", err)
	}
	if len(findings) > 0 {
		t.Errorf("%d storage.Batch value(s); pass and hold *storage.Batch (COR2-12):\n  %s",
			len(findings), strings.Join(findings, "\n  "))
	}
}

// NoBatchByValueFindings parses files and returns one line per use of
// storage.Batch as a value type, sorted. Inside package storage itself the
// bare identifier Batch is checked.
func NoBatchByValueFindings(files []string) ([]string, error) {
	fset := token.NewFileSet()
	var out []string
	for _, f := range uniq(append([]string(nil), files...)) {
		af, err := parser.ParseFile(fset, f, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %v", f, err)
		}
		isBatch := batchMatcher(af)
		if isBatch == nil {
			continue
		}
		ast.Inspect(af, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.TypeSpec:
				// The declaration `type Batch struct{...}`: check only its body.
				ast.Inspect(x.Type, inspectBatch(fset, isBatch, &out))
				return false
			}
			return inspectBatch(fset, isBatch, &out)(n)
		})
	}
	sort.Strings(out)
	return out, nil
}

// inspectBatch reports every Batch reference that is not behind a pointer,
// an address-of composite literal or new().
func inspectBatch(fset *token.FileSet, isBatch func(ast.Expr) bool, out *[]string) func(ast.Node) bool {
	var visit func(ast.Node) bool
	visit = func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.StarExpr:
			if isBatch(x.X) {
				return false
			}
		case *ast.UnaryExpr:
			if cl, ok := x.X.(*ast.CompositeLit); ok && x.Op == token.AND && isBatch(cl.Type) {
				for _, e := range cl.Elts {
					ast.Inspect(e, visit)
				}
				return false
			}
		case *ast.CallExpr:
			if id, ok := x.Fun.(*ast.Ident); ok && id.Name == "new" && len(x.Args) == 1 && isBatch(x.Args[0]) {
				return false
			}
		case *ast.SelectorExpr:
			if isBatch(x) {
				*out = append(*out, fmt.Sprintf("%s: storage.Batch by value", fset.Position(x.Pos())))
				return false
			}
			ast.Inspect(x.X, visit) // never the selected name: b.Batch is a field or method
			return false
		case *ast.Ident:
			if isBatch(x) {
				*out = append(*out, fmt.Sprintf("%s: storage.Batch by value", fset.Position(x.Pos())))
			}
		}
		return true
	}
	return visit
}

// batchMatcher returns the test for a Batch reference in af: the selector
// <alias>.Batch where alias imports storage, or the bare identifier Batch in
// package storage. nil when the file cannot name the type.
func batchMatcher(af *ast.File) func(ast.Expr) bool {
	if af.Name.Name == "storage" && fileIsStoragePackage(af) {
		return func(e ast.Expr) bool {
			id, ok := e.(*ast.Ident)
			return ok && id.Name == "Batch"
		}
	}
	alias := ""
	for _, imp := range af.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil || p != storagePath {
			continue
		}
		alias = "storage"
		if imp.Name != nil {
			alias = imp.Name.Name
		}
	}
	if alias == "" || alias == "_" {
		return nil
	}
	return func(e ast.Expr) bool {
		sel, ok := e.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Batch" {
			return false
		}
		id, ok := sel.X.(*ast.Ident)
		return ok && id.Name == alias
	}
}

// fileIsStoragePackage tells core's storage package from another package
// named storage: it is the one that declares type Batch.
func fileIsStoragePackage(af *ast.File) bool {
	for _, imp := range af.Imports {
		// The parser only accepts a well-formed quoted import path, so an
		// Unquote error cannot happen; such a path could not be storage's.
		if p, err := strconv.Unquote(imp.Path.Value); err == nil && p == storagePath {
			return false
		}
	}
	return true
}
