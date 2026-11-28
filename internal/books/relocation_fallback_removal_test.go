// file: internal/books/relocation_fallback_removal_test.go
// version: 0.1.0
// guid: c58e2a91-3f47-4d06-8b1c-9a7e0d54b263
// last-edited: 2026-11-28

package books

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// The switch-off Deluge fallback is temporary (F6-03, option b). The organize
// request calls deluge.NotifyDelugeAfterOrganize itself while the tasks switch is
// off, because the durable relocation follow-up is wired only with it
// (relocationFollowUpOn). The F5-18 switch commit turns the tasks flip on for
// good by editing tasksFlipOn (tasks_flip.go: "turns it on for good by editing
// tasksFlipOn and deletes the selector"), and from that commit the staged arm is
// the only one: the direct call would then move every organized copy's torrents a
// second time. So the switch commit must delete the fallback, and this guard
// fails it until it does.
//
// "The switch is on by default" is read the way production reads it: tasksFlipOn()
// with nothing having called the test-only selector. That catches every way of
// turning it on (a constant, an init, a changed default), and needs no marker
// edit in the switch commit.
//
// The fallback is two things: the function deluge.NotifyDelugeAfterOrganize and
// the handler's call to it. The commit deletes both, and the arm cells in
// relocation_arms_test.go with them.
const (
	fallbackFuncFile    = "../deluge/integration.go"
	fallbackCallerFile  = "../server/handlers/organize.go"
	fallbackSymbol      = "NotifyDelugeAfterOrganize"
	fallbackSwitchTodo  = "todo.d/2026-11-28-f6-03-delete-deluge-fallback-at-tasks-switch.md"
	fallbackSwitchTitle = "the F5-18 tasks switch commit"
)

// relocationFallbackProblems judges the two source files (path to contents).
// With the tasks flip off by default it asks nothing. With it on by default the
// fallback must be gone: the declaration and every call.
func relocationFallbackProblems(tasksOnByDefault bool, files map[string][]byte) []string {
	if !tasksOnByDefault {
		return nil
	}
	var out []string
	fset := token.NewFileSet()
	for _, p := range []string{fallbackFuncFile, fallbackCallerFile} {
		src, ok := files[p]
		if !ok {
			out = append(out, p+" is missing: the fallback's file moved, so this guard guards nothing")
			continue
		}
		f, err := parser.ParseFile(fset, p, src, parser.SkipObjectResolution)
		if err != nil {
			out = append(out, fmt.Sprintf("%s: unparsable: %v", p, err))
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.FuncDecl:
				if x.Name.Name == fallbackSymbol {
					out = append(out, fmt.Sprintf("%s:%d: declares %s", p, fset.Position(x.Pos()).Line, fallbackSymbol))
				}
			case *ast.CallExpr:
				if sel, ok := x.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == fallbackSymbol {
					out = append(out, fmt.Sprintf("%s:%d: calls %s", p, fset.Position(x.Pos()).Line, fallbackSymbol))
				}
			}
			return true
		})
	}
	return out
}

// TestRelocationFallback_IsDeletedWhereTheTasksFlipIsOnByDefault is the guard.
// It reads the production default, so it passes today (off) and fails in the
// commit that turns the flip on until that commit deletes the fallback.
func TestRelocationFallback_IsDeletedWhereTheTasksFlipIsOnByDefault(t *testing.T) {
	files := map[string][]byte{}
	for _, p := range []string{fallbackFuncFile, fallbackCallerFile} {
		src, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		files[p] = src
	}
	if probs := relocationFallbackProblems(tasksFlipOn(), files); len(probs) > 0 {
		t.Fatalf("the tasks flip is on by default, so the relocation follow-up is the only Deluge arm, and %s must delete the switch-off fallback (%s). Still present:\n%s",
			fallbackSwitchTitle, fallbackSwitchTodo, strings.Join(probs, "\n"))
	}
}

// TestRelocationFallbackGuard_JudgesEachCase: the guard fails closed on the one
// bad shape, passes on the two good ones, and notices the fallback in either file.
func TestRelocationFallbackGuard_JudgesEachCase(t *testing.T) {
	decl := []byte("package deluge\n\nfunc NotifyDelugeAfterOrganize(store any, bookID, newPath string) {}\n")
	noDecl := []byte("package deluge\n\nfunc NotifyDelugeMoveStorage(h, p string) {}\n")
	call := []byte("package handlers\n\nfunc f() {\n\tdeluge.NotifyDelugeAfterOrganize(nil, \"b\", \"p\")\n}\n")
	noCall := []byte("package handlers\n\nfunc f() {}\n")
	mk := func(fn, caller []byte) map[string][]byte {
		return map[string][]byte{fallbackFuncFile: fn, fallbackCallerFile: caller}
	}
	for _, tc := range []struct {
		name    string
		on      bool
		files   map[string][]byte
		wantN   int
		wantSub string
	}{
		{"switch off, fallback present: fine until the switch commit", false, mk(decl, call), 0, ""},
		{"switch on, fallback deleted: fine", true, mk(noDecl, noCall), 0, ""},
		{"switch on, both present", true, mk(decl, call), 2, "declares"},
		{"switch on, only the call left", true, mk(noDecl, call), 1, "calls"},
		{"switch on, only the declaration left", true, mk(decl, noCall), 1, "declares"},
		{"switch on, a file moved", true, map[string][]byte{fallbackFuncFile: noDecl}, 1, "is missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := relocationFallbackProblems(tc.on, tc.files)
			if len(got) != tc.wantN {
				t.Fatalf("problems = %d %v, want %d", len(got), got, tc.wantN)
			}
			if tc.wantSub != "" && !strings.Contains(strings.Join(got, "\n"), tc.wantSub) {
				t.Fatalf("problems %v do not say %q", got, tc.wantSub)
			}
		})
	}
}
