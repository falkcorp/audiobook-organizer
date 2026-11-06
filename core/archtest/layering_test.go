// file: core/archtest/layering_test.go
// version: 1.0.0
// guid: 502dfb95-415e-4131-9ce5-df02abad382c
// last-edited: 2026-11-05

package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// syntheticSpec is a three-layer module: leaf (0) <- mid (1) <- top (2).
func syntheticSpec() LayerSpec {
	return LayerSpec{
		ModulePath: "example.com/m",
		LayerOf:    map[string]int{"leaf": 0, "mid": 1, "top": 2},
		Named:      map[string]int{"leaf": 0, "mid": 1, "top": 2},
		FloorRatio: 1,
		Control:    Edge{"mid", "leaf"},
	}
}

func hasProblem(problems []string, substr string) bool {
	for _, p := range problems {
		if strings.Contains(p, substr) {
			return true
		}
	}
	return false
}

// TestLayeringEngineSelfTest is the core guard's first control (03 §3.5.1
// item 2): on a synthetic graph the engine reports a known violation, passes a
// clean graph, and fails an empty one.
func TestLayeringEngineSelfTest(t *testing.T) {
	spec := syntheticSpec()
	clean := Graph{Imports: map[string][]string{"leaf": {}, "mid": {"leaf"}, "top": {"mid"}}}
	if p := CheckGraph(spec, clean); len(p) != 0 {
		t.Fatalf("clean graph reported problems: %v", p)
	}

	bad := Graph{Imports: map[string][]string{"leaf": {"top"}, "mid": {"leaf"}, "top": {"mid"}}}
	if p := CheckGraph(spec, bad); !hasProblem(p, "layering violation: leaf (layer 0) imports top (layer 2)") {
		t.Fatalf("known violation leaf -> top not reported: %v", p)
	}

	if p := CheckGraph(spec, Graph{}); !hasProblem(p, "graph holds no package") {
		t.Fatalf("empty graph not failed: %v", p)
	}

	noControl := Graph{Imports: map[string][]string{"leaf": {}, "mid": {}, "top": {"mid"}}}
	if p := CheckGraph(spec, noControl); !hasProblem(p, "control edge mid -> leaf not found") {
		t.Fatalf("missing control edge not failed: %v", p)
	}

	small := syntheticSpec()
	small.FloorBase = 10
	if p := CheckGraph(small, clean); !hasProblem(p, "loaded 3 packages, want at least 10") {
		t.Fatalf("loader floor not enforced: %v", p)
	}
}

// TestLayeringEngineRatchetAndCompleteness covers the allowed list (shrink-only
// both ways), layerOf completeness and the computed-layer equality.
func TestLayeringEngineRatchetAndCompleteness(t *testing.T) {
	spec := syntheticSpec()
	spec.Allowed = map[Edge]string{{"leaf", "top"}: "known", {"mid", "leaf"}: "legal edge"}
	g := Graph{Imports: map[string][]string{"leaf": {"top"}, "mid": {"leaf"}, "top": {"mid"}, "new": {}}}
	p := CheckGraph(spec, g)
	for _, want := range []string{
		`package "new" has no entry in layerOf`,
		"allowed lists mid -> leaf, but that edge is legal",
	} {
		if !hasProblem(p, want) {
			t.Errorf("missing problem %q in %v", want, p)
		}
	}
	if hasProblem(p, "layering violation: leaf") {
		t.Errorf("allowed edge reported as a violation: %v", p)
	}

	gone := Graph{Imports: map[string][]string{"leaf": {}, "mid": {"leaf"}, "top": {"mid"}}}
	spec.Allowed = map[Edge]string{{"leaf", "top"}: "fixed since"}
	if p := CheckGraph(spec, gone); !hasProblem(p, "that import no longer exists") {
		t.Errorf("stale allowed entry not reported: %v", p)
	}

	// Computed layer: an unnamed package takes max(imports) floored at ImportFloor.
	spec = syntheticSpec()
	spec.Named = map[string]int{"leaf": 0, "top": 2}
	spec.ImportFloor = 1
	spec.LayerOf["mid"] = 2
	if p := CheckGraph(spec, gone); !hasProblem(p, `layerOf["mid"] is 2 but the classification rule computes 1`) {
		t.Errorf("declared/computed mismatch not reported: %v", p)
	}

	// Absent declared packages fail unless DeclaredMayBeAbsent.
	spec = syntheticSpec()
	spec.LayerOf["deferred"] = 1
	spec.FloorBase = 3
	if p := CheckGraph(spec, gone); !hasProblem(p, `layerOf lists "deferred"`) {
		t.Errorf("absent layerOf key not reported: %v", p)
	}
	spec.DeclaredMayBeAbsent = true
	if p := CheckGraph(spec, gone); len(p) != 0 {
		t.Errorf("DeclaredMayBeAbsent still reports: %v", p)
	}
}

// TestLayeringEngineForeignAndRestricted covers the foreign mapping (an ABO
// package importing a core path), the classifier control and the
// module/app import restriction, on a synthetic graph.
func TestLayeringEngineForeignAndRestricted(t *testing.T) {
	const core = "example.com/core/"
	spec := syntheticSpec()
	spec.ForeignPrefix = core
	spec.Foreign = func(p string) (int, bool) {
		switch strings.TrimPrefix(p, core) {
		case "keys":
			return -1, true
		case "httpapi":
			return 1, true
		case "module":
			return 2, true
		}
		return 0, false
	}
	spec.Restricted = map[string][]string{core + "module": {"top"}}
	spec.ForeignControl = Edge{"leaf", core + "keys"}

	g := Graph{
		Imports: map[string][]string{"leaf": {}, "mid": {"leaf"}, "top": {"mid"}},
		Foreign: map[string][]string{
			"leaf": {core + "httpapi"},                 // leaf (0) -> mapped 1: violation
			"mid":  {core + "module", core + "nosuch"}, // restricted, unmapped
			"top":  {core + "module"},                  // allowed importer
		},
		TestForeign: map[string][]string{"leaf": {core + "keys"}},
	}
	spec.LayerOf["leaf"] = 1 // computed from the mapped import: max(1) floored at 0
	spec.Named = map[string]int{"mid": 1, "top": 2}
	p := CheckGraph(spec, g)
	for _, want := range []string{
		"mid imports example.com/core/module; only top may import it",
		"mid imports example.com/core/nosuch, which has no mapped layer",
	} {
		if !hasProblem(p, want) {
			t.Errorf("missing problem %q in %v", want, p)
		}
	}
	if hasProblem(p, "top imports example.com/core/module; only") {
		t.Errorf("allowed importer of module reported: %v", p)
	}
	if hasProblem(p, "classifier") {
		t.Errorf("classifier control failed although leaf's test file imports core keys: %v", p)
	}
	if hasProblem(p, `layerOf["leaf"]`) {
		t.Errorf("a mapped core import did not feed the computed layer: %v", p)
	}

	// A -1 mapped import raises nothing; the control fails when absent.
	spec.LayerOf["leaf"] = 0
	g.Foreign["leaf"] = []string{core + "keys"}
	g.TestForeign = nil
	p = CheckGraph(spec, g)
	if hasProblem(p, `layerOf["leaf"]`) {
		t.Errorf("a core import mapped to -1 raised the computed layer: %v", p)
	}
	spec.ForeignControl = Edge{"top", core + "keys"}
	if p := CheckGraph(spec, g); !hasProblem(p, "control import top -> example.com/core/keys not found") {
		t.Errorf("missing classifier control not reported: %v", p)
	}

	// A wrong-way core import is checked like an in-module one.
	spec.ForeignControl = Edge{}
	g.Foreign["leaf"] = []string{core + "httpapi"}
	spec.LayerOf["leaf"] = 1
	spec.Named["leaf"] = 0
	if p := CheckGraph(spec, g); !hasProblem(p, "leaf (layer 1) imports example.com/core/httpapi") && !hasProblem(p, `layerOf["leaf"]`) {
		t.Errorf("expected a problem for leaf's core import: %v", p)
	}
}

// TestLayeringEngineTestSupport: test-support packages may import anything and
// no other package may import them.
func TestLayeringEngineTestSupport(t *testing.T) {
	spec := syntheticSpec()
	spec.TestSupport = func(p string) bool { return p == "kit" }
	g := Graph{Imports: map[string][]string{"leaf": {}, "mid": {"leaf", "kit"}, "top": {"mid"}, "kit": {"top"}}}
	p := CheckGraph(spec, g)
	if !hasProblem(p, "mid imports test-support package kit") {
		t.Errorf("non-test import of a test-support package not reported: %v", p)
	}
	if hasProblem(p, `"kit" has no entry`) || hasProblem(p, "layering violation: kit") {
		t.Errorf("test-support package was layered: %v", p)
	}
}

// writeModule writes a throwaway module under t.TempDir() and returns its root.
func writeModule(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// TestLoadToleratesOnlyListedEmbedOutputs is 03 §3.5.1 item 4's fixture pair:
// a tree with no built frontend passes when the output is listed, and any
// other package error fails.
func TestLoadToleratesOnlyListedEmbedOutputs(t *testing.T) {
	root := writeModule(t, map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.27.0\n",
		"ui/ui.go": "package ui\n\nimport \"embed\"\n\n//go:embed all:web/dist\nvar dist embed.FS\n\n" +
			"func FS() embed.FS { return dist }\n",
		"user/user.go": "package user\n\nimport _ \"example.com/m/ui\"\n",
	})
	if _, err := Load(LoadOptions{Dir: root, EmbedOutputs: []string{"ui/web/dist"}}); err != nil {
		t.Fatalf("listed unbuilt embed output not tolerated: %v", err)
	}
	if _, err := Load(LoadOptions{Dir: root}); err == nil || !strings.Contains(err.Error(), "no matching files found") {
		t.Fatalf("unlisted missing embed output not failed: %v", err)
	}

	broken := writeModule(t, map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.27.0\n",
		"a/a.go": "package a\n\nimport _ \"example.com/m/nosuch\"\n",
	})
	if _, err := Load(LoadOptions{Dir: broken, EmbedOutputs: []string{"ui/web/dist"}}); err == nil {
		t.Fatal("a package error other than a listed embed output was tolerated")
	}

	tagged := writeModule(t, map[string]string{
		"go.mod":     "module example.com/m\n\ngo 1.27.0\n",
		"a/a.go":     "package a\n",
		"a/a_int.go": "//go:build integration\n\npackage a\n\nimport _ \"example.com/m/b\"\n",
		"b/b.go":     "package b\n",
	})
	pkgs, err := Load(LoadOptions{Dir: tagged, TagSets: TagSets()})
	if err != nil {
		t.Fatal(err)
	}
	if a := pkgs["example.com/m/a"]; a == nil || !containsString(a.Imports, "example.com/m/b") || len(a.GoFiles) != 2 {
		t.Fatalf("the tag union missed the integration-tagged file: %+v", a)
	}
}

// TestNoInitFindings: NoInit fails an init function and a package-level
// variable, and passes a clean package with a sentinel error and an assertion.
func TestNoInitFindings(t *testing.T) {
	root := writeModule(t, map[string]string{
		"go.mod":          "module example.com/m\n\ngo 1.27.0\n",
		"dirty/d.go":      "package dirty\n\nvar registry = map[string]int{}\n\nfunc init() { registry[\"x\"] = 1 }\n",
		"clean/c.go":      "package clean\n\nimport (\n\t\"errors\"\n\t\"io\"\n)\n\nvar ErrStopped = errors.New(\"stopped\")\n\nvar _ io.Reader = (*R)(nil)\n\ntype R struct{}\n\nfunc (*R) Read([]byte) (int, error) { return 0, io.EOF }\n\nconst Name = \"clean\"\n",
		"clean/c_test.go": "package clean\n\nvar testOnly = 1\n\nfunc init() { _ = testOnly }\n",
	})
	dirty, err := NoInitFindings([]string{filepath.Join(root, "dirty", "d.go")})
	if err != nil {
		t.Fatal(err)
	}
	if len(dirty) != 2 || !hasProblem(dirty, "func init()") || !hasProblem(dirty, "package-level variable registry") {
		t.Fatalf("dirty package findings = %v, want the init and the variable", dirty)
	}
	clean, err := NoInitFindings([]string{filepath.Join(root, "clean", "c.go")})
	if err != nil {
		t.Fatal(err)
	}
	if len(clean) != 0 {
		t.Fatalf("clean package findings = %v, want none", clean)
	}
	// Through the loader: test files are not scanned, so the clean package passes.
	NoInit(t, root, "./clean/...")
}
