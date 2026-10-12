// file: pprof_guard_test.go
// version: 1.0.0
// guid: 0e6b3f9a-8d24-4c71-a5b8-61f2d7c94e03
// last-edited: 2026-10-11

package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestPprofGuard_NothingServesDefaultServeMux pins the premise the pprof build
// relies on: net/http/pprof registers its handlers on http.DefaultServeMux as an
// import side effect, which is only inert while nothing in the binary serves
// DefaultServeMux. This parses every non-test Go file of the main module and
// fails on any way of serving or touching it:
//
//   - http.ListenAndServe / ListenAndServeTLS / Serve / ServeTLS with a nil
//     handler (a nil handler means DefaultServeMux);
//   - an http.Server literal with `Handler: nil` or no Handler field at all
//     (the zero value also means DefaultServeMux);
//   - any reference to http.DefaultServeMux (wrapping it hands it to a server).
//
// It is untagged so it runs in plain `go test`, not just the pprof build.
func TestPprofGuard_NothingServesDefaultServeMux(t *testing.T) {
	skipDirs := map[string]bool{
		".git": true, "node_modules": true, "web": true, "vendor": true,
		".worktrees": true, ".standards": true, "testdata": true, "dist": true,
	}
	var violations []string
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != "." && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			// A nested module is not part of the main module.
			if path != "." {
				if _, statErr := os.Stat(filepath.Join(path, "go.mod")); statErr == nil {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		violations = append(violations, scanForDefaultServeMux(t, path)...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range violations {
		t.Errorf("serves http.DefaultServeMux (net/http/pprof registers on it): %s", v)
	}
}

// scanForDefaultServeMux returns one "file:line: what" entry per violation.
func scanForDefaultServeMux(t *testing.T, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	httpName := ""
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		if p != "net/http" {
			continue
		}
		httpName = "http"
		if imp.Name != nil {
			httpName = imp.Name.Name
		}
	}
	if httpName == "" || httpName == "_" || httpName == "." {
		return nil
	}
	isHTTP := func(e ast.Expr, name string) bool {
		sel, ok := e.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != name {
			return false
		}
		id, ok := sel.X.(*ast.Ident)
		return ok && id.Name == httpName
	}
	isNil := func(e ast.Expr) bool {
		id, ok := e.(*ast.Ident)
		return ok && id.Name == "nil"
	}
	// handlerArg is the index of the handler argument per package-level func.
	handlerArg := map[string]int{"ListenAndServe": 1, "ListenAndServeTLS": 3, "Serve": 1, "ServeTLS": 1}

	var out []string
	report := func(n ast.Node, what string) {
		out = append(out, fset.Position(n.Pos()).String()+": "+what)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			if isHTTP(x, "DefaultServeMux") {
				report(x, "reference to http.DefaultServeMux")
			}
		case *ast.CallExpr:
			for name, idx := range handlerArg {
				if isHTTP(x.Fun, name) && idx < len(x.Args) && isNil(x.Args[idx]) {
					report(x, "http."+name+" with a nil handler")
				}
			}
		case *ast.CompositeLit:
			if !isHTTP(x.Type, "Server") {
				return true
			}
			hasHandler := false
			for _, el := range x.Elts {
				kv, ok := el.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "Handler" {
					hasHandler = true
					if isNil(kv.Value) {
						report(kv, "http.Server with Handler: nil")
					}
				}
			}
			if !hasHandler {
				report(x, "http.Server literal without a Handler (zero value serves DefaultServeMux)")
			}
		}
		return true
	})
	return out
}

// The scanner must actually catch each shape, or the guard above proves nothing.
func TestPprofGuard_ScannerCatchesEachShape(t *testing.T) {
	src := map[string]string{
		"listen":     `package p; import "net/http"; func f() { http.ListenAndServe(":1", nil) }`,
		"listentls":  `package p; import "net/http"; func f() { http.ListenAndServeTLS(":1", "c", "k", nil) }`,
		"serve":      `package p; import "net/http"; func f() { var l any; _ = l; http.Serve(nil, nil) }`,
		"nilhandler": `package p; import "net/http"; var s = &http.Server{Handler: nil}`,
		"nohandler":  `package p; import "net/http"; var s = &http.Server{Addr: ":1"}`,
		"wrap":       `package p; import "net/http"; var h = http.StripPrefix("/x", http.DefaultServeMux)`,
		"alias":      `package p; import h "net/http"; var s = &h.Server{}`,
	}
	for name, code := range src {
		path := filepath.Join(t.TempDir(), name+".go")
		writeTestFile(t, path, code)
		if got := scanForDefaultServeMux(t, path); len(got) == 0 {
			t.Errorf("%s: scanner reported nothing for %s", name, code)
		}
	}
	ok := filepath.Join(t.TempDir(), "ok.go")
	writeTestFile(t, ok, `package p; import "net/http"; var m = http.NewServeMux(); var s = &http.Server{Handler: m}; func f() { http.ListenAndServe(":1", m) }`)
	if got := scanForDefaultServeMux(t, ok); len(got) != 0 {
		t.Errorf("clean file flagged: %v", got)
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
