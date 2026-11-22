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
// This is a source scan, not a reachability argument. It parses every non-test Go
// file in the repository and fails on any use of bookTaskLists, of the per-package
// BooksTaskLists entry points or of BooksHeldTaskLists that is not the one
// sanctioned call site: internal/books/tasklists.go calls the per-package entry
// points, and nothing calls bookTaskLists at all. Declarations are not uses.
// (make lint-deadcode-ratchet is the second proof: the same identifiers are in
// its baseline, so a new caller also fails there.)
func TestF5_BookTaskListsAreDark(t *testing.T) {
	root := repoRoot(t)
	const sanctioned = "internal/books/tasklists.go"
	darkNames := map[string]bool{
		"bookTaskLists":      true,
		"BooksTaskLists":     true,
		"BooksHeldTaskLists": true,
	}

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
		ast.Inspect(f, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok || declared[id] || !darkNames[id.Name] {
				return true
			}
			if rel == sanctioned && id.Name == "BooksTaskLists" {
				return true
			}
			uses = append(uses, rel+": "+id.Name)
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
