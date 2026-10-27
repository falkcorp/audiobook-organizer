// file: internal/server/handlers/opaccess/result_registry_ratchet_test.go
// version: 1.0.0
// guid: 4e9a2c76-1d58-4b03-a7f4-3c6b0e8d5a19
// last-edited: 2026-10-26

package opaccess

import (
	"fmt"
	"go/ast"
	"go/types"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

const modulePathForScan = "github.com/falkcorp/audiobook-organizer"

// userIDJSONKeys are the json names that make a stored result carry one user's
// identity: the owner of the state, the account it was moved to, the account an
// action targeted.
var userIDJSONKeys = map[string]struct{}{
	"user_id":        {},
	"target_user_id": {},
	"owner_user_id":  {},
}

// resultTypesWithUserIDs is every Go type some operation stores as its result
// (registry.ReporterSetResult and the SetResult helpers) that has a user-id
// field, with the definition id whose result it is. Each definition must be
// gated: listed in ResultUserDataDefs (only users.manage reads its result) or
// in the Repairs lane (IsRepairsOp: only a caller who may use its fixer does).
//
// This is the ratchet behind ResultUserDataDefs, which is opt-in: a new op whose
// result names a user is served to every viewer until someone lists it, and
// TestResultUserDataDefs_AreRegisteredDefs only pins that the ids already listed
// still exist. A new result type with a user-id field fails
// TestResultTypesWithUserIDsAreGated until it is added here with a gated def,
// which is the moment to add the def to ResultUserDataDefs.
var resultTypesWithUserIDs = map[string]string{
	// The merged-user-state repair's report: items[] per user.
	"internal/merge.UserStateRepairReport": merge.UserStateRepairOpID,
	// A Repairs apply's per-row report; the Repairs lane gates it by fixer.
	"internal/repairs.ApplyResult": repairs.ApplyOpID,
}

// TestResultTypesWithUserIDsAreGated scans every call that stores an
// operation's result and fails when the stored type, or a type nested in it, has
// a json field user_id, target_user_id or owner_user_id and the type is not
// registered above against a gated definition. It also fails on a registered
// type that is no longer stored anywhere (delete the entry) and on one whose
// definition is not gated.
//
// The scan is type-based (go/types), not textual, and covers the stored value's
// static type: a call that stores an interface or a map is not inspected, since
// there is no struct to read. Those callers must be reviewed by hand.
func TestResultTypesWithUserIDsAreGated(t *testing.T) {
	root := scanRepoRoot(t)
	pkgs := loadResultCallers(t, root)

	found := map[string][]string{} // type -> call sites
	for _, p := range pkgs {
		for _, f := range p.Syntax {
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) == 0 {
					return true
				}
				if name := calleeName(call.Fun); name != "ReporterSetResult" && name != "SetResult" {
					return true
				}
				arg := call.Args[len(call.Args)-1]
				typ := p.TypesInfo.TypeOf(arg)
				if typ == nil {
					return true
				}
				for _, top := range storedTypes(typ) {
					if len(userIDFields(top, map[types.Type]bool{})) == 0 {
						continue
					}
					pos := p.Fset.Position(call.Pos())
					rel, _ := filepath.Rel(root, pos.Filename)
					found[typeKey(top)] = append(found[typeKey(top)], fmt.Sprintf("%s:%d", rel, pos.Line))
				}
				return true
			})
		}
	}

	var keys []string
	for k := range found {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		def, registered := resultTypesWithUserIDs[k]
		switch {
		case !registered:
			t.Errorf("%s is stored as an operation result (%s) and has a user-id json field, but is not in "+
				"resultTypesWithUserIDs: add it with the id of the definition whose result it is, and list that "+
				"definition in ResultUserDataDefs so only users.manage reads the result", k, strings.Join(found[k], ", "))
		case !ResultCarriesUserData(def) && !IsRepairsOp(def):
			t.Errorf("%s is registered against %q, which is neither in ResultUserDataDefs nor a Repairs lane definition: "+
				"its result is served to every viewer", k, def)
		}
	}
	for k := range resultTypesWithUserIDs {
		if _, ok := found[k]; !ok {
			t.Errorf("resultTypesWithUserIDs lists %s, which no operation stores as a result any more: delete the entry", k)
		}
	}
	if len(found) == 0 {
		t.Fatal("the scan found no result type with a user-id field: the loader is broken, not the code")
	}
}

func calleeName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}

// storedTypes is the struct types a stored value of static type t has: t itself
// (through pointers), and the element type of a slice or array of them. An
// interface, a map and a basic type have none to read.
func storedTypes(t types.Type) []types.Type {
	for {
		switch u := t.(type) {
		case *types.Pointer:
			t = u.Elem()
			continue
		case *types.Slice:
			t = u.Elem()
			continue
		case *types.Array:
			t = u.Elem()
			continue
		}
		break
	}
	if _, ok := t.Underlying().(*types.Struct); ok {
		return []types.Type{t}
	}
	return nil
}

func typeKey(t types.Type) string {
	if n, ok := t.(*types.Named); ok && n.Obj().Pkg() != nil {
		return strings.TrimPrefix(n.Obj().Pkg().Path(), modulePathForScan+"/") + "." + n.Obj().Name()
	}
	return t.String()
}

// userIDFields lists the json paths under t whose key is one of userIDJSONKeys.
func userIDFields(t types.Type, seen map[types.Type]bool) []string {
	if seen[t] {
		return nil
	}
	seen[t] = true
	switch u := t.(type) {
	case *types.Pointer:
		return userIDFields(u.Elem(), seen)
	case *types.Slice:
		return userIDFields(u.Elem(), seen)
	case *types.Array:
		return userIDFields(u.Elem(), seen)
	case *types.Map:
		return userIDFields(u.Elem(), seen)
	case *types.Named:
		return userIDFields(u.Underlying(), seen)
	case *types.Struct:
		var out []string
		for i := 0; i < u.NumFields(); i++ {
			f := u.Field(i)
			name, _, _ := strings.Cut(reflect.StructTag(u.Tag(i)).Get("json"), ",")
			if name == "-" {
				continue
			}
			if _, hit := userIDJSONKeys[name]; hit {
				out = append(out, name)
			}
			out = append(out, userIDFields(f.Type(), seen)...)
		}
		return out
	}
	return nil
}

func scanRepoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	root := filepath.Clean(filepath.Join(wd, "..", "..", "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("expected go.mod at %s: %v", root, err)
	}
	return root
}

// loadResultCallers type-checks the non-test packages that can store a result:
// the ones importing the operations registry or internal/maintenance, the two
// homes of the setters.
func loadResultCallers(t *testing.T, root string) []*packages.Package {
	t.Helper()
	homes := []string{
		modulePathForScan + "/internal/operations/registry",
		modulePathForScan + "/internal/maintenance",
	}
	list, err := packages.Load(&packages.Config{Mode: packages.NeedName | packages.NeedImports, Dir: root},
		"./internal/...", "./pkg/...")
	if err != nil {
		t.Fatalf("list packages: %v", err)
	}
	var paths []string
	for _, p := range list {
		if strings.Contains(p.PkgPath, "/mocks") {
			continue
		}
		for _, h := range homes {
			if _, ok := p.Imports[h]; ok {
				paths = append(paths, p.PkgPath)
				break
			}
		}
	}
	if len(paths) == 0 {
		t.Fatal("found no package that imports the registry: the package listing is broken")
	}
	pkgs, err := packages.Load(&packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo,
		Dir: root,
	}, paths...)
	if err != nil {
		t.Fatalf("load packages: %v", err)
	}
	for _, p := range pkgs {
		for _, e := range p.Errors {
			t.Fatalf("type-check %s: %v", p.PkgPath, e)
		}
	}
	return pkgs
}
