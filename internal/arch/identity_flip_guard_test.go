// file: internal/arch/identity_flip_guard_test.go
// version: 0.1.0
// guid: 5e19d2a7-6b30-4c8f-9d41-a07c3e8b1f52
// last-edited: 2026-11-21

package arch

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The identity flip's one predicate (internal/books/identity_flip.go) has a
// backing variable and a test-only selector. Production turns the flip on only
// at B10g, by editing the predicate itself; until then nothing outside a test
// may flip it, or ABO would serve under core identity before the switch is
// reviewed. Two rules, both read off the syntax:
//
//   - no non-test file calls or names SetIdentityFlipForTest, except its own
//     declaration;
//   - no file in internal/books but identity_flip.go names identityFlip (the
//     backing variable), and inside it only identityOn reads it and only
//     SetIdentityFlipForTest writes it.

const (
	identityFlipFile     = "internal/books/identity_flip.go"
	identityFlipSelector = "SetIdentityFlipForTest"
	identityFlipVar      = "identityFlip"
)

// identityFlipProblems judges the non-test Go files of the module (path
// relative to the repository root, slash-separated, to source).
func identityFlipProblems(files map[string][]byte) []string {
	var out []string
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	fset := token.NewFileSet()
	sawFlipFile := false
	for _, p := range paths {
		f, err := parser.ParseFile(fset, p, files[p], parser.SkipObjectResolution)
		if err != nil {
			out = append(out, fmt.Sprintf("%s: unparsable: %v", p, err))
			continue
		}
		if p == identityFlipFile {
			sawFlipFile = true
			out = append(out, identityFlipFileProblems(f)...)
			continue
		}
		inBooks := strings.HasPrefix(p, "internal/books/") && strings.Count(strings.TrimPrefix(p, "internal/books/"), "/") == 0
		ast.Inspect(f, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			switch {
			case id.Name == identityFlipSelector:
				out = append(out, fmt.Sprintf("%s:%d: names %s, the test-only selector of the identity predicate: ABO would run under core identity outside a test before B10g",
					p, fset.Position(id.Pos()).Line, identityFlipSelector))
			case inBooks && id.Name == identityFlipVar:
				out = append(out, fmt.Sprintf("%s:%d: names %s, the predicate's backing variable: only %s may touch it",
					p, fset.Position(id.Pos()).Line, identityFlipVar, identityFlipFile))
			}
			return true
		})
	}
	if !sawFlipFile {
		out = append(out, identityFlipFile+" is missing: the identity predicate's file moved, so this guard guards nothing")
	}
	return out
}

// identityFlipFileProblems is the rule inside the predicate's own file: the
// variable is stored to (Store, Swap, CompareAndSwap) only inside the selector.
func identityFlipFileProblems(f *ast.File) []string {
	var out []string
	hasSelector := false
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fn.Name.Name == identityFlipSelector {
			hasSelector = true
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			recv, ok := sel.X.(*ast.Ident)
			if !ok || recv.Name != identityFlipVar {
				return true
			}
			switch sel.Sel.Name {
			case "Store", "Swap", "CompareAndSwap":
				if fn.Name.Name != identityFlipSelector {
					out = append(out, fmt.Sprintf("%s: %s writes %s outside %s", identityFlipFile, fn.Name.Name, identityFlipVar, identityFlipSelector))
				}
			}
			return true
		})
	}
	if !hasSelector {
		out = append(out, identityFlipFile+" declares no "+identityFlipSelector+": the guard cannot tell the selector from a stray writer")
	}
	return out
}

// TestIdentityFlipSelectorHasNoProductionCaller pins the two rules over the real
// tree.
func TestIdentityFlipSelectorHasNoProductionCaller(t *testing.T) {
	files := productionGoFiles(t)
	if probs := identityFlipProblems(files); len(probs) > 0 {
		t.Fatalf("the identity predicate's guard found %d problem(s):\n%s", len(probs), strings.Join(probs, "\n"))
	}
}

// productionGoFiles is every non-test Go file of the module, path relative to the
// repository root (slash-separated) to source: the input of the guards that read
// the syntax of the whole tree.
func productionGoFiles(t *testing.T) map[string][]byte {
	t.Helper()
	root := repoRoot(t)
	skip := map[string]bool{
		".git": true, "node_modules": true, "web": true, ".worktrees": true,
		"third_party": true, ".standards": true, "vendor": true, "testdata": true,
	}
	files := map[string][]byte{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skip[d.Name()] && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		rel, _ := filepath.Rel(root, path)
		files[filepath.ToSlash(rel)] = b
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// TestIdentityFlipGuard_CatchesEachBypass: the guard itself fails closed on
// each way around it, so a change that loosens it is caught here.
func TestIdentityFlipGuard_CatchesEachBypass(t *testing.T) {
	flip := []byte(`package books

import "sync/atomic"

var identityFlip atomic.Bool

func identityOn() bool { return identityFlip.Load() }

func SetIdentityFlipForTest(on bool) func() {
	prev := identityFlip.Swap(on)
	return func() { identityFlip.Store(prev) }
}
`)
	cases := map[string]struct {
		files map[string][]byte
		want  string // "" = clean; else a substring of a problem
	}{
		"the real shape is clean": {files: map[string][]byte{identityFlipFile: flip}},
		"a production caller of the selector": {
			files: map[string][]byte{identityFlipFile: flip, "cmd/serve.go": []byte("package cmd\nfunc f() { books.SetIdentityFlipForTest(true) }\n")},
			want:  "test-only selector",
		},
		"a sibling file writing the variable": {
			files: map[string][]byte{identityFlipFile: flip, "internal/books/module.go": []byte("package books\nfunc init() { identityFlip.Store(true) }\n")},
			want:  "backing variable",
		},
		"a second writer inside the predicate's file": {
			files: map[string][]byte{identityFlipFile: append(slices.Clone(flip), []byte("\nfunc enable() { identityFlip.Store(true) }\n")...)},
			want:  "writes identityFlip outside",
		},
		"the predicate file is gone": {
			files: map[string][]byte{"internal/books/module.go": []byte("package books\n")},
			want:  "is missing",
		},
		"the selector is gone": {
			files: map[string][]byte{identityFlipFile: []byte("package books\nimport \"sync/atomic\"\nvar identityFlip atomic.Bool\nfunc identityOn() bool { return identityFlip.Load() }\n")},
			want:  "declares no",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			probs := identityFlipProblems(c.files)
			if c.want == "" {
				if len(probs) != 0 {
					t.Fatalf("clean tree flagged: %v", probs)
				}
				return
			}
			if !slices.ContainsFunc(probs, func(p string) bool { return strings.Contains(p, c.want) }) {
				t.Fatalf("problems %v, want one containing %q", probs, c.want)
			}
		})
	}
}
