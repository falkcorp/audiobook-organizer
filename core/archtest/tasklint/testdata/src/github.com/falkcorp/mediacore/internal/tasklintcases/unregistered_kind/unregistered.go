// file: core/archtest/tasklint/testdata/src/github.com/falkcorp/mediacore/internal/tasklintcases/unregistered_kind/unregistered.go
// version: 2.4.0
// guid: 705d94f0-04f3-4c33-b047-d377eaebfebe
// last-edited: 2026-11-11

// Package unregistered_kind: some kinds reach Registrar.Tasks, some do not.
package unregistered_kind

import (
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/tasks"
)

var common = tasks.Common{Title: "t", Help: "synthetic"}

var listed = tasks.Task("unreg.listed", tasks.TaskSpec[tasks.NoParams, tasks.NoResult]{Common: common})

// viaHelper is reached through a helper the registration calls.
func viaHelper() tasks.Definition {
	return tasks.Batch("unreg.via-helper", tasks.BatchSpec[tasks.NoParams, string, tasks.NoResult]{Common: common, Parallelism: tasks.Sequential("a single remote session")})
}

var forgotten = tasks.Task("unreg.forgotten", tasks.TaskSpec[tasks.NoParams, tasks.NoResult]{Common: common}) // want `tasks.Task definition in forgotten is not registered: no Registrar call in this package reaches it`

func orphan() tasks.Definition {
	return tasks.Fixer("unreg.orphan", tasks.FixerSpec[tasks.NoParams, string]{Common: common}) // want `tasks.Fixer definition in orphan is not registered`
}

// Kinds returns []tasks.Definition, but list functions count only with
// -list-funcs, which this package's run does not set.
func Kinds() []tasks.Definition {
	return []tasks.Definition{tasks.Task("unreg.list", tasks.TaskSpec[tasks.NoParams, tasks.NoResult]{Common: common})} // want `tasks.Task definition in Kinds is not registered`
}

// build is a kind helper: each of its call sites is a kind site, so the
// registered call below does not cover orphanBuilt's.
func build(kind tasks.Kind) tasks.Definition {
	return tasks.Task(kind, tasks.TaskSpec[tasks.NoParams, tasks.NoResult]{Common: common})
}

// rebuild is a helper of a helper (found to a fixed point).
func rebuild(kind tasks.Kind) tasks.Definition { return build(kind) }

func orphanBuilt() tasks.Definition {
	return build("unreg.orphan-built") // want `definition built by build in orphanBuilt is not registered`
}

func orphanRebuilt() []tasks.Definition {
	return []tasks.Definition{rebuild("unreg.orphan-rebuilt")} // want `definition built by rebuild in orphanRebuilt is not registered`
}

// neverCalled is a helper with no call site: its own constructor is reported.
func neverCalled() tasks.Definition {
	return tasks.Task("unreg.never-called", tasks.TaskSpec[tasks.NoParams, tasks.NoResult]{Common: common}) // want `tasks.Task definition in neverCalled is not registered`
}

// NewDerived is an exported kind constructor for other modules: nobody here
// calls it, so its body is not checked by coverage; a kind it discards still
// is reported.
//
//tasklint:constructor modules pass the result to Registrar.DerivedIndex
func NewDerived(name string) tasks.Definition {
	_ = tasks.Task("unreg.discarded-in-constructor", tasks.TaskSpec[tasks.NoParams, tasks.NoResult]{Common: common}) // want `tasks.Task definition in NewDerived is built and discarded`
	return tasks.Task(tasks.Kind(name), tasks.TaskSpec[tasks.NoParams, tasks.NoResult]{Common: common})
}

// Unlisted carries the directive with a reason, but the fixture's
// Constructors list does not name it: the directive is refused and its body
// is checked as any uncalled helper's.
//
//tasklint:constructor modules call it
func Unlisted() tasks.Definition { // want `//tasklint:constructor on Unlisted, which Config.Constructors`
	return tasks.Task("unreg.unlisted", tasks.TaskSpec[tasks.NoParams, tasks.NoResult]{Common: common}) // want `tasks.Task definition in Unlisted is not registered`
}

// withDiscard is a called helper that also builds a kind it throws away.
func withDiscard(kind tasks.Kind) tasks.Definition {
	_ = build("unreg.discarded-in-helper") // want `definition built by build in withDiscard is built and discarded`
	return build(kind)
}

// A reached function is covered whole; what it builds and discards is not.
func reachedList() []tasks.Definition {
	kept := []tasks.Definition{build("unreg.kept")}
	dropped := build("unreg.dropped") // want `definition built by build in reachedList is built and discarded`
	_ = dropped
	build("unreg.bare")                       // want `definition built by build in reachedList is built and discarded`
	_, err := pairBuild("unreg.pair-dropped") // want `definition built by pairBuild in reachedList is built and discarded`
	_ = err
	var reassigned tasks.Definition
	reassigned = build("unreg.reassigned") // want `definition built by build in reachedList is built and discarded`
	_ = reassigned
	var _ = build("unreg.var-blank") // want `definition built by build in reachedList is built and discarded`
	d, _ := pairBuild("unreg.pair-kept")
	return append(kept, d)
}

// pairBuild is a kind helper with a second result.
func pairBuild(kind tasks.Kind) (tasks.Definition, error) { return build(kind), nil }

// closures: a closure in a reached function is covered with its body.
func closures() tasks.Definition {
	f := func() tasks.Definition {
		return build("unreg.closure-only")
	}
	_ = f
	return build("unreg.closure-outer")
}

// fromMake fills a made slice by index.
func fromMake(kinds []tasks.Kind) []tasks.Definition {
	out := make([]tasks.Definition, len(kinds))
	for i, k := range kinds {
		out[i] = build(k)
	}
	out[0] = build("unreg.make-index")
	return out
}

// fromAdder appends through an adder closure.
func fromAdder() []tasks.Definition {
	var out []tasks.Definition
	add := func(d tasks.Definition) { out = append(out, d) }
	add(build("unreg.adder"))
	return out
}

// fromTable builds kinds in a table of rows and appends each row's.
func fromTable() []tasks.Definition {
	rows := []struct {
		def  tasks.Definition
		note string
	}{
		{def: build("unreg.table"), note: "t"},
	}
	var out []tasks.Definition
	for _, row := range rows {
		out = append(out, row.def)
	}
	return out
}

// named returns through a named result and a bare return.
func named() (d tasks.Definition) {
	d = build("unreg.named")
	return
}

// NoReason is an exported constructor whose directive gives no reason.
//
//tasklint:constructor
func NoReason() tasks.Definition { // want `//tasklint:constructor on NoReason needs the reason`
	return tasks.Task("unreg.no-reason", tasks.TaskSpec[tasks.NoParams, tasks.NoResult]{Common: common}) // want `tasks.Task definition in NoReason is not registered`
}

// hidden is unexported: the directive cannot make it a constructor.
//
//tasklint:constructor it is not exported
func hidden() tasks.Definition { // want `//tasklint:constructor on hidden: only an exported kind helper`
	return tasks.Task("unreg.hidden", tasks.TaskSpec[tasks.NoParams, tasks.NoResult]{Common: common}) // want `tasks.Task definition in hidden is not registered`
}

func Register(r *module.Registrar) {
	built := tasks.Task("unreg.built-not-passed", tasks.TaskSpec[tasks.NoParams, tasks.NoResult]{Common: common}) // want `tasks.Task definition in Register is built and discarded`
	_ = built
	r.Tasks(listed, viaHelper(), tasks.Task("unreg.inline", tasks.TaskSpec[tasks.NoParams, tasks.NoResult]{Common: common}),
		build("unreg.built"), rebuild("unreg.rebuilt"))
	r.DerivedIndex("hnsw", nil, build("unreg.derived"))
	r.Tasks(withDiscard("unreg.with-discard"))
	r.Tasks(reachedList()...)
	r.Tasks(named(), closures())
	r.Tasks(fromMake([]tasks.Kind{"unreg.make"})...)
	r.Tasks(fromAdder()...)
	r.Tasks(fromTable()...)
	r.Other(build("unreg.other")) // want `definition built by build in Register is not registered`
	r.Resources("unreg.resource")
}
