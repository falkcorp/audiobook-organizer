// file: tools/cmd/oplint/main.go
// version: 1.1.0
// guid: d0e1f2a3-b4c5-6789-3456-789abcdef012
// last-edited: 2026-10-13

// Package main implements oplint, a plugin linter. It takes directories and
// walks them. Two rules:
//
//   - imports: operations code only imports internal/operations/registry,
//     internal/database/iface, internal/auth, the stdlib and third-party
//     packages (the "walled garden").
//   - deps-store (R21, 05-PR5): a field of a struct type named Deps may not
//     be typed database.Store, nor an interface that embeds it. An op names
//     the narrow interface it calls instead (sdk-api.md §11.1).
//
// The deps-store rule is syntactic. It resolves an interface declared in the
// same directory (package), transitively; it cannot see through an interface
// declared in another package. The full rewrite onto go/analysis is 05-PR7.
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	modulePrefix    = "github.com/falkcorp/audiobook-organizer/internal/"
	databaseImport  = "github.com/falkcorp/audiobook-organizer/internal/database"
	depsStoreRuleID = "R21"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: %s <dir1> [dir2] ...\n", os.Args[0])
		os.Exit(2)
	}

	var violations []string

	for _, startDir := range os.Args[1:] {
		if err := walkDir(startDir, &violations); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	}

	if len(violations) > 0 {
		for _, v := range violations {
			fmt.Println(v)
		}
		os.Exit(1)
	}
}

// walkDir lints every directory under dir, one package at a time.
func walkDir(dir string, violations *[]string) error {
	return filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return nil
		}
		return checkPackage(path, violations)
	})
}

// checkPackage parses a directory's non-test Go files and runs both rules.
func checkPackage(dir string, violations *[]string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		f, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			// Skip files that can't be parsed (build tags, invalid syntax during linting)
			continue
		}
		files = append(files, f)
		checkImports(path, f, violations)
	}
	*violations = append(*violations, checkDepsStore(fset, files)...)
	return nil
}

func checkImports(path string, f *ast.File, violations *[]string) {
	for _, imp := range f.Imports {
		importPath := strings.Trim(imp.Path.Value, `"`)

		// Allow stdlib (no dots) and third-party packages (have dots but don't start with "github.com/jdfalk/...")
		if !strings.Contains(importPath, "/") {
			// Stdlib — allowed
			continue
		}

		// Reject imports from the falkcorp/audiobook-organizer internal/ except allowed ones.
		if strings.HasPrefix(importPath, modulePrefix) {
			if importPath == "github.com/falkcorp/audiobook-organizer/internal/operations/registry" ||
				importPath == "github.com/falkcorp/audiobook-organizer/internal/database/iface" ||
				importPath == "github.com/falkcorp/audiobook-organizer/internal/auth" {
				// Allowed
				continue
			}
			*violations = append(*violations, fmt.Sprintf("%s: imports forbidden internal package: %s", path, importPath))
		}
	}
}

// databaseAlias returns the name f refers to internal/database by, or "".
func databaseAlias(f *ast.File) string {
	for _, imp := range f.Imports {
		if strings.Trim(imp.Path.Value, `"`) != databaseImport {
			continue
		}
		if imp.Name != nil {
			if imp.Name.Name == "_" || imp.Name.Name == "." {
				return ""
			}
			return imp.Name.Name
		}
		return "database"
	}
	return ""
}

// isDatabaseStore reports whether expr names database.Store in file f.
func isDatabaseStore(expr ast.Expr, alias string) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || alias == "" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == alias && sel.Sel.Name == "Store"
}

// checkDepsStore reports every field of a struct type named Deps whose type
// is database.Store, an inline interface embedding it, or an interface
// declared in this package that embeds it (transitively).
func checkDepsStore(fset *token.FileSet, files []*ast.File) []string {
	type ifaceDecl struct {
		embeds []ast.Expr
		alias  string
	}
	ifaces := map[string]ifaceDecl{}
	type depsDecl struct {
		st    *ast.StructType
		alias string
	}
	var deps []depsDecl
	for _, f := range files {
		alias := databaseAlias(f)
		ast.Inspect(f, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			switch t := ts.Type.(type) {
			case *ast.InterfaceType:
				var embeds []ast.Expr
				for _, m := range t.Methods.List {
					if len(m.Names) == 0 {
						embeds = append(embeds, m.Type)
					}
				}
				ifaces[ts.Name.Name] = ifaceDecl{embeds: embeds, alias: alias}
			case *ast.StructType:
				if ts.Name.Name == "Deps" {
					deps = append(deps, depsDecl{st: t, alias: alias})
				}
			}
			return true
		})
	}

	// embedsStore resolves an embedded type: database.Store itself, an inline
	// interface, or a same-package interface name (visited guards cycles).
	var embedsStore func(expr ast.Expr, alias string, visited map[string]bool) bool
	embedsStore = func(expr ast.Expr, alias string, visited map[string]bool) bool {
		switch e := expr.(type) {
		case *ast.StarExpr:
			return embedsStore(e.X, alias, visited)
		case *ast.SelectorExpr:
			return isDatabaseStore(e, alias)
		case *ast.InterfaceType:
			for _, m := range e.Methods.List {
				if len(m.Names) == 0 && embedsStore(m.Type, alias, visited) {
					return true
				}
			}
		case *ast.Ident:
			decl, ok := ifaces[e.Name]
			if !ok || visited[e.Name] {
				return false
			}
			visited[e.Name] = true
			for _, em := range decl.embeds {
				if embedsStore(em, decl.alias, visited) {
					return true
				}
			}
		}
		return false
	}

	var out []string
	for _, d := range deps {
		for _, field := range d.st.Fields.List {
			if !embedsStore(field.Type, d.alias, map[string]bool{}) {
				continue
			}
			names := make([]string, 0, len(field.Names))
			for _, n := range field.Names {
				names = append(names, n.Name)
			}
			if len(names) == 0 {
				names = append(names, "(embedded)")
			}
			pos := fset.Position(field.Pos())
			out = append(out, fmt.Sprintf("%s:%d: %s: Deps.%s is typed database.Store or an interface embedding it; declare the narrow interface the op calls",
				pos.Filename, pos.Line, depsStoreRuleID, strings.Join(names, ", ")))
		}
	}
	sort.Strings(out)
	return out
}
