// file: core/archtest/f803b_probe_test.go
// version: 1.3.0
// guid: 0e5a8c31-7d92-4b64-a1f7-3c6e9d2b5a08
// last-edited: 2026-11-15

package archtest

import (
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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

// TestF803bLinkerFindings (round 11, O-2): the guard reads text files by content,
// so a script without an extension, a PowerShell file, a .conf and Makefile.local
// are read; documentation, git, node_modules, a binary (NUL byte), a file over
// 4 MiB and the guard's own files are not.
func TestF803bLinkerFindings(t *testing.T) {
	flag := f803bTestBinaryFlag
	root := writeModule(t, map[string]string{
		"go.mod":                       "module example.com/m\n\ngo 1.27.0\n",
		"Makefile":                     "build:\n\tgo build -ldflags \"-X " + flag + "=1\" ./...\n",
		"Makefile.local":               "LDFLAGS += -X " + flag + "=1\n",
		"Dockerfile.build":             "RUN go build -ldflags=-X=" + flag + "=1 .\n",
		"scripts/build.sh":             "#!/bin/sh\ngo build -ldflags '-X " + flag + "=1'\n",
		"deploy/rollback-helpers/rb":   "#!/bin/sh\ngo build -ldflags '-X " + flag + "=1'\n",
		"deploy/build.ps1":             "go build -ldflags \"-X " + flag + "=1\"\n",
		"deploy/build.conf":            "GOFLAGS=-ldflags=-X=" + flag + "=1\n",
		".github/workflows/ci.yml":     "env:\n  GOFLAGS: -ldflags=-X=" + flag + "=1\n",
		"tools/build.py":               "FLAGS = '-X " + flag + "=1'\n",
		"cmd/run.go":                   "package main\n\nvar f = \"-X " + flag + "=1\"\n",
		"docs/notes.md":                "the flag " + flag + " is described here\n",
		"docs/notes.txt":               "the flag " + flag + " is described here\n",
		"web/node_modules/p/x.json":    "{\"f\": \"" + flag + "\"}\n",
		"bin/tool":                     "\x00\x01 " + flag + "\n",
		"big/generated.sql":            strings.Repeat("-- padding\n", 400000) + flag + "\n",
		"core/archtest/f803b_probe.go": "package archtest\n\nconst x = \"" + flag + "\"\n",
		"clean/Makefile":               "build:\n\tgo build ./...\n",
	})
	got, scanned, err := f803bLinkerFindings(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		".github/workflows/ci.yml:2",
		"Dockerfile.build:1",
		"Makefile.local:1",
		"Makefile:2",
		"cmd/run.go:3",
		"deploy/build.conf:1",
		"deploy/build.ps1:1",
		"deploy/rollback-helpers/rb:2",
		"scripts/build.sh:2",
		"tools/build.py:1",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("findings = %v, want %v", got, want)
	}
	// go.mod, the 10 files above, clean/Makefile: 12; not the markdown, text,
	// node_modules, binary, oversized file or the guard's own file.
	if scanned != 12 {
		t.Errorf("scanned %d files, want 12", scanned)
	}
}

// TestF803bTagsIn: the -tags arguments of build-file lines, in the spellings the
// repo uses, and not the look-alikes (git's --tags, --no-tags, a name ending in
// -tags, a comment). A reference that cannot be resolved is reported, never
// dropped (round 12, P-1).
func TestF803bTagsIn(t *testing.T) {
	vars := map[string]string{"GO_BUILD_TAGS": "pprof", "MORE": "fts5 $(GO_BUILD_TAGS)", "LOOP": "$(LOOP)"}
	for name, tc := range map[string]struct {
		body  string
		vars  map[string]string
		want  string // lists, joined
		unres string // substrings of the unresolved reports, joined with |
	}{
		"quoted list":       {body: `go build -tags "embed_frontend fts5 native_taglib" \` + "\n", want: "embed_frontend,fts5,native_taglib"},
		"bare word":         {body: "go test -tags pprof -run Pprof ./...\n", want: "pprof"},
		"equals":            {body: "go test -tags=integration ./...\n", want: "integration"},
		"comma list":        {body: "go vet -tags integration,bench ./...\n", want: "bench,integration"},
		"double dash":       {body: "go build --tags unitlifecycle .\n", want: "unitlifecycle"},
		"in parentheses":    {body: "echo \"(go vet/test -tags pprof)\"\n", want: "pprof"},
		"git describe":      {body: "VERSION := $(shell git describe --tags --always --dirty)\n"},
		"git describe abbr": {body: "git describe --tags --abbrev=0\n"},
		"no-tags":           {body: "git fetch --no-tags origin main\n"},
		"name ending":       {body: "group: cleanup-orphan-rc-tags\n"},
		"comment line":      {body: "# go vet -tags $(NOT_DEFINED)\n"},
		"two on a line":     {body: "go vet -tags pprof . && go test -tags native_taglib .\n", want: "pprof|native_taglib"},
		// Make variables (P-1).
		"variable in a list": {body: "\t@go build -tags \"embed_frontend $(GO_BUILD_TAGS)\" -o x .\n", vars: vars, want: "embed_frontend,pprof"},
		"only a variable":    {body: "\t@go build -tags \"$(GO_BUILD_TAGS)\" -o x .\n", vars: vars, want: "pprof"},
		"braces":             {body: "\t@go build -tags \"native_taglib ${GO_BUILD_TAGS}\" .\n", vars: vars, want: "native_taglib,pprof"},
		"unquoted variable":  {body: "\tgo build -tags $(GO_BUILD_TAGS) .\n", vars: vars, want: "pprof"},
		"nested variable":    {body: "\tgo build -tags \"$(MORE) bench\" .\n", vars: vars, want: "bench,fts5,pprof"},
		// Unresolved: reported with the line, not dropped.
		"undefined variable":  {body: "\tgo build -tags \"embed_frontend $(NOT_DEFINED)\" .\n", vars: vars, unres: "line 1|NOT_DEFINED"},
		"no variables at all": {body: "\tgo build -tags \"embed_frontend $(GO_BUILD_TAGS)\" .\n", unres: "GO_BUILD_TAGS"},
		"self reference":      {body: "\tgo build -tags \"$(LOOP)\" .\n", vars: vars, unres: "LOOP"},
		"make function":       {body: "\tgo build -tags \"$(shell cat tags.txt)\" .\n", vars: vars, unres: "shell cat tags.txt"},
		"shell variable":      {body: "go build -tags \"$TAGS\" .\n", unres: "\"$TAGS\" is not a build tag"},
		"shell braces":        {body: "go build -tags \"embed_frontend ${EXTRA_TAGS}\" .\n", unres: "EXTRA_TAGS"},
		"workflow expression": {body: "run: go test -tags \"${{ matrix.tags }}\" ./...\n", unres: "line 1"},
		"backticks":           {body: "go build -tags \"`cat tags`\" .\n", unres: "not a build tag"},
	} {
		lists, unres := f803bTagsIn(tc.body, tc.vars)
		var got []string
		for _, tags := range lists {
			got = append(got, strings.Join(tags, ","))
		}
		if strings.Join(got, "|") != tc.want {
			t.Errorf("%s: tag lists %q, want %q", name, strings.Join(got, "|"), tc.want)
		}
		if tc.unres == "" && len(unres) != 0 {
			t.Errorf("%s: unexpected unresolved reports %q", name, unres)
		}
		if tc.unres != "" {
			if len(unres) != 1 {
				t.Errorf("%s: %d unresolved reports %q, want 1", name, len(unres), unres)
				continue
			}
			for sub := range strings.SplitSeq(tc.unres, "|") {
				if !strings.Contains(unres[0], sub) {
					t.Errorf("%s: unresolved report %q lacks %q", name, unres[0], sub)
				}
			}
			if len(lists) != 0 {
				t.Errorf("%s: an unresolved argument also produced the lists %q", name, lists)
			}
		}
	}
}

// TestF803bMakeVars: the assignment forms and the one rule that matters for a
// default: `?=` keeps an earlier value, `+=` appends, a recipe line is not an
// assignment, a comment is stripped.
func TestF803bMakeVars(t *testing.T) {
	body := strings.Join([]string{
		"A := one",
		"B = two  # trailing comment",
		"C ::= three",
		"D ?= first",
		"D ?= second",
		"E := a",
		"E += b",
		"export F := exported",
		"override G = over",
		"H ?= default",
		"\tI := recipe line",
		"# J := comment",
		"K=nospace",
		"L :=",
		"url := http://x/#frag",
	}, "\n")
	got := f803bMakeVars(body)
	want := map[string]string{"A": "one", "B": "two", "C": "three", "D": "first", "E": "a b", "F": "exported", "G": "over", "H": "default", "K": "nospace", "L": "", "url": "http://x/#frag"}
	if len(got) != len(want) {
		t.Errorf("variables = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

// TestF803bShippedTagSets (round 11, O-1 c; round 12, P-1): over this repo, the
// sets the real-binary test builds include the ones the build files name,
// make variables resolved, and the deployed set among them. If the reading of
// Makefiles, Dockerfiles and workflows stopped matching, the real-binary test
// would build the TagSets() list alone; this fails first.
func TestF803bShippedTagSets(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root, err := ModuleRoot(wd)
	if err != nil {
		t.Fatal(err)
	}
	sets, named, err := F803bShippedTagSets(root)
	if err != nil {
		t.Fatal(err)
	}
	if named == 0 {
		t.Fatal("no -tags list read from the repo's build files")
	}
	has := func(tags ...string) bool {
		return slices.ContainsFunc(sets, func(ts TagSet) bool {
			a, b := slices.Clone(ts.Tags), slices.Clone(tags)
			slices.Sort(a)
			slices.Sort(b)
			return ts.GOOS == "" && slices.Equal(a, b)
		})
	}
	for _, want := range [][]string{
		nil, // the default build
		{"embed_frontend"},
		{"embed_frontend", "fts5", "native_taglib"}, // Dockerfile, Makefile
		{"native_taglib"},
		{"pprof"},
		{"integration"},
		{"bench"},
		{"unitlifecycle"},
		// Through $(GO_BUILD_TAGS) in the Makefile: `make build-linux`, which
		// `make deploy` runs, builds the production binary with this set.
		{"embed_frontend", "fts5", "native_taglib", "pprof"},
		{"embed_frontend", "pprof"},
		{"bench", "pprof"},
	} {
		if !has(want...) {
			t.Errorf("no host tag set %v among %d sets", want, len(sets))
		}
	}

	// Variables resolve, and an argument that cannot be resolved fails the
	// derivation by name instead of dropping out. Round 12, P-1.
	fixture := func(files map[string]string) (string, string) {
		base := map[string]string{"go.mod": "module r\n", "core/go.mod": "module c\n"}
		maps.Copy(base, files)
		dir := writeModule(t, base)
		return dir, filepath.Join(dir, "core")
	}
	_, core := fixture(map[string]string{
		"Makefile":                "TAGS := pprof\n-include Makefile.local\nMORE = $(TAGS) fts5\nDEFAULT ?= shipit\nDEFAULT ?= ignored\nx:\n\tgo build -tags \"embed_frontend $(MORE) $(DEFAULT)\" .\n",
		".github/workflows/w.yml": "run: go build -tags embed_frontend,fts5 .\n",
		"scripts/other.sh":        "go build -tags notscanned .\n",
		"Makefile.local":          "TAGS := local\nDEFAULT ?= robin\n",
	})
	fsets, fnamed, err := F803bShippedTagSets(core)
	if err != nil {
		t.Fatal(err)
	}
	if fnamed != 2 {
		t.Errorf("the fixture's build files named %d lists, want 2 (a script outside the build files is not read)", fnamed)
	}
	var extra []string
	for _, ts := range fsets {
		if strings.HasPrefix(ts.Name, "build files: ") {
			extra = append(extra, strings.Join(ts.Tags, ","))
		}
	}
	slices.Sort(extra)
	// Final review round, fix 2. make reads the Makefile top to bottom and an
	// existing -include'd file at its include line (f803bMakeVarsInto): Makefile.local's
	// TAGS := local comes after TAGS := pprof, so it wins; its DEFAULT ?= robin
	// runs before the Makefile's own DEFAULT ?= shipit, which therefore yields.
	// (Before this round the fixture pinned the reverse: Makefile.local ignored.)
	if want := "embed_frontend,fts5|embed_frontend,fts5,local,robin"; strings.Join(extra, "|") != want {
		t.Errorf("sets added from build files = %v, want %s", extra, want)
	}
	// An assignment after the include line overrides the included file's, and an
	// include of a file that does not exist changes nothing.
	for name, files := range map[string]map[string]string{
		"assignment after the include wins": {
			"Makefile":       "-include Makefile.local\nTAGS := pprof\nx:\n\tgo build -tags \"$(TAGS)\" .\n",
			"Makefile.local": "TAGS := local\n",
		},
		"include of a missing file": {
			"Makefile": "TAGS := pprof\n-include Makefile.local\nx:\n\tgo build -tags \"$(TAGS)\" .\n",
		},
		"nested include is read in place": {
			"Makefile":       "TAGS := local\n-include Makefile.local\nx:\n\tgo build -tags \"$(TAGS)\" .\n",
			"Makefile.local": "include more.mk\nTAGS += pprof\n",
			"more.mk":        "TAGS := ignored\n",
		},
	} {
		_, c := fixture(files)
		sets, _, err := F803bShippedTagSets(c)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var got []string
		for _, ts := range sets {
			if strings.HasPrefix(ts.Name, "build files: ") {
				got = append(got, strings.Join(ts.Tags, ","))
			}
		}
		// pprof is also a TagSets() entry, so only the nested case adds a set.
		want := map[string]string{"assignment after the include wins": "", "include of a missing file": "", "nested include is read in place": "ignored,pprof"}[name]
		if strings.Join(got, "|") != want {
			t.Errorf("%s: sets added = %v, want %q", name, got, want)
		}
	}
	// An include whose name cannot be expanded fails the derivation.
	_, c := fixture(map[string]string{"Makefile": "-include $(NOT_DEFINED_ANYWHERE)\nx:\n\tgo build -tags pprof .\n"})
	if _, _, err := F803bShippedTagSets(c); err == nil || !strings.Contains(err.Error(), "include") {
		t.Errorf("unresolvable include: err = %v, want a failure naming the include", err)
	}

	for name, files := range map[string]map[string]string{
		"undefined variable":  {"Makefile": "x:\n\tgo build -tags \"embed_frontend $(NOT_DEFINED_ANYWHERE)\" .\n"},
		"shell variable":      {"Makefile": "x:\n\tgo build -tags \"$$TAGS\" .\n"},
		"workflow expression": {".github/workflows/w.yml": "run: go test -tags \"${{ matrix.tags }}\" ./...\n"},
		"Dockerfile ARG":      {"Dockerfile": "ARG TAGS=embed_frontend\nRUN go build -tags \"$TAGS\" .\n"},
		"make function":       {"Makefile": "x:\n\tgo build -tags \"$(shell cat tags.txt)\" .\n"},
	} {
		_, core := fixture(files)
		_, _, err := F803bShippedTagSets(core)
		if err == nil || !strings.Contains(err.Error(), "cannot be reduced to a concrete tag set") || !strings.Contains(err.Error(), "-tags") {
			t.Errorf("%s: err = %v, want a failure naming the argument", name, err)
		}
	}
}

// TestF803bSuffixConstraint: the go command's file-name rule, and its known OS
// and architecture lists against this toolchain's `go tool dist list`.
func TestF803bSuffixConstraint(t *testing.T) {
	for name, want := range map[string]string{
		"build.go":             "",
		"windows.go":           "",
		"linux_notes.go":       "",
		"build_notes.go":       "",
		"build_test.go":        "",
		"build_linux.go":       "file name suffix _linux",
		"build_amd64.go":       "file name suffix _amd64",
		"build_linux_arm64.go": "file name suffix _linux_arm64",
		"build_linux_test.go":  "file name suffix _linux",
		"_linux.go":            "file name suffix _linux",
		"build_arm64_linux.go": "file name suffix _linux",
		"build_unix.go":        "",
		"build_wasip1_wasm.go": "file name suffix _wasip1_wasm",
		"build_mips64p32le.go": "file name suffix _mips64p32le",
	} {
		if got := f803bSuffixConstraint(name); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("no go command: %v", err)
	}
	out, err := exec.Command(goBin, "tool", "dist", "list").Output()
	if err != nil {
		t.Fatalf("go tool dist list: %v", err)
	}
	for pair := range strings.FieldsSeq(string(out)) {
		goos, goarch, _ := strings.Cut(pair, "/")
		if !slices.Contains(f803bKnownOS(), goos) {
			t.Errorf("GOOS %q (go tool dist list) is not in f803bKnownOS(): a file named x_%s.go would pass the constraint rule", goos, goos)
		}
		if !slices.Contains(f803bKnownArch(), goarch) {
			t.Errorf("GOARCH %q (go tool dist list) is not in f803bKnownArch(): a file named x_%s.go would pass the constraint rule", goarch, goarch)
		}
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
