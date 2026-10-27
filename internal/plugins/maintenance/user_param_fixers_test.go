// file: internal/plugins/maintenance/user_param_fixers_test.go
// version: 1.0.0
// guid: 8d4e1b73-2a5c-4f60-b9e8-1c7a3d5f2b94
// last-edited: 2026-10-25

package maintenance

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestFixersTakingATargetUser_RequireUsersManage: a fixer whose params name
// another user (a field tagged json:"target_user_id") echoes that id into its
// plan result (repairs.PlanResult.Params), its plan rows and its apply's journal.
// Those are served to every caller who holds the fixer's permissions, so the
// fixer must be scoped to users.manage (RequiredPermissions names
// PermUsersManage); at the lane's default library.edit_metadata an editor could
// plan against another user and read that user's state. A new fixer with such a
// field fails here until it declares the scope.
func TestFixersTakingATargetUser_RequireUsersManage(t *testing.T) {
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	fset := token.NewFileSet()
	var files []*ast.File
	var names []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		require.NoError(t, err, name)
		files = append(files, f)
		names = append(names, name)
	}

	// The param types: structs with a field tagged json:"target_user_id".
	paramTypes := map[string]bool{}
	for _, f := range files {
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, sp := range gd.Specs {
				ts := sp.(*ast.TypeSpec)
				st, ok := ts.Type.(*ast.StructType)
				if !ok {
					continue
				}
				for _, fld := range st.Fields.List {
					if fld.Tag != nil && strings.Contains(fld.Tag.Value, `json:"target_user_id`) {
						paramTypes[ts.Name.Name] = true
					}
				}
			}
		}
	}
	require.NotEmpty(t, paramTypes, "the scan found no param type with a target_user_id field: the tag it looks for has moved")

	// A fixer file is one that declares a method ID() (repairs.Fixer). Every
	// fixer file that mentions a param type must scope the fixer to users.manage.
	fixerFilesFor := map[string]int{}
	for i, f := range files {
		var isFixer, scoped bool
		mentions := map[string]bool{}
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.Ident:
				if paramTypes[n.Name] {
					mentions[n.Name] = true
				}
			case *ast.FuncDecl:
				if n.Recv == nil {
					break
				}
				switch n.Name.Name {
				case "ID":
					isFixer = true
				case "RequiredPermissions":
					ast.Inspect(n, func(b ast.Node) bool {
						if sel, ok := b.(*ast.SelectorExpr); ok && sel.Sel.Name == "PermUsersManage" {
							scoped = true
						}
						return true
					})
				}
			}
			return true
		})
		if !isFixer || len(mentions) == 0 {
			continue
		}
		for typ := range mentions {
			fixerFilesFor[typ]++
		}
		require.True(t, scoped, "%s: a fixer whose params have a target_user_id field must implement RequiredPermissions with auth.PermUsersManage", names[i])
	}
	for typ := range paramTypes {
		require.NotZero(t, fixerFilesFor[typ], "no fixer file mentions the param type %s: the scan cannot tell which fixer takes it", typ)
	}
}
