// file: core/tasks/taskstest/conformance.go
// version: 1.0.2
// guid: 9e791d16-82f5-4c80-8314-22f8b1820dab
// last-edited: 2026-11-11

// Package taskstest is the test kit for code built on package tasks:
// Conformance, which modules run over their own definitions, and FakeRun, an
// in-memory run handle for code that takes a *tasks.RC.
package taskstest

import (
	"fmt"
	"testing"

	"github.com/falkcorp/mediacore/tasks"
)

// The assertion names: one subtest each, so a failure points at the rule.
const (
	AssertNoProblems          = "no_constructor_problems"
	AssertKindParses          = "kind_parses"
	AssertRequiredCommon      = "required_common"
	AssertParallelismDeclared = "parallelism_declared"
	AssertFixerApproval       = "fixer_approval"
	AssertUniqueKinds         = "unique_kinds"
)

// Conformance proves each definition in defs is registrable, one subtest per
// definition and rule:
//
//   - no_constructor_problems: tasks.Problems reports nothing;
//   - kind_parses: the kind matches the Kind grammar;
//   - required_common: Title, a declared Effects, Perms.Start and a Resume
//     policy are set (A2: the zero ResumePolicy is refused);
//   - parallelism_declared (Batch, Fixer): the Parallelism validates (N8, A1);
//   - fixer_approval (Fixer): Approval is set with PlanRequired and Effects are Writes;
//   - unique_kinds: no two definitions share a kind.
//
// An empty Help is logged, not failed (archtest warns on it).
//
// This is the static half of the re-implementation of
// the v3 opstest package's conformance.go (04 §8.6). Its runtime assertions (preview
// writes nothing, no write after the fence, crash/resume at least once,
// partition order, progress monotone, idempotent items) need the executor and
// join this function with it (F2-03).
func Conformance(t *testing.T, defs ...tasks.Definition) {
	t.Helper()
	conformance(tAdapter{t}, defs...)
}

// tester is the part of *testing.T Conformance reports through, so the self
// test can run it against a recorder and assert which subtest failed.
type tester interface {
	Helper()
	Logf(format string, args ...any)
	Errorf(format string, args ...any)
	Run(name string, f func(tester)) bool
}

type tAdapter struct{ t *testing.T }

func (a tAdapter) Helper()                           { a.t.Helper() }
func (a tAdapter) Logf(format string, args ...any)   { a.t.Helper(); a.t.Logf(format, args...) }
func (a tAdapter) Errorf(format string, args ...any) { a.t.Helper(); a.t.Errorf(format, args...) }
func (a tAdapter) Run(name string, f func(tester)) bool {
	return a.t.Run(name, func(t *testing.T) { f(tAdapter{t}) })
}

func conformance(tt tester, defs ...tasks.Definition) {
	tt.Helper()
	if len(defs) == 0 {
		tt.Errorf("Conformance got no definition; the check would be vacuous")
		return
	}
	seen := make(map[tasks.Kind]int, len(defs))
	for i, d := range defs {
		name := fmt.Sprintf("def_%d", i)
		if d != nil {
			name = string(d.Kind())
			seen[d.Kind()]++
		}
		tt.Run(name, func(tt tester) { checkDefinition(tt, d) })
	}
	tt.Run(AssertUniqueKinds, func(tt tester) {
		for k, n := range seen {
			if n > 1 {
				tt.Errorf("kind %s is defined %d times", k, n)
			}
		}
	})
}

func checkDefinition(tt tester, d tasks.Definition) {
	tt.Helper()
	if d == nil {
		tt.Errorf("nil Definition")
		return
	}
	c := tasks.CommonOf(d)
	tt.Run(AssertNoProblems, func(tt tester) {
		for _, p := range tasks.Problems(d) {
			tt.Errorf("%v", p)
		}
	})
	tt.Run(AssertKindParses, func(tt tester) {
		if _, err := tasks.ParseKind(string(d.Kind())); err != nil {
			tt.Errorf("%v", err)
		}
	})
	tt.Run(AssertRequiredCommon, func(tt tester) {
		if c.Title == "" {
			tt.Errorf("Title is empty")
		}
		if !c.Effects.Declared() {
			tt.Errorf("Effects are not declared")
		}
		if c.Perms.Start.IsZero() {
			tt.Errorf("Perms.Start is not set")
		}
		if c.Resume < tasks.ResumeContinue || c.Resume > tasks.ResumeAsk {
			tt.Errorf("Resume is %s; a policy is required", c.Resume)
		}
		if c.Help == "" {
			tt.Logf("Help is empty (archtest warns)")
		}
	})
	switch d.Shape() {
	case tasks.ShapeBatch, tasks.ShapeFixer:
		tt.Run(AssertParallelismDeclared, func(tt tester) {
			if err := tasks.ParallelismOf(d).Validate(); err != nil {
				tt.Errorf("Parallelism %s: %v", tasks.ParallelismOf(d), err)
			}
		})
	}
	if d.Shape() == tasks.ShapeFixer {
		tt.Run(AssertFixerApproval, func(tt tester) {
			if c.Approval == nil || !c.Approval.PlanRequired {
				tt.Errorf("a Fixer needs Approval with PlanRequired")
			}
			if c.Effects.IsReadOnly() {
				tt.Errorf("a Fixer's Effects must be Writes(...)")
			}
		})
	}
}
