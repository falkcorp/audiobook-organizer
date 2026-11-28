// file: internal/books/taskmanage_dark_test.go
// version: 0.1.0
// guid: 3b8d2c61-7a4e-4f19-9c05-e1a6d7f20b84
// last-edited: 2026-11-28

package books

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/mediacore/module"
)

// TestRegister_RegistersNoTaskKind is the dark half of F5-18 M1: the per-kind
// Perms.Manage rule gates run control for registered kinds only, so while
// books registers none no running server can meet it. Register, run without
// storage (the declaration alone), records no task definition.
func TestRegister_RegistersNoTaskKind(t *testing.T) {
	withConfig(t, config.Config{})
	m := Module().(*booksModule)
	r := module.NewRegistrar(module.RegistrarConfig{Context: t.Context(), Deps: module.Deps{Module: ID}})
	require.NoError(t, m.Register(r))
	require.NoError(t, r.Err())
	require.Empty(t, r.Close().Tasks, "books registered a task kind: F5-18 M1's Manage rule is no longer dark")
}

// TestNoProductionCallRegistersATask is the same claim over the whole root
// module's source, so it holds on Register's storage path too, which the test
// above cannot boot: no non-test file under cmd/, internal/ or the root passes
// a definition to a Registrar.Tasks call. (core/ is a separate module; its one
// production caller is the hello example, which no books binary links.)
func TestNoProductionCallRegistersATask(t *testing.T) {
	root := filepath.Join("..", "..")
	var scanned int
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "core", "node_modules", ".git", ".worktrees", "web", "testdata", "vendor", ".claude":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		scanned++
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Tasks" && len(call.Args) > 0 {
				t.Errorf("%s registers task definitions through a .Tasks(...) call: F5-18 M1's Manage rule is no longer dark", path)
			}
			return true
		})
		return nil
	})
	require.NoError(t, err)
	require.Greater(t, scanned, 500, "the scan found almost no Go files: the root path is wrong")
}
