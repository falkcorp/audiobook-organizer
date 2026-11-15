// file: core/archtest/f803b_probe_test.go
// version: 1.0.0
// guid: 0e5a8c31-7d92-4b64-a1f7-3c6e9d2b5a08
// last-edited: 2026-11-15

package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// probeFindings writes src as diagnostics' export.go in a temp dir and runs the
// probe pin over it.
func probeFindings(t *testing.T, src string) []string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "export.go")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := f803bProbe([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// mutate replaces old with repl once in gateExportGo and fails if old is absent,
// so a mutation can never silently be a no-op.
func mutate(t *testing.T, old, repl string) string {
	t.Helper()
	if !strings.Contains(gateExportGo, old) {
		t.Fatalf("the fixture has no %q to mutate", old)
	}
	return strings.Replace(gateExportGo, old, repl, 1)
}

// TestF803bProbe (round 10, N-3): the probe pin passes the shape of the real
// refusal and reports each way of making the probe say "test binary" in a
// production binary. These are the mutants that survived every in-process test of
// round 9 (New hardwired to "test binary", testBinary's fallback returning true),
// plus the ways around them.
func TestF803bProbe(t *testing.T) {
	if got := probeFindings(t, gateExportGo); len(got) != 0 {
		t.Fatalf("the unmutated fixture was reported:\n  %s", strings.Join(got, "\n  "))
	}

	for name, c := range map[string]struct{ old, repl string }{
		"New hardwired to a test binary (function literal)": {
			"newExporter(nil, testing.Testing)", "newExporter(nil, func() bool { return true })"},
		"New passes another function": {
			"newExporter(nil, testing.Testing)", "newExporter(nil, alwaysTrue)"},
		"New passes nothing": {
			"newExporter(nil, testing.Testing)", "newExporter(nil)"},
		"testBinary fallback returns true": {
			"return testing.Testing()\n}", "return true\n}"},
		"testBinary fallback negated": {
			"return testing.Testing()\n}", "return !testing.Testing()\n}"},
		"testBinary has only the field": {
			"\treturn testing.Testing()\n}", "\treturn e.isTestBinary()\n}"},
		"testBinary returns a variable": {
			"return e.isTestBinary()", "return ok"},
		"testBinary holds a function literal": {
			"return e.isTestBinary()", "return func() bool { return true }()"},
		"Build passes a function literal": {
			"redesignRefusal(RedesignPendingF8_03b, e.testBinary)", "redesignRefusal(RedesignPendingF8_03b, func() bool { return true })"},
		"Build passes true": {
			"redesignRefusal(RedesignPendingF8_03b, e.testBinary)", "redesignRefusal(RedesignPendingF8_03b, true)"},
		"the refusal is given false in place of the marker": {
			"redesignRefusal(RedesignPendingF8_03b, isTestBinary)", "redesignRefusal(false, isTestBinary)"},
		"the field is assigned": {
			"return &Exporter{isTestBinary: isTestBinary}, nil", "e := &Exporter{isTestBinary: isTestBinary}\n\te.isTestBinary = func() bool { return true }\n\treturn e, nil"},
		"the field is set to a literal": {
			"&Exporter{isTestBinary: isTestBinary}", "&Exporter{isTestBinary: func() bool { return true }}"},
		"newExporter taken as a function value": {
			"func New() (*Exporter, error) { return newExporter(nil, testing.Testing) }", "var build = newExporter\n\nfunc New() (*Exporter, error) { return build(nil, testing.Testing) }\n\nfunc init() { _, _ = newExporter(nil, testing.Testing) }"},
		"testBinary taken as a method value in a variable": {
			"func (e *Exporter) Build() error {", "func (e *Exporter) Build() error {\n\tf := e.testBinary\n\t_ = f\n\treturn nil\n}\n\nfunc (e *Exporter) Build2() error {"},
		"testing.Testing from a package that is not testing": {
			"import (\n\t\"errors\"\n\t\"testing\"\n)", "import (\n\t\"errors\"\n\t\"testing\"\n\n\ttesting2 \"example.com/fake\"\n)"},
	} {
		src := mutate(t, c.old, c.repl)
		if name == "testing.Testing from a package that is not testing" {
			// New passes the other package's Testing: syntactically the same selector,
			// bound to a different import.
			src = strings.Replace(src, "newExporter(nil, testing.Testing)", "newExporter(nil, testing2.Testing)", 1)
		}
		got := probeFindings(t, src)
		if len(got) == 0 {
			t.Errorf("%s: not reported", name)
			continue
		}
		for _, g := range got {
			if !strings.Contains(g, "testing.Testing and nothing else") && !strings.Contains(g, "the probe pin found") {
				t.Errorf("%s: a finding does not carry the probe note: %s", name, g)
			}
		}
	}
}

// TestF803bProbeAllowedShapes: the aliased import of testing, and the probe
// handed to redesignRefusal as testing.Testing, are the same probe.
func TestF803bProbeAllowedShapes(t *testing.T) {
	aliased := strings.NewReplacer(
		"\t\"testing\"\n", "\ttt \"testing\"\n",
		"testing.Testing", "tt.Testing",
	).Replace(gateExportGo)
	if got := probeFindings(t, aliased); len(got) != 0 {
		t.Errorf("an aliased import of testing was reported:\n  %s", strings.Join(got, "\n  "))
	}
	direct := mutate(t, "redesignRefusal(RedesignPendingF8_03b, e.testBinary)", "redesignRefusal(RedesignPendingF8_03b, testing.Testing)")
	if got := probeFindings(t, direct); len(got) != 0 {
		t.Errorf("redesignRefusal given testing.Testing was reported:\n  %s", strings.Join(got, "\n  "))
	}
}

// TestF803bProbeIsNotVacuous: a rename or removal of the pinned symbols is
// reported, not passed.
func TestF803bProbeIsNotVacuous(t *testing.T) {
	for name, src := range map[string]string{
		"newExporter renamed":     strings.NewReplacer("newExporter", "newExp").Replace(gateExportGo),
		"testBinary renamed":      strings.NewReplacer("testBinary", "isProd").Replace(gateExportGo),
		"redesignRefusal renamed": strings.NewReplacer("redesignRefusal", "refuse").Replace(gateExportGo),
		"nothing at all":          "package diagnostics\n",
	} {
		got := probeFindings(t, src)
		found := false
		for _, g := range got {
			if strings.Contains(g, "the probe pin found") {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: no vacuity finding; got %v", name, got)
		}
	}
}

// TestF803bGateReadsTheProbeInEveryFileOfDiagnostics: a bad probe in a file no
// tag set builds is reported, and a test file's fake probe (the in-process tests
// pass one) is not.
func TestF803bGateReadsTheProbeInEveryFileOfDiagnostics(t *testing.T) {
	bad := strings.Replace(gateExportGo, "package diagnostics", "//go:build neverbuilt\n\npackage diagnostics", 1)
	bad = strings.Replace(bad, "type Exporter struct{ isTestBinary func() bool }", "", 1)
	bad = strings.Replace(bad, "func New()", "func newBad()", 1)
	bad = strings.Replace(bad, "newExporter(nil, testing.Testing)", "newExporter(nil, func() bool { return true })", 1)
	// The ignored file redeclares the pinned functions; only its text is read.
	root := gateTree(t, "true", map[string]string{"health/diagnostics/never.go": bad})
	got, err := F803bGateFindings(root)
	if err != nil {
		t.Fatal(err)
	}
	var hits int
	for _, g := range got {
		if strings.Contains(g, "never.go") && strings.HasSuffix(g, f803bProbeNote) {
			hits++
		}
	}
	if hits == 0 {
		t.Errorf("a bad probe in a file no tag set builds was not reported:\n  %s", strings.Join(got, "\n  "))
	}

	test := `package diagnostics

import "testing"

func TestRefusal(t *testing.T) {
	_, _ = newExporter(nil, func() bool { return false })
	_ = redesignRefusal(RedesignPendingF8_03b, func() bool { return false })
}
`
	got, err = F803bGateFindings(gateTree(t, "true", map[string]string{"health/diagnostics/export_test.go": test}))
	if err != nil || len(got) != 0 {
		t.Errorf("a test file's fake probe: %v, %v; want no findings", got, err)
	}
}

// TestF803bGateFlagsABadProbeInTheRealShape runs the whole gate over a fixture
// whose New is hardwired: the gate (not only the pin) reports it, with a file and
// a line.
func TestF803bGateFlagsABadProbeInTheRealShape(t *testing.T) {
	root := gateTree(t, "true", map[string]string{
		"health/diagnostics/export.go": mutate(t, "newExporter(nil, testing.Testing)", "newExporter(nil, func() bool { return true })"),
	})
	got, err := F803bGateFindings(root)
	if err != nil {
		t.Fatal(err)
	}
	if n := findingsByFile(root, got)["health/diagnostics/export.go"]; n != 1 || len(got) != 1 {
		t.Fatalf("want one report in export.go, got:\n  %s", strings.Join(got, "\n  "))
	}
}

// TestF803bLinkerFindings (round 10, N-3): a build path that names the linker
// symbol that makes testing.Testing true is reported, with its line; prose,
// node_modules and this guard's own files are not.
func TestF803bLinkerFindings(t *testing.T) {
	flag := f803bTestBinaryFlag
	root := writeModule(t, map[string]string{
		"go.mod":                       "module example.com/m\n\ngo 1.27.0\n",
		"Makefile":                     "build:\n\tgo build -ldflags \"-X " + flag + "=1\" ./...\n",
		"Dockerfile.build":             "RUN go build -ldflags=-X=" + flag + "=1 .\n",
		"scripts/build.sh":             "#!/bin/sh\ngo build -ldflags '-X " + flag + "=1'\n",
		".github/workflows/ci.yml":     "run: go test -ldflags \"-X " + flag + "=1\"\n",
		"tools/build.py":               "FLAGS = '-X " + flag + "=1'\n",
		"cmd/run.go":                   "package main\n\nvar f = \"-X " + flag + "=1\"\n",
		"docs/notes.md":                "the flag " + flag + " is described here\n",
		"web/node_modules/p/x.json":    "{\"f\": \"" + flag + "\"}\n",
		"core/archtest/f803b_probe.go": "package archtest\n\nconst x = \"" + flag + "\"\n",
		"clean/Makefile":               "build:\n\tgo build ./...\n",
	})
	got, scanned, err := f803bLinkerFindings(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		".github/workflows/ci.yml:1",
		"Dockerfile.build:1",
		"Makefile:2",
		"cmd/run.go:3",
		"scripts/build.sh:2",
		"tools/build.py:1",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("findings = %v, want %v", got, want)
	}
	// Makefile, Dockerfile.build, build.sh, ci.yml, build.py, run.go and
	// clean/Makefile; not the markdown, node_modules or the guard's own file.
	if scanned != 7 {
		t.Errorf("scanned %d files, want 7", scanned)
	}
}

// TestF803bRepoRoot: a core module under a repo is scanned from the repo; a core
// module on its own (a split repo) is scanned from itself.
func TestF803bRepoRoot(t *testing.T) {
	under := writeModule(t, map[string]string{"go.mod": "module r\n", "core/go.mod": "module c\n"})
	if got := f803bRepoRoot(filepath.Join(under, "core")); got != under {
		t.Errorf("repo root = %s, want %s", got, under)
	}
	alone := writeModule(t, map[string]string{"core/go.mod": "module c\n"})
	if got, want := f803bRepoRoot(filepath.Join(alone, "core")), filepath.Join(alone, "core"); got != want {
		t.Errorf("repo root = %s, want %s", got, want)
	}
	flat := writeModule(t, map[string]string{"go.mod": "module c\n"})
	if got := f803bRepoRoot(flat); got != flat {
		t.Errorf("repo root = %s, want %s", got, flat)
	}
}

// TestF803bNoLinkerOverride runs the guard over this repo: no build path passes
// the linker flag that would turn the F8-03b refusal off.
func TestF803bNoLinkerOverride(t *testing.T) {
	F803bNoLinkerOverride(t)
}
