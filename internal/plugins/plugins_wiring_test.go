// file: internal/plugins/plugins_wiring_test.go
// version: 1.0.0
// guid: 9c1e4b7a-3d2f-4e8a-b5c6-7f0a1d2e3b4c
// last-edited: 2026-10-01

package plugins

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const modulePath = "github.com/falkcorp/audiobook-organizer"

// TestEveryRegistryPluginIsLinkedIntoTheBinary checks the server binary's
// real import graph -- `go list -deps` on the main package, which leaves out
// test files -- for every plugin package that registers with the service
// registry. A test-only import cannot satisfy it: that is how metafetch's ops
// passed every test and were missing from prod for weeks.
func TestEveryRegistryPluginIsLinkedIntoTheBinary(t *testing.T) {
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

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, e := range entries {
		if !e.IsDir() || !registersWithServiceRegistry(t, e.Name()) {
			continue
		}
		checked++
		pkg := modulePath + "/internal/plugins/" + e.Name()
		if !linked[pkg] {
			t.Errorf("%s registers with the service registry but is not linked into the server binary; add it to internal/plugins/plugins.go", pkg)
		}
	}
	if checked == 0 {
		t.Fatal("found no service-registry plugin packages; the scan is broken")
	}
}

// registersWithServiceRegistry reports whether a plugin directory's non-test
// Go files call serviceregistry.Register.
func registersWithServiceRegistry(t *testing.T, dir string) bool {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "serviceregistry.Register(") {
			return true
		}
	}
	return false
}
