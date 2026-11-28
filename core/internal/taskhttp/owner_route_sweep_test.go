// file: core/internal/taskhttp/owner_route_sweep_test.go
// version: 0.1.0
// guid: a1cfd66b-071e-421a-ba17-ac05c0a47867
// last-edited: 2026-11-28

package taskhttp

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// actorDerivedWords mark a params field that carries who is asking or what they
// may do: the data a caller must not be able to write through the generic start
// (POST /definitions/{kind}/runs), because the run would read it as the truth.
var actorDerivedWords = []string{"actor", "perm", "scope", "role", "principal", "user", "owner", "requester", "credential", "token", "grant", "subject",
	"requested", "createdby", "created_by", "startedby", "started_by", "initiator", "submit"}

// ownerRouteOnlyForParams are the core params types with such a field, each
// with why the generic start cannot hand it caller-written data. Every one must
// be OwnerRouteOnly in its definition or carry an entry in ignoresCallerCopy.
// A new core kind whose params look like these fails here until it is listed.
var ownerRouteOnlyForParams = map[string]string{
	"health/diagnostics.ExportParams": "ActorID and Perms are stamped by the diagnostics route from the request's own principal (PermsOf); the kind is OwnerRouteOnly so the generic start cannot supply them",
}

// ignoresCallerCopy are the core params types with such a field whose run
// ignores the caller's copy and reads the stamped actor instead. None today.
var ignoresCallerCopy = map[string]string{}

type paramsUse struct {
	key            string // "<dir relative to core>.<Type>"
	pos            string
	flagged        []string // the fields that look actor-derived
	ownerRouteOnly bool
	permsLiteral   bool // Perms was an inline literal the scan could read
}

// TestOwnerRouteOnly_EveryCoreKindWithActorDerivedParams reads every core task
// definition (a tasks.TaskSpec, BatchSpec or FixerSpec literal outside tests and
// the tasks package itself) and its params struct. A kind whose params carry a
// field named for an actor, a permission, a scope, a role, a principal, a user or
// an owner must be OwnerRouteOnly (so only its own route starts it) or be listed
// as one that ignores the caller's copy. Today that is core.diagnostics.export
// alone; the test fails on any other, and on a listed one that no longer matches.
func TestOwnerRouteOnly_EveryCoreKindWithActorDerivedParams(t *testing.T) {
	wd, err := os.Getwd() // not runtime.Caller: CI builds with -trimpath
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(wd, "..", "..") // core/
	uses, scanned := scanCoreParams(t, root)
	if scanned < 20 {
		t.Fatalf("the scan read %d definition literals under %s, want at least 20: it is not finding the kinds", scanned, root)
	}

	t.Logf("scanned %d definition literals", scanned)
	flagged := map[string]paramsUse{}
	for _, u := range uses {
		if len(u.flagged) > 0 {
			flagged[u.key] = u
		}
	}
	var keys []string
	for k := range flagged {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		u := flagged[k]
		_, listedOwner := ownerRouteOnlyForParams[k]
		_, listedIgnore := ignoresCallerCopy[k]
		switch {
		case !listedOwner && !listedIgnore:
			t.Errorf("%s (%s) carries %v: a caller could write it through the generic start. Make the kind OwnerRouteOnly, or make the run ignore the caller's copy, and list it in this test with the reason", k, u.pos, u.flagged)
		case listedOwner && !u.ownerRouteOnly:
			t.Errorf("%s (%s) is listed as OwnerRouteOnly but its definition sets no OwnerRouteOnly reason (Perms literal read: %v)", k, u.pos, u.permsLiteral)
		}
	}
	for _, listed := range []map[string]string{ownerRouteOnlyForParams, ignoresCallerCopy} {
		for k := range listed {
			if _, ok := flagged[k]; !ok {
				t.Errorf("%s is listed here but no core definition has it with an actor-derived field: drop the entry or fix the scan", k)
			}
		}
	}
}

func looksActorDerived(name, tag string) bool {
	hay := strings.ToLower(name + " " + tag)
	for _, w := range actorDerivedWords {
		if strings.Contains(hay, w) {
			return true
		}
	}
	return false
}

func scanCoreParams(t *testing.T, root string) (uses []paramsUse, scanned int) {
	t.Helper()
	fset := token.NewFileSet()
	type pkg struct {
		files   []*ast.File
		structs map[string]*ast.StructType
	}
	pkgs := map[string]*pkg{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() {
			// core/tasks is the framework (its taskstest kit builds throwaway kinds).
			if d.Name() == "testdata" || rel == "tasks" || strings.HasPrefix(rel, "tasks"+string(filepath.Separator)) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		dir := filepath.Dir(rel)
		p := pkgs[dir]
		if p == nil {
			p = &pkg{structs: map[string]*ast.StructType{}}
			pkgs[dir] = p
		}
		p.files = append(p.files, f)
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, sp := range gd.Specs {
				ts := sp.(*ast.TypeSpec)
				if st, ok := ts.Type.(*ast.StructType); ok {
					p.structs[ts.Name.Name] = st
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	dirs := make([]string, 0, len(pkgs))
	for d := range pkgs {
		dirs = append(dirs, d)
	}
	slices.Sort(dirs)
	for _, dir := range dirs {
		p := pkgs[dir]
		for _, f := range p.files {
			ast.Inspect(f, func(n ast.Node) bool {
				cl, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				var x ast.Expr
				var first ast.Expr
				switch ty := cl.Type.(type) {
				case *ast.IndexListExpr:
					x, first = ty.X, ty.Indices[0]
				case *ast.IndexExpr:
					x, first = ty.X, ty.Index
				default:
					return true
				}
				sel, ok := x.(*ast.SelectorExpr)
				if !ok || !slices.Contains([]string{"TaskSpec", "BatchSpec", "FixerSpec"}, sel.Sel.Name) {
					return true
				}
				if id, ok := sel.X.(*ast.Ident); !ok || id.Name != "tasks" {
					return true
				}
				scanned++
				u := paramsUse{pos: fset.Position(cl.Pos()).String()}
				var fields *ast.StructType
				switch pt := first.(type) {
				case *ast.Ident:
					u.key = dir + "." + pt.Name
					fields = p.structs[pt.Name]
					if fields == nil && pt.Name != "any" {
						// An alias or a defined non-struct type: nothing to inspect, say so.
						t.Logf("%s: params type %s is not a struct in its package; not inspected", u.pos, pt.Name)
					}
				case *ast.SelectorExpr:
					u.key = dir + "." + pt.Sel.Name // tasks.NoParams and the like
				default:
					u.key = dir + ".<inline>"
				}
				if fields != nil {
					for _, fl := range fields.Fields.List {
						tag := ""
						if fl.Tag != nil {
							tag = fl.Tag.Value
						}
						names := []string{""}
						if len(fl.Names) > 0 {
							names = names[:0]
							for _, n := range fl.Names {
								names = append(names, n.Name)
							}
						} else if id, ok := fl.Type.(*ast.Ident); ok {
							names = []string{id.Name} // embedded
						}
						for _, nm := range names {
							if looksActorDerived(nm, tag) {
								u.flagged = append(u.flagged, nm)
							}
						}
					}
				}
				for _, el := range cl.Elts {
					kv, ok := el.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					if k, ok := kv.Key.(*ast.Ident); !ok || k.Name != "Perms" {
						continue
					}
					pl, ok := kv.Value.(*ast.CompositeLit)
					if !ok {
						continue
					}
					u.permsLiteral = true
					for _, pe := range pl.Elts {
						pkv, ok := pe.(*ast.KeyValueExpr)
						if !ok {
							continue
						}
						if k, ok := pkv.Key.(*ast.Ident); ok && k.Name == "OwnerRouteOnly" {
							if bl, ok := pkv.Value.(*ast.BasicLit); !ok || bl.Value != `""` {
								u.ownerRouteOnly = true
							}
						}
					}
				}
				uses = append(uses, u)
				return true
			})
		}
	}
	return uses, scanned
}
