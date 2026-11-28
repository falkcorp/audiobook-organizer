// file: core/tasks/manage_kinds_test.go
// version: 0.1.0
// guid: c41e7f08-52d9-4a6b-8e13-0b97d5a2f6c3
// last-edited: 2026-11-28

package tasks_test

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

// moduleSuppliedManage are the two constructors core exposes for a module's
// kind: the namespace is the module's, so Manage is the caller's argument or
// spec field, never a core scope.
var moduleSuppliedManage = map[string]bool{
	filepath.FromSlash("storage/datamigrate/derived.go"): true,
	filepath.FromSlash("integrations/ai/batch.go"):       true,
}

// TestCoreKinds_ManageIsCoreTasksManage reads every tasks.Permissions literal
// in core's production source (owner ruling 2026-11-22): each states Manage,
// and a core-owned kind states rbac.Need("core:tasks.manage") (the examples are
// a demo module's own, and the parity kit builds a module per case).
func TestCoreKinds_ManageIsCoreTasksManage(t *testing.T) {
	root := filepath.Join("..")
	var literals int
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() {
			switch d.Name() {
			case "testdata", "node_modules", "examples", "taskstest", ".git":
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
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			sel, ok := lit.Type.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Permissions" {
				return true
			}
			if x, ok := sel.X.(*ast.Ident); !ok || x.Name != "tasks" {
				return true
			}
			literals++
			var manage ast.Expr
			for _, e := range lit.Elts {
				if kv, ok := e.(*ast.KeyValueExpr); ok {
					if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "Manage" {
						manage = kv.Value
					}
				}
			}
			if manage == nil {
				t.Errorf("%s: a tasks.Permissions literal states no Manage", rel)
				return true
			}
			if moduleSuppliedManage[rel] {
				return true
			}
			call, ok := manage.(*ast.CallExpr)
			if !ok || len(call.Args) != 1 {
				t.Errorf("%s: Manage is not rbac.Need(\"core:tasks.manage\")", rel)
				return true
			}
			arg, ok := call.Args[0].(*ast.BasicLit)
			if !ok {
				t.Errorf("%s: Manage names its scope by something other than a literal", rel)
				return true
			}
			if v, _ := strconv.Unquote(arg.Value); v != "core:tasks.manage" {
				t.Errorf("%s: a core kind's Manage is %s, want core:tasks.manage", rel, arg.Value)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if literals < 15 {
		t.Fatalf("the scan found %d tasks.Permissions literals, expected the core kinds' 20 or so: the path or the match is broken", literals)
	}
}
