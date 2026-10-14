// file: internal/arch/opstest_guard_test.go
// version: 1.0.0
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
// the registry's test seam.
func TestOpstestStaysInTests(t *testing.T) {
	root := repoRoot(t)
	pkgs := listPackages(t, root)
	if len(pkgs) < minModulePackages {
		t.Fatalf("the loader is broken, not the code: loaded %d module packages, want at least %d", len(pkgs),
			minModulePackages)
	}
	sawHarness, sawSeam := false, false
	for _, p := range pkgs {
		key, ok := rel(p.ImportPath)
		if !ok {
			continue
		}
		for _, imp := range p.Imports {
			if k, ok := rel(imp); ok && k == opstestPkg && key != opstestPkg {
				t.Errorf("package %s imports %s from a non-test file: the harness drives the registry's test "+
					"seam and must stay out of production code; import it from a _test.go file", key, opstestPkg)
			}
		}
		owner := slices.Contains(seamOwners, key)
		for _, f := range slices.Concat(p.GoFiles, p.TestGoFiles, p.XTestGoFiles) {
			for _, name := range seamUses(t, filepath.Join(p.Dir, f)) {
				if key == opstestPkg {
					sawHarness = true
				}
				if key == "internal/operations/registry" {
					sawSeam = true
				}
				if !owner {
					t.Errorf("%s names the registry test seam %s: only %v may", filepath.Join(key, f), name,
						seamOwners)
				}
			}
		}
	}
	if !sawSeam || !sawHarness {
		t.Fatalf("the scan is broken, not the code: the seam was seen in the registry %v and the harness %v; "+
			"both must use it", sawSeam, sawHarness)
	}
}

type listedPackage struct {
	ImportPath, Dir                    string
	Imports                            []string
	GoFiles, TestGoFiles, XTestGoFiles []string
}

func listPackages(t *testing.T, root string) []listedPackage {
	t.Helper()
	cmd := exec.Command("go", "list", "-json=ImportPath,Dir,Imports,GoFiles,TestGoFiles,XTestGoFiles", "./...")
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
