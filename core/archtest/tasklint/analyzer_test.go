// file: core/archtest/tasklint/analyzer_test.go
// version: 2.0.1
// guid: e8def934-7124-4f87-a67e-9f206a103d30
// last-edited: 2026-11-10

package tasklint

import (
	"slices"
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"
)

const (
	cases     = "github.com/falkcorp/mediacore/internal/tasklintcases/"
	caseStore = cases + "store"
)

// fixtureConfig is DefaultConfig with the fixtures' store package.
func fixtureConfig() Config {
	cfg := DefaultConfig()
	cfg.StorePackages = []string{caseStore}
	cfg.Constructors = []string{cases + "unregistered_kind.NewDerived"}
	return cfg
}

// TestAnalyzer runs one fixture package per rule under
// testdata/src/github.com/falkcorp/mediacore/internal/tasklintcases, each with
// // want annotations; clean must produce nothing, and the fake tasks test kit
// is never reported. The fakes of core's tasks, tasks/taskstest and module
// packages live under their real import paths in the same GOPATH-style tree.
func TestAnalyzer(t *testing.T) {
	for _, pkg := range []string{
		cases + "unregistered_kind",
		cases + "writer_bypass",
		cases + "store_capture",
		cases + "store_capture_method",
		cases + "sequential_empty_reason",
		cases + "deletes_owner_review",
		cases + "missing_help",
		cases + "clean",
		"github.com/falkcorp/mediacore/tasks/taskstest",
	} {
		t.Run(pkg, func(t *testing.T) {
			analysistest.Run(t, analysistest.TestData(), New(fixtureConfig()), pkg)
		})
	}
}

// TestAnalyzer_ListFuncs: with ListFuncs a package-level func returning
// []tasks.Definition is a registration root (books before the F5 flip,
// 05-tasks §5T.8.2 rule 4); without it the same package's list is unregistered
// (the unregistered_kind fixture's Kinds). A method, or a func returning one
// Definition, is not a list function (unregistered_kind_nolist).
func TestAnalyzer_ListFuncs(t *testing.T) {
	cfg := fixtureConfig()
	cfg.ListFuncs = true
	analysistest.Run(t, analysistest.TestData(), New(cfg), cases+"list_funcs", cases+"unregistered_kind_nolist")
}

// TestAnalyzer_LegacyDefinitions: a type named by LegacyDefinitions is a
// definition for writer-bypass and store-capture.
func TestAnalyzer_LegacyDefinitions(t *testing.T) {
	cfg := fixtureConfig()
	cfg.LegacyDefinitions = []string{"github.com/falkcorp/mediacore/internal/legacyengine.OperationDef"}
	analysistest.Run(t, analysistest.TestData(), New(cfg), cases+"legacy_defs")
}

// TestAnalyzer_OutsideModuleNotReported: a package outside -module is never
// reported, whatever it contains.
func TestAnalyzer_OutsideModuleNotReported(t *testing.T) {
	cfg := fixtureConfig()
	cfg.ModulePath = "github.com/falkcorp/elsewhere"
	// The fixture's // want lines go unmatched here by design; only the
	// absence of diagnostics is asserted.
	res := analysistest.Run(discard{}, analysistest.TestData(), New(cfg), cases+"unregistered_kind_nolist")
	if len(res) != 1 {
		t.Fatalf("%d results, want 1", len(res))
	}
	for _, r := range res {
		if len(r.Diagnostics) != 0 {
			t.Errorf("%s: %d findings outside the module, want none", r.Pass.Pkg.Path(), len(r.Diagnostics))
		}
	}
}

// discard is an analysistest.Testing that drops every error.
type discard struct{}

func (discard) Errorf(string, ...any) {}

// TestAnalyzer_FlagsConfigure: every hard-coded path of HEAD's oplint is a
// flag of the analyzer (06 §6.11.4 row H), parsed into that analyzer's own
// configuration only.
func TestAnalyzer_FlagsConfigure(t *testing.T) {
	a := New(DefaultConfig())
	for _, name := range []string{"module", "tasks", "taskstest", "registrar", "stores", "legacy-defs", "list-funcs"} {
		if a.Flags.Lookup(name) == nil {
			t.Errorf("flag -%s missing", name)
		}
	}
	if err := a.Flags.Parse([]string{"-module=example.com/m", "-stores=example.com/m/a, example.com/m/b", "-list-funcs",
		"-legacy-defs=example.com/m/old.Def"}); err != nil {
		t.Fatal(err)
	}
	if got := a.Flags.Lookup("stores").Value.String(); got != "example.com/m/a,example.com/m/b" {
		t.Errorf("-stores = %q", got)
	}
	if Analyzer().Flags.Lookup("module").Value.String() != "github.com/falkcorp/mediacore" {
		t.Error("parsing one analyzer's flags changed a fresh Analyzer's configuration")
	}
	if got := (listFlag{}).String(); got != "" {
		t.Errorf("zero listFlag = %q", got)
	}
	// New copies its slices: the caller's config is never mutated.
	cfg := DefaultConfig()
	cfg.StorePackages = []string{"x"}
	b := New(cfg)
	if err := b.Flags.Parse([]string{"-stores=y"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cfg.StorePackages, []string{"x"}) {
		t.Errorf("caller's StorePackages mutated to %v", cfg.StorePackages)
	}
}
