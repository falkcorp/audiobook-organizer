// file: internal/opscatalog/catalog_link_test.go
// version: 1.0.0
// guid: 671b8f3a-97bd-4eba-bc0d-4927cf15d05e
// last-edited: 2026-10-16

package opscatalog

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const modulePath = "github.com/falkcorp/audiobook-organizer"

// TestCatalogLinkedIntoBinary checks the server binary's real import graph --
// `go list -deps` on the main package (the module root), which leaves out test
// files -- for every package that registers with the service registry or
// defines Ops(...) ops.Bundle, and requires every Ops package to be listed in
// All. A test-only import cannot satisfy it: that is how metafetch's ops passed
// every test and were missing from prod for weeks. It replaces
// internal/plugins TestEveryRegistryPluginIsLinkedIntoTheBinary, which checked
// internal/plugins/* only.
func TestCatalogLinkedIntoBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go list over the whole module")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go tool not on PATH")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(goBin, "list", "-deps", modulePath)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -deps %s: %v", modulePath, err)
	}
	linked := map[string]bool{}
	for _, line := range strings.Fields(string(out)) {
		linked[line] = true
	}
	inAll := catalogEntries(t, "catalog.go")

	problems, registrants := linkProblems(t, root, modulePath, linked, inAll)
	for _, p := range problems {
		t.Error(p)
	}
	if registrants == 0 {
		t.Fatal("found no service-registry or bundle packages; the scan is broken")
	}
}

// The check goes red on a package that defines Ops but is in neither All nor
// the binary, and green once it is in both.
func TestCatalogLinkCheckFlagsUnlistedBundle(t *testing.T) {
	const fixtureModule = "example.test/fixture"
	root := filepath.Join("testdata", "linkfixture")
	pkg := fixtureModule + "/internal/plugins/synthbundle"

	problems, n := linkProblems(t, root, fixtureModule, map[string]bool{}, map[string]bool{})
	if n != 1 {
		t.Fatalf("fixture scan found %d packages, want 1", n)
	}
	want := []string{
		pkg + " defines Ops(...) ops.Bundle but is not in opscatalog.All; add " + "synthbundle.Ops to All in internal/opscatalog/catalog.go",
		pkg + " defines Ops(...) ops.Bundle but is not linked into the server binary; reference it from internal/opscatalog/catalog.go",
	}
	if strings.Join(problems, "\n") != strings.Join(want, "\n") {
		t.Fatalf("problems:\n%s\nwant:\n%s", strings.Join(problems, "\n"), strings.Join(want, "\n"))
	}

	problems, _ = linkProblems(t, root, fixtureModule, map[string]bool{pkg: true}, map[string]bool{pkg: true})
	if len(problems) != 0 {
		t.Fatalf("a listed, linked bundle was flagged: %v", problems)
	}
}

// The catalog's All, read the way the check reads it, is what the package
// holds.
func TestCatalogEntriesMatchAll(t *testing.T) {
	if got := len(catalogEntries(t, "catalog.go")); got != len(All) {
		t.Fatalf("catalog.go lists %d Ops references, All holds %d entries; every entry must name one package's Ops as pkg.Ops", got, len(All))
	}
}

// catalogEntries returns the import paths whose Ops function the All var in
// file references (as pkg.Ops, directly or inside an entry's func literal).
func catalogEntries(t *testing.T, file string) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	imports := importNames(f)
	out := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok || len(vs.Names) != 1 || vs.Names[0].Name != "All" {
			return true
		}
		for _, v := range vs.Values {
			lit, ok := v.(*ast.CompositeLit)
			if !ok {
				continue
			}
			for _, el := range lit.Elts {
				ast.Inspect(el, func(n ast.Node) bool {
					sel, ok := n.(*ast.SelectorExpr)
					if !ok || sel.Sel.Name != "Ops" {
						return true
					}
					if id, ok := sel.X.(*ast.Ident); ok && imports[id.Name] != "" {
						out[imports[id.Name]] = true
					}
					return true
				})
			}
		}
		return false
	})
	return out
}

// importNames maps each import's local name to its path.
func importNames(f *ast.File) map[string]string {
	out := map[string]string{}
	for _, imp := range f.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		name := p[strings.LastIndex(p, "/")+1:]
		if imp.Name != nil {
			name = imp.Name.Name
		}
		out[name] = p
	}
	return out
}

// linkProblems walks root/internal for packages whose non-test files call
// serviceregistry.Register or declare Ops(...) ops.Bundle, and reports each
// one that is not linked (or, for a bundle, not in inAll). It returns the
// problems sorted and the number of such packages.
func linkProblems(t *testing.T, root, module string, linked, inAll map[string]bool) ([]string, int) {
	t.Helper()
	var problems []string
	found := 0
	base := filepath.Join(root, "internal")
	err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".") {
			return filepath.SkipDir
		}
		registers, bundle := scanPackage(t, path, module)
		if !registers && !bundle {
			return nil
		}
		found++
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		pkg := module + "/" + filepath.ToSlash(rel)
		if bundle && !inAll[pkg] {
			problems = append(problems, pkg+" defines Ops(...) ops.Bundle but is not in opscatalog.All; add "+
				filepath.Base(path)+".Ops to All in internal/opscatalog/catalog.go")
		}
		if !linked[pkg] {
			what := "registers with the service registry"
			if bundle {
				what = "defines Ops(...) ops.Bundle"
			}
			problems = append(problems, pkg+" "+what+" but is not linked into the server binary; reference it from internal/opscatalog/catalog.go")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(problems)
	return problems, found
}

// scanPackage reports whether dir's non-test Go files call
// serviceregistry.Register and whether they declare a package-level
// func Ops(...) ops.Bundle, with both packages resolved under module.
func scanPackage(t *testing.T, dir, module string) (registers, bundle bool) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		local := map[string]string{} // path -> local name
		for n, p := range importNames(f) {
			local[p] = n
		}
		opsName, regName := local[module+"/pkg/ops"], local[module+"/internal/serviceregistry"]
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.FuncDecl:
				if x.Recv == nil && x.Name.Name == "Ops" && returnsBundle(x, opsName) {
					bundle = true
				}
			case *ast.CallExpr:
				if sel, ok := x.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Register" {
					if id, ok := sel.X.(*ast.Ident); ok && regName != "" && id.Name == regName {
						registers = true
					}
				}
			}
			return true
		})
	}
	return registers, bundle
}

func returnsBundle(fn *ast.FuncDecl, opsName string) bool {
	if opsName == "" || fn.Type.Results == nil || len(fn.Type.Results.List) != 1 {
		return false
	}
	sel, ok := fn.Type.Results.List[0].Type.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Bundle" {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == opsName
}
