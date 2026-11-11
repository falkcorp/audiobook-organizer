// file: core/archtest/tasklint/testdata/src/github.com/falkcorp/mediacore/internal/tasklintcases/xpkg/owner/owner.go
// version: 1.0.0
// guid: b070e7d6-6df0-4c15-aa5c-09449d1f6d0a
// last-edited: 2026-11-10

// Package owner builds kinds and registers none: the kernel package registers
// them (05-tasks §5T.8.4, the whole-program pass).
package owner

import "github.com/falkcorp/mediacore/tasks"

var common = tasks.Common{Title: "t", Help: "synthetic"}

// Single is registered by the kernel by name.
var Single = tasks.Task("owner.single", tasks.TaskSpec[tasks.NoParams, tasks.NoResult]{Common: common})

// Build is a kind helper other packages call.
func Build(kind tasks.Kind) tasks.Definition {
	return tasks.Task(kind, tasks.TaskSpec[tasks.NoParams, tasks.NoResult]{Common: common})
}

// Kinds is passed to Registrar.Tasks by the kernel.
func Kinds() []tasks.Definition {
	return []tasks.Definition{Build("owner.listed"), Batch{}.Def()}
}

// Batch builds a kind through a method.
type Batch struct{}

func (Batch) Def() tasks.Definition {
	return tasks.Batch("owner.batch", tasks.BatchSpec[tasks.NoParams, string, tasks.NoResult]{Common: common, Parallelism: tasks.CPU()})
}
