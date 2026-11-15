// file: core/archtest/f803b_gate_test.go
// version: 1.1.0
// guid: 91c3e5a7-0d48-4f26-8b1e-6a7d2f4c9e03
// last-edited: 2026-11-15

package archtest

import (
	"maps"
	"strings"
	"testing"
)

// gateTree is a fixture core module: diagnostics with the marker and a New and an
// Exporter, healthhttp built on it (a type reference, no construction), a kernel
// that imports neither in non-test code, and a test file that imports healthhttp
// and calls New. pending is the marker's value; extra adds or replaces files.
func gateTree(t *testing.T, pending string, extra map[string]string) string {
	t.Helper()
	files := map[string]string{
		"go.mod": "module github.com/falkcorp/mediacore\n\ngo 1.27.0\n",
		"health/diagnostics/paths.go": `package diagnostics

const RedesignPendingF8_03b = ` + pending + `
`,
		"health/diagnostics/export.go": `package diagnostics

type Exporter struct{}

func New() (*Exporter, error) { return &Exporter{}, nil }
`,
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
// healthhttp's own use of the Exporter type are not; with the marker false
// nothing is reported; and the guard refuses a tree it cannot read the marker
// from.
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

	// The same breaches with the marker false: F8-03b has landed, the gate is open.
	t.Run("MarkerFalseReportsNothing", func(t *testing.T) {
		root := gateTree(t, "false", map[string]string{
			"kernel/kernel.go": `package kernel

import (
	"github.com/falkcorp/mediacore/health/diagnostics"
	"github.com/falkcorp/mediacore/internal/healthhttp"
)

func Boot() { e, _ := diagnostics.New(); _ = healthhttp.Register(e) }
`,
		})
		if got := findingsFor(t, root); len(got) != 0 {
			t.Fatalf("a false marker still reported:\n  %s", strings.Join(got, "\n  "))
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

	// Round 9, m-1: the marker stands for the scanner. A flip to false while
	// scanPaths or tokenAt are still declared in a non-test file is refused, not
	// believed.
	t.Run("MarkerFalseWhileScannerRemainsIsRefused", func(t *testing.T) {
		for name, src := range map[string]string{
			"scanPaths": "package diagnostics\n\nconst RedesignPendingF8_03b = false\n\nfunc scanPaths() {}\n",
			"tokenAt":   "package diagnostics\n\nfunc tokenAt() {}\n\nconst RedesignPendingF8_03b = false\n",
		} {
			_, err := F803bGateFindings(gateTree(t, "false", map[string]string{"health/diagnostics/paths.go": src}))
			if err == nil || !strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), "not a switch") {
				t.Errorf("%s declared with a false marker: err = %v, want the not-a-switch refusal naming it", name, err)
			}
		}
		// Not the scanner: a method of that name, or the function in a test file.
		for name, files := range map[string]map[string]string{
			"method":    {"health/diagnostics/paths.go": "package diagnostics\n\nconst RedesignPendingF8_03b = false\n\ntype t struct{}\n\nfunc (t) scanPaths() {}\n"},
			"test file": {"health/diagnostics/paths_test.go": "package diagnostics\n\nfunc scanPaths() {}\nfunc tokenAt() {}\n"},
		} {
			if _, err := F803bGateFindings(gateTree(t, "false", files)); err != nil {
				t.Errorf("%s: err = %v, want the gate open (not the scanner)", name, err)
			}
		}
		// Marker true with the scanner declared is the pending state, not an error.
		files := map[string]string{"health/diagnostics/paths.go": "package diagnostics\n\nconst RedesignPendingF8_03b = true\n\nfunc scanPaths() {}\nfunc tokenAt() {}\n"}
		if got, err := F803bGateFindings(gateTree(t, "true", files)); err != nil || len(got) != 0 {
			t.Errorf("true marker with the scanner: %v, %v; want no findings", got, err)
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
// routes before F8-03b.
func TestF803bGateOverCore(t *testing.T) {
	F803bGate(t)
}
