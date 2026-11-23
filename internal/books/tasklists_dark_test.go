// file: internal/books/tasklists_dark_test.go
// version: 0.1.0
// guid: b9265f69-fcb5-4702-aa7a-d51479b7d9d7
// last-edited: 2026-11-22

package books

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The book task lists are dark: nothing that runs in ABO reaches them. A6 builds
// the lists and the flip (F5-18 r12) is the one change that wires them into
// Module(); until then a native kind must not be registered by a stray caller.
//
// This is a source scan, not a reachability argument, and it is the only proof
// of darkness: scripts/deadcode-ratchet.py is not a second one. The ratchet fails
// a NEW unreachable id and only prints a notice when a baselined id becomes
// reachable, so a caller wired in later passes it. (The A6 identifiers are in its
// baseline only so the ratchet does not fail on code that is dark on purpose.)
//
// The scan parses every non-test Go file in the repository and fails on any use
// of a dark name outside the files listed for it. Two kinds of name are dark:
//
//   - the entry points (bookTaskLists, BooksTaskLists, BooksHeldTaskLists), whose
//     one sanctioned use is internal/books/tasklists.go calling the per-package
//     BooksTaskLists, and nothing calls bookTaskLists at all;
//   - the builders those entry points are made of (newBooksKindDeps, newT6G7Deps
//     and writer.ForRun), whose only users are the A6 files that build the dark
//     lists. A live path calling one directly would hand a run the books module's
//     gated writer without going through the lists, so it trips here.
//
// Declarations are not uses.
type darkName struct {
	name string
	// qualifier, when set, restricts the match to the selector qualifier.name
	// (writer.ForRun), so an unrelated method of the same name is not a use.
	qualifier string
	// allowed are the files whose use is sanctioned, by path from the repo root.
	allowed []string
}

var darkNames = []darkName{
	{name: "bookTaskLists"},
	{name: "BooksTaskLists", allowed: []string{"internal/books/tasklists.go"}},
	{name: "BooksHeldTaskLists"},
	{name: "newBooksKindDeps", allowed: []string{
		"internal/plugins/acoustid/books_tasklists.go",
		"internal/plugins/deluge/books_tasklists.go",
		"internal/plugins/metafetch/books_tasklists.go",
	}},
	{name: "newT6G7Deps", allowed: []string{"internal/plugins/maintenance/books_tasklists.go"}},
	{name: "ForRun", qualifier: "writer", allowed: []string{
		"internal/plugins/acoustid/books_tasklists.go",
		"internal/plugins/deluge/books_tasklists.go",
		"internal/plugins/metafetch/books_tasklists.go",
		"internal/plugins/maintenance/deps_gate.go",
		"internal/scheduler/books_tasklists.go",
	}},
}

// darkUse reports the dark name an identifier use is, or "" if it is none or the
// file is sanctioned for it. sel is the selector the identifier is the right-hand
// side of, nil for a bare identifier.
func darkUse(rel string, id *ast.Ident, sel *ast.SelectorExpr) string {
	for _, dn := range darkNames {
		if id.Name != dn.name {
			continue
		}
		if dn.qualifier != "" {
			if sel == nil {
				continue
			}
			if q, ok := sel.X.(*ast.Ident); !ok || q.Name != dn.qualifier {
				continue
			}
		}
		for _, a := range dn.allowed {
			if rel == a {
				return ""
			}
		}
		if dn.qualifier != "" {
			return dn.qualifier + "." + dn.name
		}
		return dn.name
	}
	return ""
}

func TestF5_BookTaskListsAreDark(t *testing.T) {
	root := repoRoot(t)
	const sanctioned = "internal/books/tasklists.go"

	var uses []string
	scanned := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".worktrees", ".claude", "node_modules", "web", "testdata", "vendor":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		f, perr := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		require.NoError(t, perr, rel)
		scanned++
		declared := map[*ast.Ident]bool{}
		for _, decl := range f.Decls {
			if fd, ok := decl.(*ast.FuncDecl); ok {
				declared[fd.Name] = true
			}
		}
		// A selector's Sel is visited as an Ident of its own: record which
		// selector it belongs to so a qualified name (writer.ForRun) is judged
		// by its qualifier, and visit it once.
		selOf := map[*ast.Ident]*ast.SelectorExpr{}
		ast.Inspect(f, func(n ast.Node) bool {
			if se, ok := n.(*ast.SelectorExpr); ok {
				selOf[se.Sel] = se
			}
			return true
		})
		ast.Inspect(f, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok || declared[id] {
				return true
			}
			if use := darkUse(rel, id, selOf[id]); use != "" {
				uses = append(uses, rel+": "+use)
			}
			return true
		})
		return nil
	})
	require.NoError(t, err)
	require.Greater(t, scanned, 500, "the scan walked the repository, not an empty tree")
	require.Empty(t, uses, "a non-test file reaches the dark book task lists")

	// The sanctioned file does call the entry points: the scan above would pass
	// vacuously if the names had been renamed away from what it looks for.
	src, err := os.ReadFile(filepath.Join(root, sanctioned))
	require.NoError(t, err)
	require.Contains(t, string(src), "maintenance.BooksTaskLists(")
	require.Contains(t, string(src), "func bookTaskLists(")
}

// repoRoot is the module root, found from the test's working directory (a
// -trimpath build has no usable runtime.Caller path).
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "internal", "books")); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, dir, parent, "no module root above the test's directory")
		dir = parent
	}
}
