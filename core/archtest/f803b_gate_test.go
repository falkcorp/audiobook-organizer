// file: core/archtest/f803b_gate_test.go
// version: 1.3.0
// guid: 91c3e5a7-0d48-4f26-8b1e-6a7d2f4c9e03
// last-edited: 2026-11-15

package archtest

import (
	"maps"
	"os"
	"strings"
	"testing"
)

// gateExportGo is the fixture diagnostics' export.go: an Exporter, New, and the
// run-time refusal with the probe in the shape the probe pin reads.
const gateExportGo = `package diagnostics

import (
	"errors"
	"testing"
)

type Exporter struct{ isTestBinary func() bool }

func New() (*Exporter, error) { return newExporter(nil, testing.Testing) }

func newExporter(cfg any, isTestBinary func() bool) (*Exporter, error) {
	if err := redesignRefusal(RedesignPendingF8_03b, isTestBinary); err != nil {
		return nil, err
	}
	return &Exporter{isTestBinary: isTestBinary}, nil
}

func (e *Exporter) testBinary() bool {
	if e.isTestBinary != nil {
		return e.isTestBinary()
	}
	return testing.Testing()
}

func (e *Exporter) Build() error { return redesignRefusal(RedesignPendingF8_03b, e.testBinary) }

func redesignRefusal(pending bool, isTestBinary func() bool) error {
	if pending && !isTestBinary() {
		return errors.New("refused")
	}
	return nil
}
`

// gateTree is a fixture core module: diagnostics with the marker, a New, an
// Exporter and the run-time refusal with its probe (the shape the probe pin
// reads), healthhttp built on it (a type reference, no construction), a kernel
// that imports neither in non-test code, and a test file that imports healthhttp
// and calls New. pending is the marker's value; extra adds or replaces files.
func gateTree(t *testing.T, pending string, extra map[string]string) string {
	t.Helper()
	files := map[string]string{
		"go.mod": "module github.com/falkcorp/mediacore\n\ngo 1.27.0\n",
		"health/diagnostics/paths.go": `package diagnostics

const RedesignPendingF8_03b = ` + pending + `
`,
		"health/diagnostics/export.go": gateExportGo,
		"internal/healthhttp/routes.go": `package healthhttp

import "github.com/falkcorp/mediacore/health/diagnostics"

type api struct{ e *diagnostics.Exporter }

func Register(e *diagnostics.Exporter) *api { return &api{e: e} }
`,
		"kernel/kernel.go": "package kernel\n\nfunc Boot() {}\n",
		"kernel/kernel_test.go": `package kernel

import (
	"testing"

	"github.com/falkcorp/mediacore/health/diagnostics"
	"github.com/falkcorp/mediacore/internal/healthhttp"
)

func TestBoot(t *testing.T) {
	e, _ := diagnostics.New()
	_ = healthhttp.Register(e)
	_ = &diagnostics.Exporter{}
}
`,
	}
	maps.Copy(files, extra)
	return writeModule(t, files)
}

// TestF803bGate (F8-03 round 8): while the marker is true, a non-test file that
// imports healthhttp or constructs the Exporter in any spelling is reported, with
// the message that says the mount is blocked; tests, diagnostics itself and
// healthhttp's own use of the Exporter type are not; the guard refuses a tree
// whose marker is false (round 10: false is not a valid state) and a tree it
// cannot read the marker from.
func TestF803bGate(t *testing.T) {
	findingsFor := func(t *testing.T, root string) []string {
		t.Helper()
		got, err := F803bGateFindings(root)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	t.Run("AllowedTree", func(t *testing.T) {
		// healthhttp refers to diagnostics.Exporter as a type; the kernel's test
		// imports healthhttp and calls New. Neither is a breach.
		if got := findingsFor(t, gateTree(t, "true", nil)); len(got) != 0 {
			t.Fatalf("the allowed tree reported:\n  %s", strings.Join(got, "\n  "))
		}
	})

	t.Run("NonTestImportOfHealthhttp", func(t *testing.T) {
		root := gateTree(t, "true", map[string]string{
			"kernel/kernel.go": `package kernel

import "github.com/falkcorp/mediacore/internal/healthhttp"

func Boot() { _ = healthhttp.Register(nil) }
`,
		})
		got := findingsFor(t, root)
		if n := findingsByFile(root, got)["kernel/kernel.go"]; n != 1 || len(got) != 1 {
			t.Fatalf("want one report in kernel.go, got:\n  %s", strings.Join(got, "\n  "))
		}
		if !strings.HasSuffix(got[0], "mount is blocked until F8-03b replaces the path redactor; see seams.d/F8-03.md") {
			t.Errorf("the finding does not end with the blocked message: %s", got[0])
		}
	})

	t.Run("EveryConstructionShape", func(t *testing.T) {
		root := gateTree(t, "true", map[string]string{
			"kernel/kernel.go": `package kernel

import "github.com/falkcorp/mediacore/health/diagnostics"

var build = diagnostics.New

func Boot() { _, _ = diagnostics.New() }
`,
			"tasks/tasks.go": `package tasks

import gordon "github.com/falkcorp/mediacore/health/diagnostics"

func Run() any { return &gordon.Exporter{} }
`,
			"outbox/outbox.go": `package outbox

import "github.com/falkcorp/mediacore/health/diagnostics"

func Drain() any { return new(diagnostics.Exporter) }
`,
			"events/events.go": `package events

import . "github.com/falkcorp/mediacore/health/diagnostics"

func Emit() { _, _ = New(); _ = Exporter{} }
`,
			// a blank import, and a type reference, construct nothing
			"library/library.go": `package library

import (
	_ "github.com/falkcorp/mediacore/health/diagnostics"
)
`,
			"library/typed.go": `package library

import "github.com/falkcorp/mediacore/health/diagnostics"

func Use(e *diagnostics.Exporter) *diagnostics.Exporter { return e }
`,
			// a build-tagged file is read under the tag-set union
			"books/books.go": "package books\n",
			"books/books_integration.go": `//go:build integration

package books

import "github.com/falkcorp/mediacore/internal/healthhttp"

func Mount() { _ = healthhttp.Register(nil) }
`,
		})
		got := findingsFor(t, root)
		byFile := findingsByFile(root, got)
		for file, want := range map[string]int{
			"kernel/kernel.go":           2,
			"tasks/tasks.go":             1,
			"outbox/outbox.go":           1,
			"events/events.go":           2,
			"books/books_integration.go": 1,
		} {
			if byFile[file] != want {
				t.Errorf("%s: %d reports, want %d", file, byFile[file], want)
			}
		}
		if byFile["library/library.go"] != 0 || byFile["library/typed.go"] != 0 {
			t.Errorf("a blank import or a type reference was reported:\n  %s", strings.Join(got, "\n  "))
		}
		if len(got) != 7 {
			t.Errorf("%d reports, want 7:\n  %s", len(got), strings.Join(got, "\n  "))
		}
		for _, g := range got {
			if !strings.HasSuffix(g, f803bBlocked) {
				t.Errorf("a finding does not end with the blocked message: %s", g)
			}
		}
	})

	// Round 10, N-1: false is not a valid state. The marker has two states, true
	// and deleted (F8-03b deletes it together with the refusal and this guard),
	// so a false marker is refused whatever else the tree holds.
	t.Run("MarkerFalseIsRefused", func(t *testing.T) {
		for name, extra := range map[string]map[string]string{
			"clean tree": nil,
			"with breaches": {
				"kernel/kernel.go": `package kernel

import (
	"github.com/falkcorp/mediacore/health/diagnostics"
	"github.com/falkcorp/mediacore/internal/healthhttp"
)

func Boot() { e, _ := diagnostics.New(); _ = healthhttp.Register(e) }
`,
			},
		} {
			got, err := F803bGateFindings(gateTree(t, "false", extra))
			if err == nil || !strings.Contains(err.Error(), "false is not a valid state") || !strings.Contains(err.Error(), f803bMarker) {
				t.Errorf("%s: a false marker gave findings %v, err = %v; want the not-a-valid-state error", name, got, err)
			}
		}
	})

	t.Run("MarkerMissingOrOddIsRefused", func(t *testing.T) {
		for name, files := range map[string]map[string]string{
			"removed":     {"health/diagnostics/paths.go": "package diagnostics\n"},
			"not literal": {"health/diagnostics/paths.go": "package diagnostics\n\nconst RedesignPendingF8_03b = 1 == 1\n"},
			"renamed":     {"health/diagnostics/paths.go": "package diagnostics\n\nconst RedesignPending = true\n"},
		} {
			if _, err := F803bGateFindings(gateTree(t, "true", files)); err == nil {
				t.Errorf("%s: a marker the guard cannot read passed", name)
			}
		}
	})

	// Round 10, N-1: no symbol name is consulted, so whatever the scanner is
	// called, wherever it is declared (a function, a method of any receiver,
	// nowhere, a test file), a false marker is refused. Round 9 tied the marker to
	// the scanner's names and a method receiver defeated it; this pins the shapes
	// that did.
	t.Run("MarkerFalseIsRefusedWhateverTheScannerIs", func(t *testing.T) {
		const decl = "package diagnostics\n\nconst RedesignPendingF8_03b = false\n\n"
		for name, files := range map[string]map[string]string{
			"scanPaths function":  {"health/diagnostics/paths.go": decl + "func scanPaths() {}\n"},
			"tokenAt function":    {"health/diagnostics/paths.go": decl + "func tokenAt() {}\n"},
			"method of pathRoots": {"health/diagnostics/paths.go": decl + "type pathRoots []string\n\nfunc (r pathRoots) scanPaths() {}\nfunc (r pathRoots) tokenAt() {}\n"},
			"renamed scanner":     {"health/diagnostics/paths.go": decl + "func scanText() {}\n"},
			"no scanner":          {"health/diagnostics/paths.go": decl},
			"test file only":      {"health/diagnostics/paths.go": decl, "health/diagnostics/paths_test.go": "package diagnostics\n\nfunc scanPaths() {}\n"},
		} {
			if _, err := F803bGateFindings(gateTree(t, "false", files)); err == nil || !strings.Contains(err.Error(), "false is not a valid state") {
				t.Errorf("%s with a false marker: err = %v, want the not-a-valid-state error", name, err)
			}
		}
		// Marker true with a scanner of any shape is the pending state, not an error.
		files := map[string]string{"health/diagnostics/paths.go": "package diagnostics\n\nconst RedesignPendingF8_03b = true\n\ntype pathRoots []string\n\nfunc (r pathRoots) scanPaths() {}\nfunc scanPaths() {}\nfunc tokenAt() {}\n"}
		if got, err := F803bGateFindings(gateTree(t, "true", files)); err != nil || len(got) != 0 {
			t.Errorf("true marker with the scanner: %v, %v; want no findings", got, err)
		}
	})

	// Round 11, O-1: the marker is read from every file of the package, built or
	// not, and a second declaration is an error. A build-tagged pair (one false,
	// one true) used to pass: the reader saw only the files some tag set builds and
	// kept the last declaration it parsed, so the false file, under the production
	// image's embed_frontend tag included, opened the refusal in that build alone.
	t.Run("TaggedMarkerPairIsRefused", func(t *testing.T) {
		pair := func(tag, falseFile, trueFile string) map[string]string {
			return map[string]string{
				"health/diagnostics/paths.go":     "package diagnostics\n",
				"health/diagnostics/" + falseFile: "//go:build " + tag + "\n\npackage diagnostics\n\nconst RedesignPendingF8_03b = false\n",
				"health/diagnostics/" + trueFile:  "//go:build !" + tag + "\n\npackage diagnostics\n\nconst RedesignPendingF8_03b = true\n",
			}
		}
		for name, files := range map[string]map[string]string{
			"embed_frontend, false file sorts first": pair("embed_frontend", "aa_marker.go", "zz_marker.go"),
			"embed_frontend, false file sorts last":  pair("embed_frontend", "zz_marker.go", "aa_marker.go"),
			"integration, a tag in the union":        pair("integration", "aa_marker.go", "zz_marker.go"),
			"shipit, a tag no set names":             pair("shipit", "aa_marker.go", "zz_marker.go"),
			// the false declaration is in a file no tag set builds
			"false in an ignored file": {
				"health/diagnostics/ignored.go": "//go:build neverbuilt\n\npackage diagnostics\n\nconst RedesignPendingF8_03b = false\n",
			},
		} {
			got, err := F803bGateFindings(gateTree(t, "true", files))
			if err == nil {
				t.Errorf("%s: a tagged marker pair passed the gate (findings %v)", name, got)
				continue
			}
			if !strings.Contains(err.Error(), "false is not a valid state") && !strings.Contains(err.Error(), "declared 2 times") {
				t.Errorf("%s: err = %v, want the false or declared-twice error", name, err)
			}
		}
	})

	t.Run("MarkerDeclaredTwiceIsRefused", func(t *testing.T) {
		// Two true declarations compile only under exclusive tags, and each build
		// would read a different file: one declaration is the rule.
		files := map[string]string{
			"health/diagnostics/paths.go":    "package diagnostics\n",
			"health/diagnostics/marker_a.go": "//go:build integration\n\npackage diagnostics\n\nconst RedesignPendingF8_03b = true\n",
			"health/diagnostics/marker_b.go": "//go:build !integration\n\npackage diagnostics\n\nconst RedesignPendingF8_03b = true\n",
		}
		if _, err := F803bGateFindings(gateTree(t, "true", files)); err == nil || !strings.Contains(err.Error(), "declared 2 times") {
			t.Errorf("two true declarations: err = %v, want the declared-twice error", err)
		}
		// A grouped const that declares it once is still once.
		grouped := map[string]string{"health/diagnostics/paths.go": "package diagnostics\n\nconst (\n\tother = 1\n\tRedesignPendingF8_03b = true\n)\n"}
		if got, err := F803bGateFindings(gateTree(t, "true", grouped)); err != nil || len(got) != 0 {
			t.Errorf("one declaration in a const group: %v, %v; want none", got, err)
		}
	})

	// Round 11, O-1 (b): no non-test file of diagnostics or healthhttp may carry a
	// build constraint while the marker stands, so there is no tagged copy of New,
	// Build or the marker beside the refusal.
	t.Run("BuildConstraintInDiagnosticsOrHealthhttp", func(t *testing.T) {
		const copyOfNew = "package diagnostics\n\nfunc New2() {}\n"
		for name, tc := range map[string]struct {
			files map[string]string
			file  string
			want  string
		}{
			"go:build in diagnostics":      {map[string]string{"health/diagnostics/tagged.go": "//go:build integration\n\n" + copyOfNew}, "health/diagnostics/tagged.go", "build constraint"},
			"go:build !tag in diagnostics": {map[string]string{"health/diagnostics/prod.go": "//go:build !integration\n\n" + copyOfNew}, "health/diagnostics/prod.go", "build constraint"},
			"plus build in diagnostics":    {map[string]string{"health/diagnostics/old.go": "// +build integration\n\n" + copyOfNew}, "health/diagnostics/old.go", "build constraint"},
			"file nobody builds":           {map[string]string{"health/diagnostics/never.go": "//go:build ignore\n\n" + copyOfNew}, "health/diagnostics/never.go", "build constraint"},
			"GOOS suffix in diagnostics":   {map[string]string{"health/diagnostics/build_windows.go": copyOfNew}, "health/diagnostics/build_windows.go", "file name suffix _windows"},
			"GOOS_GOARCH suffix":           {map[string]string{"health/diagnostics/build_linux_arm64.go": copyOfNew}, "health/diagnostics/build_linux_arm64.go", "file name suffix _linux_arm64"},
			"GOARCH suffix in healthhttp":  {map[string]string{"internal/healthhttp/routes_amd64.go": "package healthhttp\n"}, "internal/healthhttp/routes_amd64.go", "file name suffix _amd64"},
			"go:build in healthhttp":       {map[string]string{"internal/healthhttp/extra.go": "//go:build linux\n\npackage healthhttp\n"}, "internal/healthhttp/extra.go", "build constraint"},
		} {
			root := gateTree(t, "true", tc.files)
			got, err := F803bGateFindings(root)
			if err != nil {
				t.Errorf("%s: %v", name, err)
				continue
			}
			by := findingsByFile(root, got)
			if by[tc.file] != 1 || len(got) != 1 {
				t.Errorf("%s: want one report in %s, got:\n  %s", name, tc.file, strings.Join(got, "\n  "))
				continue
			}
			if !strings.Contains(got[0], tc.want) || !strings.HasSuffix(got[0], f803bConstraintNote) {
				t.Errorf("%s: finding %q lacks %q or the constraint note", name, got[0], tc.want)
			}
		}
		// None of these is a constraint: a test file's, a constraint-looking
		// comment after the package clause, a name that merely contains an OS, and
		// a constraint in a package the rule does not cover.
		root := gateTree(t, "true", map[string]string{
			"health/diagnostics/only_test.go":   "//go:build integration\n\npackage diagnostics\n",
			"health/diagnostics/late.go":        "package diagnostics\n\n//go:build integration\n\nfunc Late() {}\n",
			"health/diagnostics/windows.go":     "package diagnostics\n\nfunc Win() {}\n",
			"health/diagnostics/linux_notes.go": "package diagnostics\n\nfunc Notes() {}\n",
			"kernel/kernel_linux.go":            "package kernel\n\nfunc Linux() {}\n",
			"kernel/tagged.go":                  "//go:build integration\n\npackage kernel\n",
		})
		if got := findingsFor(t, root); len(got) != 0 {
			t.Errorf("unconstrained files were reported:\n  %s", strings.Join(got, "\n  "))
		}
	})

	// Round 9, n-2: the check is syntactic, so a file that no tag set compiles is
	// parsed too. A file under a tag set that is loaded is reported once.
	t.Run("FilesNoTagSetBuildsAreParsed", func(t *testing.T) {
		const body = `import "github.com/falkcorp/mediacore/internal/healthhttp"

func Boot2() { _ = healthhttp.Register(nil) }
`
		root := gateTree(t, "true", map[string]string{
			"kernel/never.go":      "//go:build neverbuilt\n\npackage kernel\n\n" + body,
			"kernel/ignore.go":     "//go:build ignore\n\npackage main\n\n" + body,
			"kernel/loaded.go":     "//go:build bench\n\npackage kernel\n\n" + body,
			"kernel/never_test.go": "//go:build neverbuilt\n\npackage kernel\n\n" + body,
		})
		got := findingsFor(t, root)
		by := findingsByFile(root, got)
		for _, f := range []string{"kernel/never.go", "kernel/ignore.go", "kernel/loaded.go"} {
			if by[f] != 1 {
				t.Errorf("%s: %d reports, want 1:\n  %s", f, by[f], strings.Join(got, "\n  "))
			}
		}
		if len(got) != 3 {
			t.Errorf("%d reports, want 3 (a test file that no tag set builds is exempt too):\n  %s", len(got), strings.Join(got, "\n  "))
		}
	})

	t.Run("VacuousTreeRefused", func(t *testing.T) {
		root := writeModule(t, map[string]string{
			"go.mod":         "module github.com/falkcorp/mediacore\n\ngo 1.27.0\n",
			"tasks/tasks.go": "package tasks\n",
		})
		if _, err := F803bGateFindings(root); err == nil || !strings.Contains(err.Error(), "vacuous") {
			t.Errorf("a tree without diagnostics: err = %v, want a vacuity error", err)
		}
	})
}

// TestF803bGateOverCore runs the guard over the real core module: while
// diagnostics.RedesignPendingF8_03b is true, nothing outside tests imports
// healthhttp or builds the Exporter, so F8-04 cannot mount the diagnostics
// routes before F8-03b, and the refusal's probe is testing.Testing.
func TestF803bGateOverCore(t *testing.T) {
	F803bGate(t)
}

// TestF803bGateOverCoreRefusesAFalseMarker (round 10, N-1) runs the gate over the
// real core tree with the marker read as false through the injected reader (no
// file is edited): the gate must fail. Round 9's gate passed this, because its
// tie to the scanner counted free functions and the real scanner is two methods
// of pathRoots. The reader first reads the real marker and requires it true, so
// the test also fails if the marker is no longer the literal the gate reads.
func TestF803bGateOverCoreRefusesAFalseMarker(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root, err := ModuleRoot(wd)
	if err != nil {
		t.Fatal(err)
	}
	var read int
	asFalse := func(files []string) (bool, error) {
		real, err := f803bReadMarker(files)
		if err != nil {
			return false, err
		}
		if !real {
			t.Errorf("the real marker is already false; the test expects true to flip")
		}
		read++
		return false, nil
	}
	got, err := f803bFindings(root, asFalse)
	if read != 1 {
		t.Fatalf("the injected reader ran %d times, want 1 (err = %v)", read, err)
	}
	if err == nil || !strings.Contains(err.Error(), "false is not a valid state") {
		t.Fatalf("the gate over the real core tree with a false marker: findings %v, err = %v; want the not-a-valid-state error", got, err)
	}
	// And with the real marker the same call is clean.
	if got, err := f803bFindings(root, f803bReadMarker); err != nil || len(got) != 0 {
		t.Errorf("the gate over the real core tree: %v, %v; want no findings", got, err)
	}
}
