// file: core/archtest/typedload/load_test.go
// version: 1.0.0
// guid: 5d0f1c2e-8a43-4c61-9b27-3e6a1f7d9c04
// last-edited: 2026-11-21

package typedload

import (
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// module writes a module of files (path -> source) and returns its directory
// and a Config that loads it offline. The module is named m.test.
func module(t *testing.T, mode packages.LoadMode, files map[string]string) *packages.Config {
	t.Helper()
	dir := t.TempDir()
	files["go.mod"] = "module m.test\n\ngo 1.27\n"
	for name, src := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &packages.Config{Mode: mode, Dir: dir,
		Env: append(os.Environ(), "GOFLAGS=-mod=mod", "GOWORK=off", "GOPROXY=off", "CGO_ENABLED=0")}
}

// twoPackages: a returns an instantiated generic from the standard library
// (the shape that races under packages.Load), b imports a, and both import
// strings.
var twoPackages = map[string]string{
	"a/a.go": `package a

import (
	"iter"
	"maps"
	"strings"
)

func Keys(m map[string]int) iter.Seq[string] { return maps.Keys(m) }

func Up(s string) string { return strings.ToUpper(s) }
`,
	"b/b.go": `package b

import (
	"strings"

	"m.test/a"
)

func Names(m map[string]int) []string {
	var out []string
	for k := range a.Keys(m) {
		out = append(out, strings.TrimSpace(k))
	}
	return out
}
`,
}

func imported(from *types.Package) map[string]*types.Package {
	m := map[string]*types.Package{}
	for _, ip := range from.Imports() {
		m[ip.Path()] = ip
	}
	return m
}

// TestLoad_LoadSyntax: Load hands back what packages.LoadSyntax would (types,
// info, sizes, one FileSet, no errors) in one type universe: b's import of a is
// a's own *types.Package, and strings, read from export data, is one
// *types.Package to both. Object identity across packages, which an analysis
// over several roots compares, depends on it.
func TestLoad_LoadSyntax(t *testing.T) {
	pkgs, err := Load(module(t, packages.LoadSyntax, twoPackages), "m.test/a", "m.test/b")
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 2 {
		t.Fatalf("loaded %d packages, want 2", len(pkgs))
	}
	byPath := map[string]*packages.Package{}
	for _, p := range pkgs {
		byPath[p.PkgPath] = p
		switch {
		case p.Types == nil || !p.Types.Complete():
			t.Errorf("%s: Types missing or incomplete", p.PkgPath)
		case p.TypesInfo == nil || len(p.TypesInfo.Defs) == 0 || len(p.TypesInfo.Uses) == 0 || len(p.TypesInfo.Instances) == 0 && p.PkgPath == "m.test/a":
			t.Errorf("%s: TypesInfo not filled", p.PkgPath)
		case p.TypesSizes == nil || p.Fset == nil || p.Fset != pkgs[0].Fset:
			t.Errorf("%s: TypesSizes or the shared FileSet missing", p.PkgPath)
		case len(p.Syntax) == 0:
			t.Errorf("%s: no syntax", p.PkgPath)
		case len(p.Errors) != 0 || p.IllTyped:
			t.Errorf("%s: errors %v, IllTyped %v", p.PkgPath, p.Errors, p.IllTyped)
		}
	}
	a, b := byPath["m.test/a"], byPath["m.test/b"]
	if a == nil || b == nil {
		t.Fatalf("loaded %v, want a and b", byPath)
	}
	if got := imported(b.Types)["m.test/a"]; got != a.Types {
		t.Errorf("b imports a different a than the one loaded: %p vs %p", got, a.Types)
	}
	as, bs := imported(a.Types)["strings"], imported(b.Types)["strings"]
	if as == nil || as != bs {
		t.Errorf("a and b see different strings packages: %p vs %p", as, bs)
	}
	// A direct dependency read from export data keeps its Types, as with packages.Load.
	if s := b.Imports["strings"]; s == nil || s.Types != bs {
		t.Error("b.Imports[strings].Types is not the package b was checked against")
	}
	// The instance is complete and expands (go/types expands it lazily).
	keys := a.Types.Scope().Lookup("Keys").Type().(*types.Signature)
	if u := keys.Results().At(0).Type().Underlying(); u == nil {
		t.Error("iter.Seq[string] has no underlying type")
	}
}

// TestLoad_LoadAllSyntax: with NeedDeps every package of the graph, the
// standard library included, is checked from source (SSA and call-graph
// callers read dependency bodies), in one universe.
func TestLoad_LoadAllSyntax(t *testing.T) {
	pkgs, err := Load(module(t, packages.LoadAllSyntax, twoPackages), "m.test/b")
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 1 {
		t.Fatalf("loaded %d packages, want 1", len(pkgs))
	}
	n := 0
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		n++
		if p.PkgPath == "unsafe" {
			if p.Types != types.Unsafe {
				t.Error("unsafe is not types.Unsafe")
			}
			return
		}
		if p.Types == nil || !p.Types.Complete() || p.TypesInfo == nil || len(p.Syntax) == 0 || p.Fset == nil {
			t.Errorf("%s: not loaded from source (Types %v, syntax %d)", p.PkgPath, p.Types != nil, len(p.Syntax))
		}
		if len(p.Errors) != 0 || p.IllTyped {
			t.Errorf("%s: errors %v", p.PkgPath, p.Errors)
		}
		for path, ip := range p.Imports {
			if path != "unsafe" && ip.Types != nil && imported(p.Types)[ip.PkgPath] != ip.Types {
				t.Errorf("%s imports a different %s than the one loaded", p.PkgPath, ip.PkgPath)
			}
		}
	})
	if n < 10 {
		t.Errorf("visited %d packages; the standard library was not loaded from source", n)
	}
}

// TestLoad_Overlay: a file only in the overlay is part of the package, as it
// is for packages.Load.
func TestLoad_Overlay(t *testing.T) {
	cfg := module(t, packages.LoadSyntax, twoPackages)
	extra := filepath.Join(cfg.Dir, "a", "extra.go")
	cfg.Overlay = map[string][]byte{extra: []byte("package a\n\nfunc Extra() int { return 1 }\n")}
	pkgs, err := Load(cfg, "m.test/a")
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs[0].Errors) != 0 || pkgs[0].Types.Scope().Lookup("Extra") == nil {
		t.Errorf("overlay file not loaded: errors %v", pkgs[0].Errors)
	}
}

// TestLoad_TypeError: a type error is reported as packages.Load reports it,
// on the package and, through IllTyped, on its dependents.
func TestLoad_TypeError(t *testing.T) {
	files := map[string]string{
		"a/a.go": "package a\n\nfunc F() int { return \"x\" }\n",
		"b/b.go": "package b\n\nimport \"m.test/a\"\n\nvar _ = a.F\n",
	}
	pkgs, err := Load(module(t, packages.LoadSyntax, files), "m.test/a", "m.test/b")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pkgs {
		switch p.PkgPath {
		case "m.test/a":
			// go list's own build error for the package comes with it, as
			// with packages.Load; the type error must be among the errors.
			found := false
			for _, e := range p.Errors {
				found = found || e.Kind == packages.TypeError && strings.Contains(e.Msg, "cannot use") && strings.Contains(e.Pos, "a.go:3")
			}
			if !found || !p.IllTyped {
				t.Errorf("a: errors %v, IllTyped %v, want a type error at a.go:3", p.Errors, p.IllTyped)
			}
		case "m.test/b":
			if len(p.Errors) != 0 || !p.IllTyped {
				t.Errorf("b: errors %v, IllTyped %v, want clean but IllTyped through a", p.Errors, p.IllTyped)
			}
		}
	}
}

// TestLoad_Refusals: a Config the loader cannot honour is refused, not
// loaded wrongly.
func TestLoad_Refusals(t *testing.T) {
	if _, err := Load(nil); err == nil {
		t.Error("nil Config accepted")
	}
	if _, err := Load(&packages.Config{Mode: packages.NeedName | packages.NeedImports}, "std"); err == nil {
		t.Error("a Mode without NeedTypes accepted")
	}
	cfg := module(t, packages.LoadSyntax, twoPackages)
	cfg.Tests = true
	if _, err := Load(cfg, "m.test/a"); err == nil {
		t.Error("Tests without NeedDeps accepted")
	}
}
