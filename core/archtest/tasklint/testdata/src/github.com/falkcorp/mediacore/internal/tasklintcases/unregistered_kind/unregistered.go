// file: core/archtest/tasklint/testdata/src/github.com/falkcorp/mediacore/internal/tasklintcases/unregistered_kind/unregistered.go
// version: 2.0.0
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

var forgotten = tasks.Task("unreg.forgotten", tasks.TaskSpec[tasks.NoParams, tasks.NoResult]{Common: common}) // want `tasks.Task definition in forgotten is not registered: no Registrar.Tasks call in this package reaches it`

func orphan() tasks.Definition {
	return tasks.Fixer("unreg.orphan", tasks.FixerSpec[tasks.NoParams, string]{Common: common}) // want `tasks.Fixer definition in orphan is not registered`
}

// Kinds returns []tasks.Definition, but list functions count only with
// -list-funcs, which this package's run does not set.
func Kinds() []tasks.Definition {
	return []tasks.Definition{tasks.Task("unreg.list", tasks.TaskSpec[tasks.NoParams, tasks.NoResult]{Common: common})} // want `tasks.Task definition in Kinds is not registered`
}

func Register(r *module.Registrar) {
	built := tasks.Task("unreg.built-not-passed", tasks.TaskSpec[tasks.NoParams, tasks.NoResult]{Common: common}) // want `tasks.Task definition in Register is not registered`
	_ = built
	r.Tasks(listed, viaHelper(), tasks.Task("unreg.inline", tasks.TaskSpec[tasks.NoParams, tasks.NoResult]{Common: common}))
}
