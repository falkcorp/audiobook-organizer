// file: core/archtest/tasklint/load_test.go
// version: 1.0.0
// guid: 5d0f1c2e-8a43-4c61-9b27-3e6a1f7d9c04
// last-edited: 2026-11-21

package tasklint

import (
	"go/types"
	"testing"
)

// TestLoadTyped: loadTyped hands Analyze what packages.LoadSyntax did (types,
// info, sizes, one FileSet, no errors) and one type universe: kernel imports
// owner, a package of the same load, so kernel's import of it is owner's own
// *types.Package, and a package none of them loads from source (tasks, read
// from export data) is one *types.Package to owner and stray. The checker's identity comparisons, and
// the unregistered-kind rule across packages, depend on it.
func TestLoadTyped(t *testing.T) {
	fixtureGOPATH(t)
	x := cases + "xpkg/"
	pkgs, err := loadTyped([]string{x + "kernel", x + "owner", x + "stray"})
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 3 {
		t.Fatalf("loaded %d packages, want 3", len(pkgs))
	}
	byPath := map[string]*types.Package{}
	for _, p := range pkgs {
		switch {
		case p.Types == nil || !p.Types.Complete():
			t.Errorf("%s: Types missing or incomplete", p.PkgPath)
		case p.TypesInfo == nil || len(p.TypesInfo.Defs) == 0 || len(p.TypesInfo.Uses) == 0:
			t.Errorf("%s: TypesInfo not filled", p.PkgPath)
		case p.TypesSizes == nil || p.Fset == nil || p.Fset != pkgs[0].Fset:
			t.Errorf("%s: TypesSizes or the shared FileSet missing", p.PkgPath)
		case len(p.Syntax) == 0:
			t.Errorf("%s: no syntax", p.PkgPath)
		case len(p.Errors) != 0 || p.IllTyped:
			t.Errorf("%s: errors %v, IllTyped %v", p.PkgPath, p.Errors, p.IllTyped)
		}
		byPath[p.PkgPath] = p.Types
	}
	kernel, owner, stray := byPath[x+"kernel"], byPath[x+"owner"], byPath[x+"stray"]
	if kernel == nil || owner == nil || stray == nil {
		t.Fatalf("loaded %v, want kernel, owner and stray", byPath)
	}
	imported := func(from *types.Package) map[string]*types.Package {
		m := map[string]*types.Package{}
		for _, ip := range from.Imports() {
			m[ip.Path()] = ip
		}
		return m
	}
	if got := imported(kernel)[x+"owner"]; got != owner {
		t.Errorf("kernel imports a different owner than the one loaded: %p vs %p", got, owner)
	}
	oi, si := imported(owner), imported(stray)
	shared := 0
	for path, op := range oi {
		if sp, ok := si[path]; ok {
			shared++
			if op != sp {
				t.Errorf("%s: owner and stray see different *types.Package values", path)
			}
		}
	}
	if shared == 0 {
		t.Error("owner and stray share no import: the fixture no longer exercises the shared universe")
	}
}
