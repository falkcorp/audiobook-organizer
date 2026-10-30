// file: internal/arch/hashcost_guard_test.go
// version: 1.0.0
// guid: fb8d6b68-29d5-47f5-a837-68859e23e852
// last-edited: 2026-10-30

package arch

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// hashCostSeam lowers the argon2id cost every password is written at, so it
// belongs to test binaries' TestMain and nowhere else. A call from a file that
// ships would make every password the server stores cheap to guess. The
// function panics outside a test binary as well; this guard catches the call
// before it can reach one.
const hashCostSeam = "SetHashCostForTest"

// hashCostOwner declares the seam.
const hashCostOwner = "internal/server/absauth"

// hashCostUse is one file that names the seam: the file (relative to its
// package directory), whether it is a test file, and whether the name is its
// function declaration.
type hashCostUse struct {
	File       string
	Test, Decl bool
}

// hashCostUses lists the Go files in p's directory that name hashCostSeam as
// an identifier (a call, a selector, a function value, a dot-imported use).
// It reads the directory, not only the files go list selected for this build,
// so a file behind a build tag or another GOOS, a cgo file, and the root main
// package are all scanned. Comments and strings do not count: only an
// identifier can call it.
func hashCostUses(t *testing.T, p listedPackage) []hashCostUse {
	t.Helper()
	entries, err := os.ReadDir(p.Dir)
	if err != nil {
		t.Fatalf("read %s: %v", p.Dir, err)
	}
	var out []hashCostUse
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		path := filepath.Join(p.Dir, e.Name())
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if !bytes.Contains(src, []byte(hashCostSeam)) {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		decls := map[*ast.Ident]bool{}
		for _, d := range f.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == hashCostSeam {
				decls[fn.Name] = true
			}
		}
		use := hashCostUse{File: e.Name(), Test: strings.HasSuffix(e.Name(), "_test.go")}
		named := false
		ast.Inspect(f, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && id.Name == hashCostSeam {
				named = true
				use.Decl = use.Decl || decls[id]
			}
			return true
		})
		if named {
			out = append(out, use)
		}
	}
	return out
}

// TestHashCostSeamStaysInTests: no non-test file in the module names
// SetHashCostForTest, other than the declaration in absauth. It covers every
// package go list finds, the root main package included, and the files each
// package's directory holds that the build excludes.
func TestHashCostSeamStaysInTests(t *testing.T) {
	root := repoRoot(t)
	pkgs := listPackages(t, root)
	if len(pkgs) < minModulePackages {
		t.Fatalf("the loader is broken, not the code: loaded %d module packages, want at least %d", len(pkgs),
			minModulePackages)
	}
	sawDecl, sawTestUse, sawRoot := false, false, false
	for _, p := range pkgs {
		key, ok := rel(p.ImportPath)
		if !ok {
			continue
		}
		sawRoot = sawRoot || key == "."
		for _, u := range hashCostUses(t, p) {
			switch {
			case u.Test:
				sawTestUse = true
			case u.Decl && key == hashCostOwner:
				sawDecl = true
			default:
				t.Errorf("%s names %s: production code must not lower the password hash cost; "+
					"call it from a _test.go TestMain only", filepath.Join(key, u.File), hashCostSeam)
			}
		}
	}
	if !sawRoot || !sawDecl || !sawTestUse {
		t.Fatalf("the scan is broken, not the code: the root package was loaded %v, the declaration in %s "+
			"seen %v, a test file's use seen %v; all three must hold", sawRoot, hashCostOwner, sawDecl, sawTestUse)
	}
}

// TestHashCostGuardSeesTheRootPackageAndBuildTaggedFiles: the scan reads the
// root main package, a file behind a build tag, and a file a comment or string
// merely mentions. A scratch module holds a root main.go and a tagged file
// that call the seam, a test file that may, a file that only mentions the name
// in a comment and a string, and a method of the same name; only the first two
// are reported.
func TestHashCostGuardSeesTheRootPackageAndBuildTaggedFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.test/m\n\ngo 1.21\n")
	write("a/a.go", "package a\n\nfunc SetHashCostForTest(t, m uint32) {}\n")
	write("main.go", "package main\n\nimport \"example.test/m/a\"\n\nfunc main() { a.SetHashCostForTest(1, 64) }\n")
	write("main_test.go", "package main\n\nimport (\n\t\"testing\"\n\n\t\"example.test/m/a\"\n)\n\n"+
		"func TestMain(m *testing.M) { a.SetHashCostForTest(1, 64) }\n")
	write("tagged.go", "//go:build never\n\npackage main\n\nimport \"example.test/m/a\"\n\n"+
		"var _ = a.SetHashCostForTest\n")
	write("mention.go", "package main\n\n// SetHashCostForTest is not called here.\nvar note = \"SetHashCostForTest\"\n")
	write("method.go", "package main\n\ntype T struct{}\n\nfunc (T) SetHashCostForTest() {}\n")

	var root *listedPackage
	pkgs := listPackages(t, dir)
	for i := range pkgs {
		if pkgs[i].ImportPath == "example.test/m" {
			root = &pkgs[i]
		}
	}
	if root == nil {
		t.Fatalf("go list did not list the scratch root package: %+v", pkgs)
	}
	if slices.Contains(root.GoFiles, "tagged.go") {
		t.Fatalf("the build builds tagged.go (%v); the build tag no longer excludes it, so this test proves "+
			"nothing about excluded files", root.GoFiles)
	}
	var got []string
	for _, u := range hashCostUses(t, *root) {
		if !u.Test {
			got = append(got, u.File)
		}
	}
	slices.Sort(got)
	// method.go names the identifier as a method declaration, which is not the
	// package-level function but is still a use of the name a reviewer should
	// see; it is reported, a comment or string is not.
	if want := []string{"main.go", "method.go", "tagged.go"}; !slices.Equal(got, want) {
		t.Errorf("reported %v, want %v", got, want)
	}
}
