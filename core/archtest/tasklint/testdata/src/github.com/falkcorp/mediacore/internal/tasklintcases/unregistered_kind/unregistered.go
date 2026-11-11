// file: core/archtest/tasklint/testdata/src/github.com/falkcorp/mediacore/internal/tasklintcases/unregistered_kind/unregistered.go
// version: 2.2.0
// guid: 705d94f0-04f3-4c33-b047-d377eaebfebe
// last-edited: 2026-11-10

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
// calls it, so its body is not a kind site.
//
//tasklint:constructor modules pass the result to Registrar.DerivedIndex
func NewDerived(name string) tasks.Definition {
	return tasks.Task(tasks.Kind(name), tasks.TaskSpec[tasks.NoParams, tasks.NoResult]{Common: common})
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
	built := tasks.Task("unreg.built-not-passed", tasks.TaskSpec[tasks.NoParams, tasks.NoResult]{Common: common}) // want `tasks.Task definition in Register is not registered`
	_ = built
	r.Tasks(listed, viaHelper(), tasks.Task("unreg.inline", tasks.TaskSpec[tasks.NoParams, tasks.NoResult]{Common: common}),
		build("unreg.built"), rebuild("unreg.rebuilt"))
	r.DerivedIndex("hnsw", nil, build("unreg.derived"))
	r.Resources("unreg.resource")
}
