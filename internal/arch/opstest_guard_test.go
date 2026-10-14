// file: internal/arch/opstest_guard_test.go
// version: 1.1.0
// guid: 689ac799-2e34-44c5-94e9-69f76ee56ac0
// last-edited: 2026-10-14

package arch

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// opstestPkg is the operations test harness. It drives a real registry
// through the registry's test seam (Options.RunnerHooksForTest and the
// RunnerHooks it returns), so it must never reach a production binary, and
// nothing but the harness may use that seam.
const opstestPkg = "pkg/ops/opstest"

// registryTestSeam are the registry identifiers only the harness (and the
// registry's own code and tests) may name.
var registryTestSeam = []string{"RunnerHooksForTest", "RunnerHooks"}

// seamOwners may name the seam: the registry declares and threads it, the
// harness uses it.
var seamOwners = []string{"internal/operations/registry", opstestPkg}

// TestOpstestStaysInTests: no package's non-test files import
// pkg/ops/opstest, and no file outside the registry and the harness names
// the registry's test seam. The scan covers files the build excludes (a
// build tag, another GOOS): they compile on some other build, and a seam use
// or a harness import hidden in one is as live as in any other file.
func TestOpstestStaysInTests(t *testing.T) {
	root := repoRoot(t)
	pkgs := listPackages(t, root)
	if len(pkgs) < minModulePackages {
		t.Fatalf("the loader is broken, not the code: loaded %d module packages, want at least %d", len(pkgs),
			minModulePackages)
	}
	harness := modulePath + "/" + opstestPkg
	sawHarness, sawSeam := false, false
	for _, p := range pkgs {
		key, ok := rel(p.ImportPath)
		if !ok {
			continue
		}
		scan := scanPackage(t, p, harness)
		if key != opstestPkg {
			if scan.builtImportsHarness {
				t.Errorf("package %s imports %s from a non-test file: the harness drives the registry's test "+
					"seam and must stay out of production code; import it from a _test.go file", key, opstestPkg)
			}
			for _, f := range scan.ignoredImporters {
				t.Errorf("%s imports %s from a non-test file the build excludes (a build tag or another GOOS): "+
					"the harness must stay out of production code on every build; import it from a _test.go file",
					filepath.Join(key, f), opstestPkg)
			}
		}
		owner := slices.Contains(seamOwners, key)
		for _, u := range scan.seamUses {
			if key == opstestPkg {
				sawHarness = true
			}
			if key == "internal/operations/registry" {
				sawSeam = true
			}
			if !owner {
				t.Errorf("%s names the registry test seam %s: only %v may", filepath.Join(key, u.File), u.Name,
					seamOwners)
			}
		}
	}
	if !sawSeam || !sawHarness {
		t.Fatalf("the scan is broken, not the code: the seam was seen in the registry %v and the harness %v; "+
			"both must use it", sawSeam, sawHarness)
	}
}

// TestOpstestGuardSeesBuildTaggedFiles: the scan reads the files go list
// excludes from a package (IgnoredGoFiles), not only the ones it builds. A
// scratch module holds a non-test file behind a build tag that imports the
// harness and names the seam, a tagged test file that may import the
// harness, and a plain file that does neither; only the first is reported.
func TestOpstestGuardSeesBuildTaggedFiles(t *testing.T) {
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
	write("h/h.go", "package h\n\ntype RunnerHooks struct{}\n")
	write("p/p.go", "package p\n")
	write("p/tagged.go", "//go:build never\n\npackage p\n\nimport x \"example.test/m/h\"\n\n"+
		"var _ = x.RunnerHooks{}\n")
	write("p/tagged_test.go", "//go:build never\n\npackage p\n\nimport _ \"example.test/m/h\"\n")

	var p *listedPackage
	pkgs := listPackages(t, dir)
	for i := range pkgs {
		if pkgs[i].ImportPath == "example.test/m/p" {
			p = &pkgs[i]
		}
	}
	if p == nil {
		t.Fatalf("go list did not list the scratch package: %+v", pkgs)
	}
	if len(p.Imports) != 0 {
		t.Fatalf("the built files of the scratch package import %v; the build tag no longer hides the import "+
			"from go list, so this test proves nothing", p.Imports)
	}
	scan := scanPackage(t, *p, "example.test/m/h")
	if scan.builtImportsHarness || !slices.Equal(scan.ignoredImporters, []string{"tagged.go"}) {
		t.Errorf("the build-tagged non-test file that imports the harness was reported as %v (built files: %v), "+
			"want [tagged.go] (the tagged test file may import it)", scan.ignoredImporters, scan.builtImportsHarness)
	}
	if len(scan.seamUses) != 1 || scan.seamUses[0].File != "tagged.go" || scan.seamUses[0].Name != "RunnerHooks" {
		t.Errorf("the seam use in the build-tagged file was reported as %+v, want RunnerHooks in tagged.go",
			scan.seamUses)
	}
}

type listedPackage struct {
	ImportPath, Dir                                    string
	Imports                                            []string
	GoFiles, TestGoFiles, XTestGoFiles, IgnoredGoFiles []string
}

func listPackages(t *testing.T, root string) []listedPackage {
	t.Helper()
	cmd := exec.Command("go", "list", "-json=ImportPath,Dir,Imports,GoFiles,TestGoFiles,XTestGoFiles,IgnoredGoFiles",
		"./...")
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("go list: %v\n%s", err, stderr.String())
	}
	var out []listedPackage
	dec := json.NewDecoder(&stdout)
	for {
		var p listedPackage
		if err := dec.Decode(&p); err == io.EOF {
			return out
		} else if err != nil {
			t.Fatalf("decode go list output: %v", err)
		}
		out = append(out, p)
	}
}

// seamUse is one use of a registry test seam identifier, in a file of the
// package (relative to its directory).
type seamUse struct{ File, Name string }

// packageScan is what scanPackage found in one package.
type packageScan struct {
	// builtImportsHarness: the package's built non-test files import the
	// harness. ignoredImporters are the non-test files the build excludes
	// (relative to the package directory) that do.
	builtImportsHarness bool
	ignoredImporters    []string
	// seamUses are the seam identifiers any file names, tests included.
	seamUses []seamUse
}

// scanPackage reads p's files for harness imports and seam uses. Built files
// are read as go list reports them (Imports for the import check); the files
// the build excludes (IgnoredGoFiles) are parsed here, because go list
// leaves their imports out. An ignored _test.go file may import the harness,
// like any test file; an ignored non-test file may not.
func scanPackage(t *testing.T, p listedPackage, harnessImport string) packageScan {
	t.Helper()
	out := packageScan{builtImportsHarness: slices.Contains(p.Imports, harnessImport)}
	for _, f := range slices.Concat(p.GoFiles, p.TestGoFiles, p.XTestGoFiles, p.IgnoredGoFiles) {
		path := filepath.Join(p.Dir, f)
		for _, name := range seamUses(t, path) {
			out.seamUses = append(out.seamUses, seamUse{File: f, Name: name})
		}
	}
	for _, f := range p.IgnoredGoFiles {
		if !strings.HasSuffix(f, "_test.go") && slices.Contains(importsOf(t, filepath.Join(p.Dir, f)), harnessImport) {
			out.ignoredImporters = append(out.ignoredImporters, f)
		}
	}
	return out
}

// importsOf lists the import paths of the Go file at path.
func importsOf(t *testing.T, path string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var out []string
	for _, imp := range f.Imports {
		if v, err := strconv.Unquote(imp.Path.Value); err == nil {
			out = append(out, v)
		}
	}
	return out
}

// seamUses lists the registry test seam identifiers path names, as an
// identifier or a selector (registry.RunnerHooks, Options{RunnerHooksForTest:
// ...}). Files that do not mention them are not parsed.
func seamUses(t *testing.T, path string) []string {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !bytes.Contains(src, []byte("RunnerHooks")) {
		return nil
	}
	f, err := parser.ParseFile(token.NewFileSet(), path, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && slices.Contains(registryTestSeam, id.Name) {
			out = append(out, id.Name)
		}
		return true
	})
	return out
}
