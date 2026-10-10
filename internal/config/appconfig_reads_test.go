// file: internal/config/appconfig_reads_test.go
// version: 1.1.0
// guid: 7b1d4e92-3c58-4a0f-9e26-d5a8c3f10b74
// last-edited: 2026-10-10

package config

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// appConfigReadBaseline is the number of qualified uses of the global
// config.AppConfig (the selector `<alias>.AppConfig`, which covers both
// config.AppConfig.Field and a bare config.AppConfig / &config.AppConfig)
// in non-test Go files under internal/ and cmd/, outside internal/config.
//
// RATCHET RULES: the baseline may only be lowered, by hand, in the same PR
// that removes the uses. The test never rewrites it. 07-M2, 07-M3 and 07-M5
// are the expected sources of decreases. Uses inside internal/config are out
// of scope (reported informationally) until 07-M1 makes this package a leaf.
//
// Build-constrained files (for example bench-tagged ones) are counted too,
// because parser.ParseFile ignores build tags.
//
// Measured at origin/main 005f0810d with this instrument: 625 uses in 183
// files; 8 assignment or inc/dec targets as first measured; 10 unqualified uses inside
// internal/config. This is deliberately
// not the 636 from the A.5 measurement, whose command was never printed; plain
// greps give different numbers than an AST walk (grep 'config\.AppConfig\.'
// counts lines, this counts selector nodes and follows import aliases).
const appConfigReadBaseline = 625

const appConfigImportPath = "github.com/falkcorp/audiobook-organizer/internal/config"

var appConfigSkipDirs = map[string]bool{
	"testdata": true, "vendor": true, "node_modules": true, "mocks": true,
}

type appConfigScan struct {
	files        int
	outside      int            // gated: qualified uses outside internal/config
	bare         int            // informational: address-of / value uses outside internal/config
	writes       int            // informational: outside assignment or inc/dec targets rooted at AppConfig
	insideConfig int            // informational: unqualified AppConfig uses inside internal/config
	perFile      map[string]int // outside uses per file
}

// appConfigLocalName returns the name under which f imports the config
// package, or "" if it does not (or imports it as _ or .).
func appConfigLocalName(f *ast.File) string {
	for _, imp := range f.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil || p != appConfigImportPath {
			continue
		}
		if imp.Name == nil {
			return "config"
		}
		if imp.Name.Name == "_" || imp.Name.Name == "." {
			return ""
		}
		return imp.Name.Name
	}
	return ""
}

func isAppConfigSel(n ast.Node, alias string) bool {
	se, ok := n.(*ast.SelectorExpr)
	if !ok || se.Sel.Name != "AppConfig" {
		return false
	}
	id, ok := se.X.(*ast.Ident)
	return ok && id.Name == alias
}

// appConfigUses classifies the qualified AppConfig selectors of one file.
type appConfigUses struct {
	total  int // every `<alias>.AppConfig` selector (the gated number)
	bare   int // address-of / value uses: the selector is not the X of a further selector
	writes int // assignment or inc/dec targets whose selector chain is rooted at AppConfig
}

// appConfigWriteTarget reports whether e, followed down through field
// selectors, indexing and parens, is rooted at `<alias>.AppConfig`. An
// AppConfig selector that only appears inside an index expression
// (m[config.AppConfig.K] = v) is a read, not a write, because the walk goes
// down X and never into the index.
func appConfigWriteTarget(e ast.Expr, alias string) bool {
	for {
		if isAppConfigSel(e, alias) {
			return true
		}
		switch x := e.(type) {
		case *ast.SelectorExpr:
			e = x.X
		case *ast.IndexExpr:
			e = x.X
		case *ast.ParenExpr:
			e = x.X
		default:
			return false
		}
	}
}

// classifyAppConfigUses counts the qualified AppConfig selectors in f.
func classifyAppConfigUses(f *ast.File, alias string) appConfigUses {
	var u appConfigUses
	var stack []ast.Node
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		if isAppConfigSel(n, alias) {
			u.total++
			isField := false
			if len(stack) > 0 {
				if p, ok := stack[len(stack)-1].(*ast.SelectorExpr); ok && p.X == n {
					isField = true
				}
			}
			if !isField {
				u.bare++
			}
		}
		var lhs []ast.Expr
		switch s := n.(type) {
		case *ast.AssignStmt:
			lhs = s.Lhs
		case *ast.IncDecStmt:
			lhs = []ast.Expr{s.X}
		}
		for _, e := range lhs {
			if appConfigWriteTarget(e, alias) {
				u.writes++
			}
		}
		stack = append(stack, n)
		return true
	})
	return u
}

// countInsideConfig counts unqualified identifier uses of AppConfig in a file
// of package config (informational only).
func countInsideConfig(f *ast.File) int {
	n := 0
	ast.Inspect(f, func(node ast.Node) bool {
		if id, ok := node.(*ast.Ident); ok && id.Name == "AppConfig" {
			n++
		}
		return true
	})
	return n
}

func scanAppConfigReads(t *testing.T, root string) appConfigScan {
	t.Helper()
	res := appConfigScan{perFile: map[string]int{}}
	for _, top := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(path string, de fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if de.IsDir() {
				if appConfigSkipDirs[de.Name()] || strings.HasPrefix(de.Name(), ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			res.files++
			f, perr := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
			if perr != nil {
				t.Errorf("parse %s: %v", rel, perr)
				return nil
			}
			if strings.HasPrefix(rel, "internal/config/") {
				res.insideConfig += countInsideConfig(f)
				return nil
			}
			alias := appConfigLocalName(f)
			if alias == "" {
				return nil
			}
			u := classifyAppConfigUses(f, alias)
			if u.total > 0 {
				res.perFile[rel] = u.total
				res.outside += u.total
				res.bare += u.bare
				res.writes += u.writes
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", top, err)
		}
	}
	return res
}

func appConfigRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the package directory; cannot locate the repo root")
		}
		dir = parent
	}
}

// TestAppConfigDirectReadRatchet fails only when the number of direct
// config.AppConfig uses outside internal/config rises above the baseline.
// This is source scanning only: it never imports the scanned packages and
// never touches the shared AppConfig value.
func TestAppConfigDirectReadRatchet(t *testing.T) {
	res := scanAppConfigReads(t, appConfigRepoRoot(t))

	// Positive controls: a walker that scanned nothing, or an alias resolver
	// that matched nothing, would pass forever.
	if res.files < 500 {
		t.Fatalf("scanned only %d Go files; the walker is broken, not the code", res.files)
	}
	if res.outside <= 100 {
		t.Fatalf("counted only %d AppConfig uses; the alias resolver is broken, not the code", res.outside)
	}

	t.Logf("files scanned=%d; direct AppConfig uses outside internal/config=%d (in %d files), of which address-of / value uses (not config.AppConfig.Field)=%d; write targets=%d; unqualified uses inside internal/config (informational, not gated)=%d",
		res.files, res.outside, len(res.perFile), res.bare, res.writes, res.insideConfig)

	if res.outside > appConfigReadBaseline {
		type kv struct {
			file string
			n    int
		}
		var top []kv
		for f, n := range res.perFile {
			top = append(top, kv{f, n})
		}
		sort.Slice(top, func(i, j int) bool {
			if top[i].n != top[j].n {
				return top[i].n > top[j].n
			}
			return top[i].file < top[j].file
		})
		if len(top) > 5 {
			top = top[:5]
		}
		var b strings.Builder
		for _, e := range top {
			fmt.Fprintf(&b, "\n\t%s: %d", e.file, e.n)
		}
		t.Fatalf("direct config.AppConfig uses outside internal/config rose to %d (baseline %d). "+
			"Read config.Snapshot() once at the top of the function, or take the value as a parameter. "+
			"Files with the most uses:%s", res.outside, appConfigReadBaseline, b.String())
	}
	if res.outside < appConfigReadBaseline {
		t.Logf("NOTE (not a failure): uses fell to %d, below the baseline %d; lower appConfigReadBaseline to %d in this PR",
			res.outside, appConfigReadBaseline, res.outside)
	}
}

func TestClassifyAppConfigUses(t *testing.T) {
	const src = `package x

import cfg "github.com/falkcorp/audiobook-organizer/internal/config"

func f(m map[string]int) {
	_ = cfg.AppConfig.RootDir // field read
	cfg.AppConfig.RootDir = "a" // field write
	p := &cfg.AppConfig // address-of
	c := cfg.AppConfig // value copy
	m[cfg.AppConfig.RootDir] = 1 // index expression LHS: a read
	cfg.AppConfig.Count++ // inc/dec write
	_, _ = p, c
}
`
	f, err := parser.ParseFile(token.NewFileSet(), "x.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	if got := appConfigLocalName(f); got != "cfg" {
		t.Fatalf("alias = %q, want cfg", got)
	}
	got := classifyAppConfigUses(f, "cfg")
	want := appConfigUses{total: 6, bare: 2, writes: 2}
	if got != want {
		t.Fatalf("classify = %+v, want %+v", got, want)
	}
	if other := classifyAppConfigUses(f, "config"); other.total != 0 {
		t.Fatalf("wrong alias matched %d uses", other.total)
	}
}
