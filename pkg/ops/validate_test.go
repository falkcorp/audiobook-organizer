// file: pkg/ops/validate_test.go
// version: 1.0.1
// guid: c975d2f3-cff0-4c7b-9f24-535762042e04
// last-edited: 2026-10-19

package ops

import (
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
)

type fakeOrigin string

func (o fakeOrigin) AdaptedDefID() string { return string(o) }

func okTask(id string) Definition {
	return Task(id, TaskSpec[NoParams, NoResult]{Common: readOnlyCommon(id),
		Run: func(*RC, NoParams) (NoResult, error) { return NoResult{}, nil }})
}

func okBatch(id string, c Common, conc Concurrency) Definition {
	return Batch(id, BatchSpec[NoParams, string, NoResult]{Common: c, Concurrency: conc,
		Source: func(*RC, NoParams) (Source[string], error) { return Source[string]{}, nil },
		Item:   func(*ItemRC, NoParams, string) error { return nil }})
}

func okFixer(id string, e Effects) Definition {
	return Fixer(id, FixerSpec[NoParams, string]{
		Common:     Common{Title: id, Effects: e, Permission: auth.PermSettingsManage},
		Candidates: func(*RC, NoParams) (Source[string], error) { return Source[string]{}, nil },
		Load:       func(*RC, NoParams, string) (string, bool, error) { return "", false, nil },
		Evaluate:   func(*ItemRC, NoParams, string) (Row, error) { return Row{}, nil },
		Apply:      func(*ItemRC, Writer, NoParams, Row) error { return nil },
	})
}

func refusals(err error) map[Refusal]bool {
	out := map[Refusal]bool{}
	var walk func(error)
	walk = func(e error) {
		if ce, ok := errors.AsType[*CatalogError](e); ok {
			out[ce.Refusal] = true
		}
		if j, ok := e.(interface{ Unwrap() []error }); ok {
			for _, x := range j.Unwrap() {
				walk(x)
			}
		}
	}
	if err != nil {
		walk(err)
	}
	return out
}

func TestValidateCatalogAcceptsAGoodCatalog(t *testing.T) {
	child := okTask("c.child")
	pipe := Pipeline("p.pipe", PipelineSpec[NoParams]{Common: Common{Title: "p", Permission: auth.PermSettingsManage},
		Stages: []Stage[NoParams]{{Name: "one", Def: child}}})
	defs := []Definition{okTask("a.one"), child, pipe,
		okBatch("b.one", Common{Title: "b", Effects: Writes(ResBooks), Permission: auth.PermSettingsManage}, Sequential("one row at a time")),
		okFixer("f.one", Writes(ResBooks)),
		Adapted("v2.def", Common{Title: "legacy"}, fakeOrigin("v2.def"))}
	if err := ValidateCatalog(defs, nil); err != nil {
		t.Fatalf("good catalog refused: %v", err)
	}
	if !Compile(pipe).Common.Effects.IsReadOnly() {
		t.Fatal("a pipeline of read-only children is read-only")
	}
}

// One case per refusal. Each catalog is otherwise valid, so the refusal under
// test is the only reason it fails.
func TestValidateCatalogRefusals(t *testing.T) {
	writes := Common{Title: "w", Effects: Writes(ResBooks), Permission: auth.PermSettingsManage}
	cases := []struct {
		name string
		defs []Definition
		want Refusal
	}{
		{"invalid (no Run)", []Definition{Task("t", TaskSpec[NoParams, NoResult]{Common: readOnlyCommon("t")})}, RefuseInvalid},
		{"duplicate id", []Definition{okTask("same"), okTask("same")}, RefuseDuplicateID},
		{"former id collides", []Definition{okTask("a"), Task("b", TaskSpec[NoParams, NoResult]{
			Common: Common{Title: "b", Effects: ReadOnly(), Permission: auth.PermLibraryView, FormerIDs: []string{"a"}},
			Run:    func(*RC, NoParams) (NoResult, error) { return NoResult{}, nil }})}, RefuseDuplicateID},
		{"invalid schedule", []Definition{Task("s", TaskSpec[NoParams, NoResult]{
			Common: Common{Title: "s", Effects: ReadOnly(), Permission: auth.PermLibraryView, Schedule: Cron("not a cron")},
			Run:    func(*RC, NoParams) (NoResult, error) { return NoResult{}, nil }})}, RefuseSchedule},
		{"no effects", []Definition{Task("e", TaskSpec[NoParams, NoResult]{
			Common: Common{Title: "e", Permission: auth.PermLibraryView},
			Run:    func(*RC, NoParams) (NoResult, error) { return NoResult{}, nil }})}, RefuseNoEffects},
		{"fixer without writes", []Definition{okFixer("f", ReadOnly(ResBooks))}, RefuseFixerReadOnly},
		{"native without permission", []Definition{okBatch("np", Common{Title: "np", Effects: Writes(ResBooks)}, CPU())}, RefuseNoPermission},
		{"sequential without a reason", []Definition{okBatch("sq", writes, Sequential(""))}, RefuseSequentialNoReason},
		{"stage not in catalog", []Definition{Pipeline("p", PipelineSpec[NoParams]{
			Common: Common{Title: "p", Permission: auth.PermSettingsManage},
			Stages: []Stage[NoParams]{{Name: "one", Def: okTask("missing")}}})}, RefuseStageNotInCatalog},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := refusals(ValidateCatalog(tc.defs, nil))
			if !got[tc.want] {
				t.Fatalf("want refusal %s, got %v", tc.want, got)
			}
		})
	}
}

func TestValidateCatalogAdaptedNeedsNoPermission(t *testing.T) {
	if err := ValidateCatalog([]Definition{Adapted("v2", Common{Title: "v2"}, fakeOrigin("v2"))}, nil); err != nil {
		t.Fatalf("an adapted v2 def relies on X2's default: %v", err)
	}
	if got := refusals(ValidateCatalog([]Definition{Adapted("v2", Common{Title: "v2"}, nil)}, nil)); !got[RefuseInvalid] {
		t.Fatalf("an adapted def without an origin is invalid, got %v", got)
	}
}

func TestValidateCatalogLedgerCheckIsStubbed(t *testing.T) {
	err := ValidateCatalog([]Definition{okTask("a")}, []string{"a"})
	if !errors.Is(err, ErrLedgerCheckPending) {
		t.Fatalf("err = %v, want ErrLedgerCheckPending", err)
	}
}

func TestPipelineCycleAndUnknownStage(t *testing.T) {
	a := okTask("a")
	p := Pipeline("p", PipelineSpec[NoParams]{Common: Common{Title: "p", Permission: auth.PermSettingsManage},
		Stages: []Stage[NoParams]{{Name: "x", Def: a, After: []string{"y"}}, {Name: "y", Def: a, After: []string{"x"}},
			{Name: "z", Def: a, After: []string{"nope"}}}})
	probs := Compile(p).Problems
	if len(probs) < 2 {
		t.Fatalf("problems = %v", probs)
	}
}
