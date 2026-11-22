// file: internal/arch/tasks_flip_guard_test.go
// version: 0.1.0
// guid: 2876ea4b-402e-463b-a081-0851c594337b
// last-edited: 2026-11-27

package arch

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strings"
	"testing"
)

// The tasks flip's one predicate (internal/books/tasks_flip.go) has a backing
// variable and a test-only selector. Production turns the flip on only at the
// F5-18 switch commit, by editing the predicate itself; until then nothing
// outside a test may flip it, or ABO would run on core's executor, and M4 would
// retire HEAD's run rows, before the switch is reviewed. The identity flip had
// the same guard until B10g turned it on for good. Two rules, both read off
// the syntax:
//
//   - no non-test file calls or names SetTasksFlipForTest, except its own
//     declaration;
//   - no file in internal/books but tasks_flip.go names tasksFlip (the
//     backing variable), and inside it only tasksFlipOn reads it and only
//     SetTasksFlipForTest writes it.

const (
	tasksFlipFile     = "internal/books/tasks_flip.go"
	tasksFlipSelector = "SetTasksFlipForTest"
	tasksFlipVar      = "tasksFlip"
)

// tasksFlipProblems judges the non-test Go files of the module (path
// relative to the repository root, slash-separated, to source).
func tasksFlipProblems(files map[string][]byte) []string {
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
		if p == tasksFlipFile {
			sawFlipFile = true
			out = append(out, tasksFlipFileProblems(f)...)
			continue
		}
		inBooks := strings.HasPrefix(p, "internal/books/") && strings.Count(strings.TrimPrefix(p, "internal/books/"), "/") == 0
		ast.Inspect(f, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			switch {
			case id.Name == tasksFlipSelector:
				out = append(out, fmt.Sprintf("%s:%d: names %s, the test-only selector of the tasks predicate: ABO would run on core's executor outside a test before the F5-18 switch",
					p, fset.Position(id.Pos()).Line, tasksFlipSelector))
			case inBooks && id.Name == tasksFlipVar:
				out = append(out, fmt.Sprintf("%s:%d: names %s, the predicate's backing variable: only %s may touch it",
					p, fset.Position(id.Pos()).Line, tasksFlipVar, tasksFlipFile))
			}
			return true
		})
	}
	if !sawFlipFile {
		out = append(out, tasksFlipFile+" is missing: the tasks predicate's file moved, so this guard guards nothing")
	}
	return out
}

// tasksFlipFileProblems is the rule inside the predicate's own file: the
// variable is stored to (Store, Swap, CompareAndSwap) only inside the selector.
func tasksFlipFileProblems(f *ast.File) []string {
	var out []string
	hasSelector := false
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fn.Name.Name == tasksFlipSelector {
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
			if !ok || recv.Name != tasksFlipVar {
				return true
			}
			switch sel.Sel.Name {
			case "Store", "Swap", "CompareAndSwap":
				if fn.Name.Name != tasksFlipSelector {
					out = append(out, fmt.Sprintf("%s: %s writes %s outside %s", tasksFlipFile, fn.Name.Name, tasksFlipVar, tasksFlipSelector))
				}
			}
			return true
		})
	}
	if !hasSelector {
		out = append(out, tasksFlipFile+" declares no "+tasksFlipSelector+": the guard cannot tell the selector from a stray writer")
	}
	return out
}

// TestTasksFlipSelectorHasNoProductionCaller pins the two rules over the real
// tree.
func TestTasksFlipSelectorHasNoProductionCaller(t *testing.T) {
	files := productionGoFiles(t)
	if probs := tasksFlipProblems(files); len(probs) > 0 {
		t.Fatalf("the tasks predicate's guard found %d problem(s):\n%s", len(probs), strings.Join(probs, "\n"))
	}
}

// TestTasksFlipGuard_CatchesEachBypass: the guard itself fails closed on
// each way around it, so a change that loosens it is caught here.
func TestTasksFlipGuard_CatchesEachBypass(t *testing.T) {
	flip := []byte(`package books

import "sync/atomic"

var tasksFlip atomic.Bool

func tasksFlipOn() bool { return tasksFlip.Load() }

func SetTasksFlipForTest(on bool) func() {
	prev := tasksFlip.Swap(on)
	return func() { tasksFlip.Store(prev) }
}
`)
	cases := map[string]struct {
		files map[string][]byte
		want  string // "" = clean; else a substring of a problem
	}{
		"the real shape is clean": {files: map[string][]byte{tasksFlipFile: flip}},
		"a production caller of the selector": {
			files: map[string][]byte{tasksFlipFile: flip, "cmd/serve.go": []byte("package cmd\nfunc f() { books.SetTasksFlipForTest(true) }\n")},
			want:  "test-only selector",
		},
		"a sibling file writing the variable": {
			files: map[string][]byte{tasksFlipFile: flip, "internal/books/module.go": []byte("package books\nfunc init() { tasksFlip.Store(true) }\n")},
			want:  "backing variable",
		},
		"a second writer inside the predicate's file": {
			files: map[string][]byte{tasksFlipFile: append(slices.Clone(flip), []byte("\nfunc enable() { tasksFlip.Store(true) }\n")...)},
			want:  "writes tasksFlip outside",
		},
		"the predicate file is gone": {
			files: map[string][]byte{"internal/books/module.go": []byte("package books\n")},
			want:  "is missing",
		},
		"the selector is gone": {
			files: map[string][]byte{tasksFlipFile: []byte("package books\nimport \"sync/atomic\"\nvar tasksFlip atomic.Bool\nfunc tasksFlipOn() bool { return tasksFlip.Load() }\n")},
			want:  "declares no",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			probs := tasksFlipProblems(c.files)
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
