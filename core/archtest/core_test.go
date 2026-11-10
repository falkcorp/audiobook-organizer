// file: core/archtest/core_test.go
// version: 1.0.2
// guid: bae86fdb-f804-4079-b250-514259741720
// last-edited: 2026-11-09

package archtest

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const corePath = "github.com/falkcorp/mediacore"

// coreRoot is the core module root: the go.mod above this package.
func coreRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root, err := ModuleRoot(wd)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// thirdPartyAllow reads thirdparty_allow.txt: one import-path prefix per line.
func thirdPartyAllow(t *testing.T, root string) []string {
	t.Helper()
	f, err := os.Open(filepath.Join(root, "archtest", "thirdparty_allow.txt"))
	if err != nil {
		t.Fatalf("read the third-party allowlist: %v", err)
	}
	defer func() { _ = f.Close() }()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// isStdlib reports a standard-library import path: no dot in its first
// element (the go command's own rule for the standard library).
func isStdlib(p string) bool {
	first, _, _ := strings.Cut(p, "/")
	return !strings.Contains(first, ".")
}

// outsideCore returns every import of pkgs that is not core, stdlib, or under
// an allowlisted prefix, as "importer -> import" lines.
func outsideCore(pkgs map[string]*Package, allow []string) []string {
	var bad []string
	for _, p := range pkgs {
		for _, imp := range append(append([]string(nil), p.Imports...), p.TestImports...) {
			if imp == "C" || isStdlib(imp) || imp == corePath || strings.HasPrefix(imp, corePath+"/") {
				continue
			}
			ok := false
			for _, a := range allow {
				if exact, isExact := strings.CutPrefix(a, "="); isExact {
					ok = imp == exact
				} else {
					ok = imp == a || strings.HasPrefix(imp, a+"/")
				}
				if ok {
					break
				}
			}
			if !ok {
				bad = append(bad, p.ImportPath+" -> "+imp)
			}
		}
	}
	sort.Strings(bad)
	return uniq(bad)
}

// TestCoreImportsOnlyCore is 03 R1(b): no core package, test files included,
// imports a path outside github.com/falkcorp/mediacore/..., the standard
// library and thirdparty_allow.txt, under any (GOOS, tag set) of the one list.
func TestCoreImportsOnlyCore(t *testing.T) {
	root := coreRoot(t)
	pkgs, err := Load(LoadOptions{Dir: root, TagSets: TagSets()})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := pkgs[corePath+"/archtest"]; !ok {
		t.Fatalf("the loader is broken, not the code: archtest itself is missing from %d packages", len(pkgs))
	}
	if bad := outsideCore(pkgs, thirdPartyAllow(t, root)); len(bad) > 0 {
		t.Errorf("core imports outside core, the standard library and archtest/thirdparty_allow.txt (03 R1(b)):\n  %s",
			strings.Join(bad, "\n  "))
	}

	// Positive control: the same scan flags an app import in a test file.
	fake := map[string]*Package{corePath + "/x": {
		ImportPath:  corePath + "/x",
		TestImports: []string{"github.com/falkcorp/audiobook-organizer/internal/books/store"},
	}}
	if got := outsideCore(fake, nil); len(got) != 1 {
		t.Fatalf("control: an app import in a test file was not flagged: %v", got)
	}

	// Control for the exact form: "=a/b" admits a/b and not a/b/c.
	exact := map[string]*Package{corePath + "/y": {
		ImportPath: corePath + "/y",
		Imports:    []string{"example.com/gotham", "example.com/gotham/arkham"},
	}}
	if got := outsideCore(exact, []string{"=example.com/gotham"}); len(got) != 1 || !strings.HasSuffix(got[0], "example.com/gotham/arkham") {
		t.Fatalf("control: =example.com/gotham must admit only that path, flagged %v", got)
	}
}

// appModule reports a module path that belongs to an app: any module under
// github.com/falkcorp/ other than core itself.
func appModule(p string) bool {
	return strings.HasPrefix(p, "github.com/falkcorp/") && p != corePath && !strings.HasPrefix(p, corePath+"/")
}

// requiredModules lists the require directives of the go.mod at path.
func requiredModules(t *testing.T, gomod string) []string {
	t.Helper()
	out, err := exec.Command("go", "mod", "edit", "-json", gomod).Output()
	if err != nil {
		t.Fatalf("go mod edit -json %s: %v", gomod, err)
	}
	var m struct {
		Require []struct{ Path string }
	}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, r := range m.Require {
		paths = append(paths, r.Path)
	}
	return paths
}

// TestNoAppRequire is 03 R1(a): core/go.mod requires no app module. Go allows
// module cycles, so this is not implied by TestCoreImportsOnlyCore: a core
// go mod tidy silently adds the app module when any core file imports it.
func TestNoAppRequire(t *testing.T) {
	gomod := filepath.Join(coreRoot(t), "go.mod")
	for _, p := range requiredModules(t, gomod) {
		if appModule(p) {
			t.Errorf("core/go.mod requires app module %s (03 R1(a))", p)
		}
	}

	// Positive control on a fixture go.mod.
	fixture := filepath.Join(t.TempDir(), "go.mod")
	body := "module " + corePath + "\n\ngo 1.27.0\n\nrequire github.com/falkcorp/audiobook-organizer v1.0.0\n"
	if err := os.WriteFile(fixture, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range requiredModules(t, fixture) {
		found = found || appModule(p)
	}
	if !found {
		t.Fatal("control: a fixture go.mod requiring the app module was not flagged")
	}
}

// TestNoInitOverCore runs NoInit over every core package (06 §6.10; the
// core/examples/... scope arrives with R1-13's hello).
func TestNoInitOverCore(t *testing.T) {
	NoInit(t, coreRoot(t))
}
