// file: core/archtest/tasklint/testdata/src/github.com/falkcorp/mediacore/internal/tasklintcases/unregistered_kind_nolist/nolist.go
// version: 2.0.1
// guid: 9f0307f0-44c6-40e8-9910-3bb995d26b79
// last-edited: 2026-11-10

// Package unregistered_kind_nolist builds a kind and has neither a
// Registrar.Tasks call nor a list function.
package unregistered_kind_nolist

import "github.com/falkcorp/mediacore/tasks"

var Lonely = tasks.Task("nolist.lonely", tasks.TaskSpec[tasks.NoParams, tasks.NoResult]{Common: tasks.Common{Help: "synthetic"}}) // want `tasks.Task definition in Lonely is not registered: no Registrar.Tasks call in this package and no called list function returning \[\]tasks.Definition reaches it`

// helper returns one Definition, not a []tasks.Definition: not a list function.
func helper() tasks.Definition {
	return tasks.Batch("nolist.helper", tasks.BatchSpec[tasks.NoParams, string, tasks.NoResult]{Common: tasks.Common{Help: "synthetic"}, Parallelism: tasks.CPU()}) // want `tasks.Batch definition in helper is not registered`
}

// (t) Kinds is a method, not a package-level func: not a list function.
type t struct{}

func (t) Kinds() []tasks.Definition {
	return []tasks.Definition{tasks.Task("nolist.method", tasks.TaskSpec[tasks.NoParams, tasks.NoResult]{Common: tasks.Common{Help: "synthetic"}})} // want `tasks.Task definition in Kinds is not registered`
}
