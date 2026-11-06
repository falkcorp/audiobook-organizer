// file: internal/config/direct_writes_test.go
// version: 1.0.0
// guid: 6dad283b-5efd-4187-9db7-2c4f10fa51ce
// last-edited: 2026-11-06

package config_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const configImportPath = "github.com/falkcorp/audiobook-organizer/internal/config"

// TestNoDirectAppConfigWrites fails on any assignment or increment whose target
// is config.AppConfig (or a field/element reached through it) outside
// internal/config. Such writes bypass config.Mutate: they race readers and are
// invisible to config.Generation() (05-config 1.12.1 item 1).
func TestNoDirectAppConfigWrites(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	skipDirs := map[string]bool{"vendor": true, "node_modules": true, "web": true, "core": true}
	configDir := filepath.Join(root, "internal", "config")

	parsed := 0
	fset := token.NewFileSet()
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == root {
				return nil
			}
			name := d.Name()
			if path == configDir || skipDirs[name] || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			t.Errorf("parse %s: %v", path, perr)
			return nil
		}
		parsed++
		local := ""
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if p != configImportPath {
				continue
			}
			local = "config"
			if imp.Name != nil {
				local = imp.Name.Name
			}
		}
		if local == "" || local == "_" || local == "." {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		report := func(lhs ast.Expr, pos token.Pos) {
			if rootsAtAppConfig(lhs, local) {
				t.Errorf("%s:%d: direct write to config.AppConfig; use config.Mutate (05-config §1.12.1)",
					filepath.ToSlash(rel), fset.Position(pos).Line)
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch s := n.(type) {
			case *ast.AssignStmt:
				if s.Tok == token.DEFINE {
					return true
				}
				for _, lhs := range s.Lhs {
					report(lhs, s.Pos())
				}
			case *ast.IncDecStmt:
				report(s.X, s.Pos())
			}
			return true
		})
		return nil
	})
	if walkErr != nil {
		t.Fatal(walkErr)
	}
	if parsed < 100 {
		t.Fatalf("parsed only %d .go files; the directory walk is broken", parsed)
	}
}

// rootsAtAppConfig reports whether e, after stripping selectors, index
// expressions, dereferences and parentheses, is the selector <local>.AppConfig.
func rootsAtAppConfig(e ast.Expr, local string) bool {
	for {
		switch x := e.(type) {
		case *ast.ParenExpr:
			e = x.X
		case *ast.StarExpr:
			e = x.X
		case *ast.IndexExpr:
			e = x.X
		case *ast.SelectorExpr:
			if id, ok := x.X.(*ast.Ident); ok && id.Name == local && x.Sel.Name == "AppConfig" {
				return true
			}
			e = x.X
		default:
			return false
		}
	}
}
